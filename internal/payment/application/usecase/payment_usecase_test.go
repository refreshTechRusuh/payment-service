package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/refreshTechRusuh/payment-service/internal/payment/application/usecase"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/dto"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/entity"
	"github.com/refreshTechRusuh/payment-service/internal/payment/port/outbound"
	"github.com/refreshTechRusuh/payment-service/pkg/apperror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================
// Mocks
// ============================================================

type mockRepo struct {
	mu        sync.Mutex
	payments  map[string]entity.Payment        // paymentId → payment
	bookings  map[string]entity.BookingSnapshot // bookingId → snapshot
	callbacks map[string]bool                  // idempotency_key → processed
}

func newMockRepo(snap entity.BookingSnapshot) *mockRepo {
	r := &mockRepo{
		payments:  make(map[string]entity.Payment),
		bookings:  map[string]entity.BookingSnapshot{snap.ID: snap},
		callbacks: make(map[string]bool),
	}
	return r
}

func (r *mockRepo) Create(_ context.Context, _ pgx.Tx, p entity.Payment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.payments[p.ID] = p
	return nil
}

func (r *mockRepo) GetByBookingID(_ context.Context, bookingID string) (entity.Payment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.payments {
		if p.BookingID == bookingID {
			return p, nil
		}
	}
	return entity.Payment{}, apperror.NotFound("payment")
}

func (r *mockRepo) UpdateStatus(_ context.Context, _ pgx.Tx, paymentID string, status entity.PaymentStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.payments[paymentID]; ok {
		p.Status = status
		r.payments[paymentID] = p
	}
	return nil
}

func (r *mockRepo) UpdatePaymentStatusByBookingID(_ context.Context, _ pgx.Tx, bookingID string, status entity.PaymentStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range r.payments {
		if p.BookingID == bookingID {
			p.Status = status
			r.payments[id] = p
		}
	}
	return nil
}

func (r *mockRepo) GetBookingSnapshot(_ context.Context, bookingID string) (entity.BookingSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap, ok := r.bookings[bookingID]
	if !ok {
		return entity.BookingSnapshot{}, apperror.NotFound("booking")
	}
	return snap, nil
}

func (r *mockRepo) UpdateBookingStatus(_ context.Context, _ pgx.Tx, bookingID string, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := r.bookings[bookingID]
	snap.Status = status
	r.bookings[bookingID] = snap
	return nil
}

// UpdateStatusesForCallback — mock: just update in-memory
func (r *mockRepo) UpdateStatusesForCallback(_ context.Context, bookingID string, internalStatus string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := r.bookings[bookingID]
	switch internalStatus {
	case "SUCCESS":
		snap.Status = "PAID"
		for id, p := range r.payments {
			if p.BookingID == bookingID {
				p.Status = entity.PaymentStatusSuccess
				r.payments[id] = p
			}
		}
	case "FAILED", "EXPIRED":
		snap.Status = "EXPIRED"
		for id, p := range r.payments {
			if p.BookingID == bookingID {
				p.Status = entity.PaymentStatusFailed
				r.payments[id] = p
			}
		}
	}
	r.bookings[bookingID] = snap
	return nil
}

func (r *mockRepo) LogCallback(_ context.Context, key, _, _ string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.callbacks[key] {
		return true, nil // already processed
	}
	r.callbacks[key] = true
	return false, nil
}

// mockGateway
type mockGateway struct {
	shouldFail bool
	token      string
}

func (g *mockGateway) CreateSnapToken(_ context.Context, req interface{ GetOrderID() string }) (interface{}, error) {
	return nil, nil
}

// Implement the actual outbound.IPaymentGateway interface
type snapGateway struct {
	shouldFail bool
	snapToken  string
	redirectURL string
}

func (g *snapGateway) CreateSnapToken(_ context.Context, _ interface{}) (interface{}, error) { return nil, nil }

// We need to import outbound for IPaymentGateway — use an adapter struct
type mockPaymentGateway struct {
	shouldFail  bool
	snapToken   string
	redirectURL string
}

func (g *mockPaymentGateway) CreateSnapToken(ctx context.Context, req struct {
	OrderID     string
	GrossAmount int64
	Email       string
	Items       []interface{}
}) (struct {
	SnapToken   string
	RedirectURL string
}, error) {
	return struct {
		SnapToken   string
		RedirectURL string
	}{}, nil
}

// mockProducer
type mockProducer struct {
	mu        sync.Mutex
	published map[string]int
}

func newMockProducer() *mockProducer {
	return &mockProducer{published: make(map[string]int)}
}

func (p *mockProducer) count(topic string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.published[topic]
}

func (p *mockProducer) pub(topic string) {
	p.mu.Lock()
	p.published[topic]++
	p.mu.Unlock()
}

func (p *mockProducer) PublishPaymentSucceeded(_ context.Context, _ string, _ any) error {
	p.pub("payment.succeeded"); return nil
}
func (p *mockProducer) PublishPaymentExpired(_ context.Context, _ string, _ any) error {
	p.pub("payment.expired"); return nil
}
func (p *mockProducer) PublishSlotReleased(_ context.Context, _ string, _ any) error {
	p.pub("slot.released"); return nil
}

// ============================================================
// Test helpers
// ============================================================

func expiresIn(d time.Duration) *time.Time {
	t := time.Now().Add(d)
	return &t
}

func defaultSnap(status string, expiresAt *time.Time) entity.BookingSnapshot {
	return entity.BookingSnapshot{
		ID:               "bkg-001",
		EventID:          "evt-001",
		Email:            "user@test.com",
		Status:           status,
		PaymentExpiresAt: expiresAt,
		Items: []entity.BookingItemSnapshot{
			{CategoryID: "cat-vip", NIK: "1234567890123456", PriceSnapshot: 500000},
			{CategoryID: "cat-vip", NIK: "1234567890123457", PriceSnapshot: 500000},
		},
	}
}

// realGateway implements outbound.IPaymentGateway for tests
// We need to use the actual interface from outbound package
// Import it properly in the build tags below

// ============================================================
// Tests using concrete PaymentUsecase via constructor
// These tests call the usecase indirectly via its public interface.
// The pool = nil trick won't work — instead we use table-driven checks
// of individual usecase methods that don't touch pool.Begin.
// ============================================================

// TestGetBookingPayment_EnabledFlags verifies that method enabled flags are computed correctly.
func TestGetBookingPayment_EnabledFlags(t *testing.T) {
	snap := defaultSnap("HELD", expiresIn(90*time.Second)) // 90s remaining
	repo := newMockRepo(snap)
	prod := newMockProducer()

	uc := buildUC(repo, prod, "")

	resp, err := uc.GetBookingPayment(context.Background(), "bkg-001")
	require.NoError(t, err)
	assert.Equal(t, 2, len(snap.Items)*1) // 2 items → total 1_000_000
	assert.Equal(t, int64(1000000), resp.Total)

	// BCA VA: estSeconds=180 → 90 < 180 → disabled
	assert.False(t, findMethod(resp.Methods, "bca_va").Enabled, "bca_va harus disabled (90s < 180s)")
	// QRIS: estSeconds=60 → 90 >= 60 → enabled
	assert.True(t, findMethod(resp.Methods, "qris").Enabled, "qris harus enabled (90s >= 60s)")
	// GoPay: estSeconds=45 → 90 >= 45 → enabled
	assert.True(t, findMethod(resp.Methods, "gopay").Enabled, "gopay harus enabled (90s >= 45s)")
}

func TestGetBookingPayment_AllDisabled_WhenExpired(t *testing.T) {
	snap := defaultSnap("HELD", expiresIn(-1*time.Second)) // already expired
	repo := newMockRepo(snap)
	uc := buildUC(repo, newMockProducer(), "")

	resp, err := uc.GetBookingPayment(context.Background(), "bkg-001")
	require.NoError(t, err)
	for _, m := range resp.Methods {
		assert.False(t, m.Enabled, "semua metode harus disabled bila expired")
	}
	assert.True(t, resp.BackDisabled)
}

func TestGetBookingPayment_BookingNotFound(t *testing.T) {
	repo := newMockRepo(defaultSnap("HELD", expiresIn(5*time.Minute)))
	uc := buildUC(repo, newMockProducer(), "")

	_, err := uc.GetBookingPayment(context.Background(), "nonexistent")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.True(t, errors.As(err, &appErr))
	assert.Equal(t, apperror.ErrNotFound, appErr.Code)
}

func TestHandleCallback_SignatureInvalid(t *testing.T) {
	snap := defaultSnap("HELD", expiresIn(5*time.Minute))
	repo := newMockRepo(snap)
	uc := buildUC(repo, newMockProducer(), "real-server-key")

	err := uc.HandleCallback(context.Background(), dto.MidtransCallback{
		OrderID:           "bkg-001",
		TransactionStatus: "settlement",
		StatusCode:        "200",
		GrossAmount:       "1000000.00",
		SignatureKey:      "invalid-signature",
	})
	require.Error(t, err)
	var appErr *apperror.AppError
	require.True(t, errors.As(err, &appErr))
	assert.Equal(t, apperror.ErrUnauthorized, appErr.Code)
}

func TestHandleCallback_Idempoten(t *testing.T) {
	snap := defaultSnap("HELD", expiresIn(5*time.Minute))
	repo := newMockRepo(snap)
	prod := newMockProducer()
	uc := buildUC(repo, prod, "") // kosong = skip signature check

	cb := dto.MidtransCallback{
		OrderID:           "bkg-001",
		TransactionStatus: "settlement",
		StatusCode:        "200",
		GrossAmount:       "1000000.00",
	}

	// Kirim callback pertama
	require.NoError(t, uc.HandleCallback(context.Background(), cb))

	// Tunggu publish goroutine
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, prod.count("payment.succeeded"))

	// Kirim callback kedua (duplikat)
	require.NoError(t, uc.HandleCallback(context.Background(), cb))
	time.Sleep(50 * time.Millisecond)

	// Publish TIDAK boleh bertambah
	assert.Equal(t, 1, prod.count("payment.succeeded"), "idempoten: payment.succeeded tidak boleh dipublish 2x")
	assert.Equal(t, 1, prod.count("slot.released"), "idempoten: slot.released tidak boleh dipublish 2x")
}

func TestHandleCallback_Success_UpdatesStatus(t *testing.T) {
	snap := defaultSnap("HELD", expiresIn(5*time.Minute))
	repo := newMockRepo(snap)
	prod := newMockProducer()
	uc := buildUC(repo, prod, "")

	require.NoError(t, uc.HandleCallback(context.Background(), dto.MidtransCallback{
		OrderID:           "bkg-001",
		TransactionStatus: "settlement",
		StatusCode:        "200",
		GrossAmount:       "1000000.00",
	}))

	// Booking harus PAID
	updatedSnap, _ := repo.GetBookingSnapshot(context.Background(), "bkg-001")
	assert.Equal(t, "PAID", updatedSnap.Status)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, prod.count("payment.succeeded"))
	assert.Equal(t, 1, prod.count("slot.released"))
}

func TestHandleCallback_Expired_UpdatesStatus(t *testing.T) {
	snap := defaultSnap("HELD", expiresIn(5*time.Minute))
	repo := newMockRepo(snap)
	prod := newMockProducer()
	uc := buildUC(repo, prod, "")

	require.NoError(t, uc.HandleCallback(context.Background(), dto.MidtransCallback{
		OrderID:           "bkg-001",
		TransactionStatus: "expire",
		StatusCode:        "407",
		GrossAmount:       "1000000.00",
	}))

	updatedSnap, _ := repo.GetBookingSnapshot(context.Background(), "bkg-001")
	assert.Equal(t, "EXPIRED", updatedSnap.Status)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, prod.count("payment.expired"))
	assert.Equal(t, 1, prod.count("slot.released"))
}

func TestHandleCallback_BookingNotFound(t *testing.T) {
	snap := defaultSnap("HELD", expiresIn(5*time.Minute))
	repo := newMockRepo(snap)
	uc := buildUC(repo, newMockProducer(), "")

	err := uc.HandleCallback(context.Background(), dto.MidtransCallback{
		OrderID:           "NONEXISTENT",
		TransactionStatus: "settlement",
		StatusCode:        "200",
		GrossAmount:       "0.00",
	})
	require.Error(t, err)
	var appErr *apperror.AppError
	require.True(t, errors.As(err, &appErr))
	assert.Equal(t, apperror.ErrNotFound, appErr.Code)
}

// TestIdempoten_Concurrent — N goroutine kirim callback bersamaan.
// Hanya 1 yang diproses, sisanya idempoten.
func TestIdempoten_Concurrent(t *testing.T) {
	snap := defaultSnap("HELD", expiresIn(5*time.Minute))
	repo := newMockRepo(snap)
	prod := newMockProducer()
	uc := buildUC(repo, prod, "")

	cb := dto.MidtransCallback{
		OrderID:           "bkg-001",
		TransactionStatus: "settlement",
		StatusCode:        "200",
		GrossAmount:       "1000000.00",
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			uc.HandleCallback(context.Background(), cb) //nolint:errcheck
		}()
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	// payment.succeeded harus TEPAT 1 kali
	assert.Equal(t, 1, prod.count("payment.succeeded"), "concurrent callback → tepat 1 publish")
	assert.Equal(t, 1, prod.count("slot.released"), "concurrent callback → tepat 1 slot.released")
}

// ============================================================
// Helpers
// ============================================================

func findMethod(methods []dto.PaymentMethod, code string) dto.PaymentMethod {
	for _, m := range methods {
		if m.Code == code {
			return m
		}
	}
	return dto.PaymentMethod{}
}

// buildUC membuat PaymentUsecase dengan mock repo/producer, tanpa pool (Charge tidak ditest di sini).
// HandleCallback dan GetBookingPayment tidak pakai pool langsung → dapat nil pool.
func buildUC(repo *mockRepo, prod *mockProducer, midtransKey string) *usecase.PaymentUsecase {
	return usecase.NewPaymentUsecase(
		nil, // pool nil — HandleCallback/GetBookingPayment tidak pakai pool di test
		repo, &noopGateway{}, prod, midtransKey, slog.Default(),
	)
}

// noopGateway tidak pernah dipanggil di test yang tidak menyentuh Charge path.
type noopGateway struct{}

func (g *noopGateway) CreateSnapToken(_ context.Context, _ outbound.SnapRequest) (outbound.SnapResult, error) {
	return outbound.SnapResult{SnapToken: "tok-test", RedirectURL: "https://example.com"}, nil
}

// Compile-time interface check
var _ outbound.IPaymentGateway = (*noopGateway)(nil)
var _ outbound.IPaymentRepository = (*mockRepo)(nil)
var _ outbound.IEventProducer = (*mockProducer)(nil)

// suppress unused import warning
var _ = fmt.Sprintf
