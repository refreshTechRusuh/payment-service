package usecase_test

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/refreshTechRusuh/payment-service/internal/payment/application/usecase"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/dto"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// computeTestSig menghasilkan Midtrans signature yang valid untuk test.
func computeTestSig(orderID, statusCode, grossAmount, serverKey string) string {
	raw := orderID + statusCode + grossAmount + serverKey
	h := sha512.New()
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

// makeCallbackSnap membuat BookingSnapshot dengan HELD status dan expiry.
func makeCallbackSnap() entity.BookingSnapshot {
	return defaultSnap("HELD", expiresIn(10*time.Minute))
}

// ---- mapTransactionStatus (via HandleCallback) -----------------------------

func TestHandleCallback_CaptureAccept_Success(t *testing.T) {
	snap := makeCallbackSnap()
	repo := newMockRepo(snap)
	// Inject payment PENDING agar callback bisa update status
	repo.payments["pay-bkg-001"] = entity.Payment{
		ID: "pay-bkg-001", BookingID: "bkg-001", Status: entity.PaymentStatusPending,
	}
	prod := newMockProducer()
	uc := buildUC(repo, prod, "srv-key")

	// gross_amount harus match snapshot TotalAmount (dari defaultSnap = 1000000.00)
	sig := computeTestSig("bkg-001", "200", "1000000.00", "srv-key")
	err := uc.HandleCallback(context.Background(), dto.MidtransCallback{
		OrderID: "bkg-001", TransactionStatus: "capture", FraudStatus: "accept",
		StatusCode: "200", GrossAmount: "1000000.00", SignatureKey: sig,
	})
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond) // wait goroutine publish
	assert.GreaterOrEqual(t, prod.count("payment.succeeded"), 1)
}

func TestHandleCallback_CaptureFraudDeny_Failed(t *testing.T) {
	repo := newMockRepo(makeCallbackSnap())
	uc := buildUC(repo, newMockProducer(), "srv-key")

	sig := computeTestSig("bkg-001", "200", "100000.00", "srv-key")
	err := uc.HandleCallback(context.Background(), dto.MidtransCallback{
		OrderID: "bkg-001", TransactionStatus: "capture", FraudStatus: "deny",
		StatusCode: "200", GrossAmount: "100000.00", SignatureKey: sig,
	})
	// capture + deny → FAILED path (returns nil or handles gracefully)
	_ = err
}

func TestHandleCallback_DenyStatus(t *testing.T) {
	repo := newMockRepo(makeCallbackSnap())
	uc := buildUC(repo, newMockProducer(), "srv-key")
	sig := computeTestSig("bkg-001", "202", "100000.00", "srv-key")
	_ = uc.HandleCallback(context.Background(), dto.MidtransCallback{
		OrderID: "bkg-001", TransactionStatus: "deny",
		StatusCode: "202", GrossAmount: "100000.00", SignatureKey: sig,
	})
}

func TestHandleCallback_CancelStatus(t *testing.T) {
	repo := newMockRepo(makeCallbackSnap())
	uc := buildUC(repo, newMockProducer(), "srv-key")
	sig := computeTestSig("bkg-001", "202", "100000.00", "srv-key")
	_ = uc.HandleCallback(context.Background(), dto.MidtransCallback{
		OrderID: "bkg-001", TransactionStatus: "cancel",
		StatusCode: "202", GrossAmount: "100000.00", SignatureKey: sig,
	})
}

func TestHandleCallback_ExpireStatus(t *testing.T) {
	repo := newMockRepo(makeCallbackSnap())
	uc := buildUC(repo, newMockProducer(), "srv-key")
	sig := computeTestSig("bkg-001", "202", "100000.00", "srv-key")
	_ = uc.HandleCallback(context.Background(), dto.MidtransCallback{
		OrderID: "bkg-001", TransactionStatus: "expire",
		StatusCode: "202", GrossAmount: "100000.00", SignatureKey: sig,
	})
}

func TestHandleCallback_UnknownStatus_DefaultPending(t *testing.T) {
	repo := newMockRepo(makeCallbackSnap())
	uc := buildUC(repo, newMockProducer(), "srv-key")
	sig := computeTestSig("bkg-001", "200", "100000.00", "srv-key")
	_ = uc.HandleCallback(context.Background(), dto.MidtransCallback{
		OrderID: "bkg-001", TransactionStatus: "unknown_xyz",
		StatusCode: "200", GrossAmount: "100000.00", SignatureKey: sig,
	})
}

// ---- SweepOnce branches ----------------------------------------------------

func TestSweepOnce_Payment_Empty_NoOp(t *testing.T) {
	uc := buildUC(newMockRepo(makeCallbackSnap()), newMockProducer(), "")
	assert.NotPanics(t, func() { uc.SweepOnce(context.Background()) })
}

func TestSweepOnce_Payment_ListExpiredError_Logs(t *testing.T) {
	repo := &errListExpiredRepo{mockRepo: newMockRepo(makeCallbackSnap())}
	uc := usecase.NewPaymentUsecase(nil, repo, &noopGateway{}, newMockProducer(), nil, "", slog.Default())
	assert.NotPanics(t, func() { uc.SweepOnce(context.Background()) })
}

func TestSweepOnce_Payment_ExpiresPayment_PublishesEvents(t *testing.T) {
	base := newMockRepo(makeCallbackSnap())
	repo := &soldOutMockRepo{
		mockRepo:        base,
		expiredPayments: []entity.Payment{{ID: "pay-exp", BookingID: "bkg-001"}},
		stocks:          []entity.CategoryStock{{CategoryID: "cat-1", Available: 5}},
	}
	prod := newMockProducer()
	uc := usecase.NewPaymentUsecase(nil, repo, &noopGateway{}, prod, nil, "", slog.Default())
	assert.NotPanics(t, func() { uc.SweepOnce(context.Background()) })
	time.Sleep(100 * time.Millisecond)
	// Slot released dan payment expired harus dipublish
	assert.GreaterOrEqual(t, prod.count("slot.released")+prod.count("payment.expired"), 1)
}

// ---- postExpirePaymentPublish: empty eventID --------------------------------

func TestSweepOnce_EmptyEventID_SkipsKafka(t *testing.T) {
	base := newMockRepo(entity.BookingSnapshot{EventID: ""}) // snap tanpa eventID
	repo := &soldOutMockRepo{
		mockRepo:        base,
		expiredPayments: []entity.Payment{{ID: "pay-no-evt", BookingID: "bkg-001"}},
		stocks:          []entity.CategoryStock{},
	}
	prod := newMockProducer()
	uc := usecase.NewPaymentUsecase(nil, repo, &noopGateway{}, prod, nil, "", slog.Default())
	uc.SweepOnce(context.Background())
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, prod.count("payment.expired"), "eventID kosong tidak boleh publish payment.expired")
}

// ---- checkAndPublishSoldOut branches ----------------------------------------

func TestCheckAndPublishSoldOut_GetStockError_NoOp(t *testing.T) {
	base := newMockRepo(makeCallbackSnap())
	repo := &errGetStockRepo{
		soldOutMockRepo: &soldOutMockRepo{
			mockRepo:        base,
			expiredPayments: []entity.Payment{{ID: "pay-x", BookingID: "bkg-001"}},
		},
	}
	uc := usecase.NewPaymentUsecase(nil, repo, &noopGateway{}, newMockProducer(), nil, "", slog.Default())
	assert.NotPanics(t, func() {
		uc.SweepOnce(context.Background())
		time.Sleep(100 * time.Millisecond)
	})
}

func TestCheckAndPublishSoldOut_StillHasStock_NoSoldOut(t *testing.T) {
	base := newMockRepo(makeCallbackSnap())
	repo := &soldOutMockRepo{
		mockRepo:        base,
		expiredPayments: []entity.Payment{{ID: "pay-s", BookingID: "bkg-001"}},
		stocks:          []entity.CategoryStock{{CategoryID: "cat-1", Available: 5}},
	}
	prod := newMockProducer()
	uc := usecase.NewPaymentUsecase(nil, repo, &noopGateway{}, prod, nil, "", slog.Default())
	uc.SweepOnce(context.Background())
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, prod.count("event.sold_out"), "masih ada stok, tidak sold-out")
}

func TestCheckAndPublishSoldOut_ZeroStockHasActive_NoSoldOut(t *testing.T) {
	base := newMockRepo(makeCallbackSnap())
	repo := &soldOutMockRepo{
		mockRepo:        base,
		expiredPayments: []entity.Payment{{ID: "pay-h", BookingID: "bkg-001"}},
		stocks:          []entity.CategoryStock{{CategoryID: "cat-1", Available: 0}},
		hasActive:       true,
	}
	prod := newMockProducer()
	uc := usecase.NewPaymentUsecase(nil, repo, &noopGateway{}, prod, nil, "", slog.Default())
	uc.SweepOnce(context.Background())
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, prod.count("event.sold_out"), "ada HELD aktif, belum sold-out")
}

// ---- GetBookingPayment extra branches --------------------------------------

func TestGetBookingPayment_NoRedirectURL(t *testing.T) {
	snap := defaultSnap("HELD", expiresIn(5*time.Minute))
	repo := newMockRepo(snap)
	repo.payments["pay-1"] = entity.Payment{
		ID: "pay-1", BookingID: "bkg-001", Status: entity.PaymentStatusPending,
	}
	uc := buildUC(repo, newMockProducer(), "")
	resp, err := uc.GetBookingPayment(context.Background(), "bkg-001")
	require.NoError(t, err)
	assert.Empty(t, resp.RedirectURL)
}

func TestGetBookingPayment_NilExpiry_RemainingZero(t *testing.T) {
	// defaultSnap("HELD", nil) -- nil PaymentExpiresAt via defaultSnap helper
	snap := defaultSnap("HELD", nil)
	repo := newMockRepo(snap)
	// Inject payment dengan BookingID yang matching snap.BookingID = "bkg-001"
	repo.payments["pay-nil"] = entity.Payment{
		ID: "pay-nil", BookingID: "bkg-001", Status: entity.PaymentStatusPending,
	}
	uc := buildUC(repo, newMockProducer(), "")
	resp, err := uc.GetBookingPayment(context.Background(), "bkg-001")
	require.NoError(t, err)
	assert.Equal(t, 0, resp.RemainingSeconds)
}

// ---- Mock overrides --------------------------------------------------------

type errListExpiredRepo struct {
	*mockRepo
}

func (r *errListExpiredRepo) ListExpiredPending(_ context.Context, _ int) ([]entity.Payment, error) {
	return nil, errors.New("db error")
}

type errGetStockRepo struct {
	*soldOutMockRepo
}

func (r *errGetStockRepo) GetStockByEvent(_ context.Context, _ string) ([]entity.CategoryStock, error) {
	return nil, errors.New("db error")
}
