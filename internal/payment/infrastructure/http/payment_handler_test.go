package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/refreshTechRusuh/payment-service/internal/config"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/dto"
	payhttp "github.com/refreshTechRusuh/payment-service/internal/payment/infrastructure/http"
	"github.com/refreshTechRusuh/payment-service/pkg/apperror"
	"github.com/refreshTechRusuh/payment-service/pkg/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-payment-secret-do-not-use-in-prod"

// ---- Mock usecase ----------------------------------------------------------

type mockUsecase struct {
	getBookingPaymentFn func(ctx context.Context, bookingID string) (dto.BookingPaymentDetailResponse, error)
	chargeFn            func(ctx context.Context, req dto.ChargeRequest) (dto.ChargeResponse, error)
	callbackFn          func(ctx context.Context, cb dto.MidtransCallback) error
}

func (m *mockUsecase) GetBookingPayment(ctx context.Context, bookingID string) (dto.BookingPaymentDetailResponse, error) {
	if m.getBookingPaymentFn != nil {
		return m.getBookingPaymentFn(ctx, bookingID)
	}
	return dto.BookingPaymentDetailResponse{BookingID: bookingID}, nil
}

func (m *mockUsecase) Charge(ctx context.Context, req dto.ChargeRequest) (dto.ChargeResponse, error) {
	if m.chargeFn != nil {
		return m.chargeFn(ctx, req)
	}
	return dto.ChargeResponse{PaymentID: "pay-001", Status: "PENDING", SnapToken: "snap-tok"}, nil
}

func (m *mockUsecase) HandleCallback(ctx context.Context, cb dto.MidtransCallback) error {
	if m.callbackFn != nil {
		return m.callbackFn(ctx, cb)
	}
	return nil
}

// ---- Helpers ----------------------------------------------------------------

// makePaymentToken membuat JWT Payment Token untuk test.
func makePaymentToken(t *testing.T, bookingID, eventID string, exp time.Time) string {
	t.Helper()
	claims := middleware.PaymentTokenClaims{
		BookingID: bookingID,
		EventID:   eventID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(testSecret))
	require.NoError(t, err)
	return signed
}

// setupRouter membangun ServeMux dengan mock usecase dan routes terdaftar.
func setupRouter(uc *mockUsecase) *http.ServeMux {
	mux := http.NewServeMux()
	h := payhttp.NewHandlerWithUsecase(uc)
	payhttp.RegisterRoutesWithSecret(mux, h, testSecret)
	return mux
}

// ---- GET /api/payment/v1/booking/{bookingId} --------------------------------

func TestGetBookingPayment_Success(t *testing.T) {
	uc := &mockUsecase{}
	mux := setupRouter(uc)

	token := makePaymentToken(t, "bkg-001", "evt-001", time.Now().Add(10*time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/api/payment/v1/booking/bkg-001", nil)
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "00000", resp["status"])
}

func TestGetBookingPayment_NoToken(t *testing.T) {
	mux := setupRouter(&mockUsecase{})
	req := httptest.NewRequest(http.MethodGet, "/api/payment/v1/booking/bkg-001", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestGetBookingPayment_WrongBookingID(t *testing.T) {
	// Token untuk bkg-001, tapi request ke bkg-999 → 403
	mux := setupRouter(&mockUsecase{})
	token := makePaymentToken(t, "bkg-001", "evt-001", time.Now().Add(10*time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/api/payment/v1/booking/bkg-999", nil)
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestGetBookingPayment_UsecaseError(t *testing.T) {
	uc := &mockUsecase{
		getBookingPaymentFn: func(_ context.Context, _ string) (dto.BookingPaymentDetailResponse, error) {
			return dto.BookingPaymentDetailResponse{}, apperror.New(apperror.ErrNotFound, "booking tidak ditemukan")
		},
	}
	mux := setupRouter(uc)
	token := makePaymentToken(t, "bkg-404", "evt-001", time.Now().Add(10*time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/api/payment/v1/booking/bkg-404", nil)
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestGetBookingPayment_ExpiredToken(t *testing.T) {
	mux := setupRouter(&mockUsecase{})
	// Token sudah expired
	token := makePaymentToken(t, "bkg-001", "evt-001", time.Now().Add(-1*time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/api/payment/v1/booking/bkg-001", nil)
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ---- POST /api/payment/v1/charge -------------------------------------------

func TestCharge_Success(t *testing.T) {
	uc := &mockUsecase{}
	mux := setupRouter(uc)

	token := makePaymentToken(t, "bkg-001", "evt-001", time.Now().Add(10*time.Minute))
	body, _ := json.Marshal(dto.ChargeRequest{BookingID: "bkg-001"})
	req := httptest.NewRequest(http.MethodPost, "/api/payment/v1/charge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "00000", resp["status"])
}

func TestCharge_NoToken(t *testing.T) {
	mux := setupRouter(&mockUsecase{})
	body, _ := json.Marshal(dto.ChargeRequest{BookingID: "bkg-001"})
	req := httptest.NewRequest(http.MethodPost, "/api/payment/v1/charge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCharge_InvalidJSON(t *testing.T) {
	mux := setupRouter(&mockUsecase{})
	token := makePaymentToken(t, "bkg-001", "evt-001", time.Now().Add(10*time.Minute))
	req := httptest.NewRequest(http.MethodPost, "/api/payment/v1/charge",
		bytes.NewReader([]byte(`{invalid json`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestCharge_MissingBookingID(t *testing.T) {
	// BookingID kosong → validation error
	mux := setupRouter(&mockUsecase{})
	token := makePaymentToken(t, "bkg-001", "evt-001", time.Now().Add(10*time.Minute))
	body, _ := json.Marshal(map[string]string{}) // tidak ada bookingId
	req := httptest.NewRequest(http.MethodPost, "/api/payment/v1/charge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code) // invalid "validation: ..." goes 500 via apperror
}

func TestCharge_WrongBookingID_403(t *testing.T) {
	// Token untuk bkg-001, body punya bkg-999 → 403
	mux := setupRouter(&mockUsecase{})
	token := makePaymentToken(t, "bkg-001", "evt-001", time.Now().Add(10*time.Minute))
	body, _ := json.Marshal(dto.ChargeRequest{BookingID: "bkg-999"})
	req := httptest.NewRequest(http.MethodPost, "/api/payment/v1/charge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestCharge_UsecaseError(t *testing.T) {
	uc := &mockUsecase{
		chargeFn: func(_ context.Context, _ dto.ChargeRequest) (dto.ChargeResponse, error) {
			return dto.ChargeResponse{}, apperror.New(apperror.ErrBookingExpired, "booking expired")
		},
	}
	mux := setupRouter(uc)
	token := makePaymentToken(t, "bkg-expired", "evt-001", time.Now().Add(10*time.Minute))
	body, _ := json.Marshal(dto.ChargeRequest{BookingID: "bkg-expired"})
	req := httptest.NewRequest(http.MethodPost, "/api/payment/v1/charge", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusGone, rr.Code)
}

// ---- POST /api/payment/v1/callback -----------------------------------------

func TestCallback_Success(t *testing.T) {
	mux := setupRouter(&mockUsecase{})
	cb := dto.MidtransCallback{
		OrderID: "bkg-001", TransactionStatus: "settlement",
		StatusCode: "200", GrossAmount: "100000",
	}
	body, _ := json.Marshal(cb)
	req := httptest.NewRequest(http.MethodPost, "/api/payment/v1/callback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestCallback_InvalidJSON(t *testing.T) {
	mux := setupRouter(&mockUsecase{})
	req := httptest.NewRequest(http.MethodPost, "/api/payment/v1/callback",
		bytes.NewReader([]byte(`{bad json`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestCallback_UsecaseError(t *testing.T) {
	uc := &mockUsecase{
		callbackFn: func(_ context.Context, _ dto.MidtransCallback) error {
			return errors.New("midtrans signature invalid")
		},
	}
	mux := setupRouter(uc)
	body, _ := json.Marshal(dto.MidtransCallback{OrderID: "bkg-001"})
	req := httptest.NewRequest(http.MethodPost, "/api/payment/v1/callback", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// ---- GET /sys/ping ---------------------------------------------------------

func TestPing(t *testing.T) {
	mux := setupRouter(&mockUsecase{})
	req := httptest.NewRequest(http.MethodGet, "/sys/ping", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "pong")
}

// ── NewWiredHandler + RegisterRoutes — nil/minimal deps ─────────────────────

func TestNewWiredHandler_nilDeps(t *testing.T) {
	// Konstruktor tidak connect saat dibuat.
	// cfg.Midtrans.ServerKey bisa kosong (tidak connect ke Midtrans saat konstruksi).
	cfg := &config.Config{}
	cfg.JWT.PaymentTokenSecret = "test"
	h := payhttp.NewWiredHandler(nil, cfg, nil, nil)
	assert.NotNil(t, h)
}

func TestRegisterRoutes_registersEndpoints(t *testing.T) {
	cfg := &config.Config{}
	cfg.JWT.PaymentTokenSecret = testSecret

	mux := http.NewServeMux()
	h := payhttp.NewHandlerWithUsecase(&mockUsecase{})
	payhttp.RegisterRoutes(mux, h, cfg)

	// Ping endpoint harus terdaftar
	req := httptest.NewRequest(http.MethodGet, "/sys/ping", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}
