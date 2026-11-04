package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/refreshTechRusuh/payment-service/pkg/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAccessSecret  = "test-access-secret-payment"
	testPaymentSecret = "test-payment-secret-payment"
	testUserSecret    = "test-user-secret-payment"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func makePaymentJWT(t *testing.T, secret, bookingID, eventID string, expired bool) string {
	t.Helper()
	exp := time.Now().Add(10 * time.Minute)
	if expired {
		exp = time.Now().Add(-1 * time.Minute)
	}
	claims := jwt.MapClaims{
		"bookingId": bookingID,
		"eventId":   eventID,
		"exp":       exp.Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(secret))
	require.NoError(t, err)
	return signed
}

func makeAdminJWT(t *testing.T, secret, role string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":  "user-1",
		"role": role,
		"exp":  time.Now().Add(1 * time.Hour).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(secret))
	require.NoError(t, err)
	return signed
}

// ---- RequireAccessToken (simple header check) --------------------------------

func TestRequireAccessToken_NoHeader_401(t *testing.T) {
	h := middleware.RequireAccessToken(testAccessSecret)(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRequireAccessToken_AnyToken_Pass(t *testing.T) {
	// Payment-service RequireAccessToken hanya cek keberadaan header -- tidak parse JWT
	h := middleware.RequireAccessToken(testAccessSecret)(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Access-Token", "any-token-value")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ---- RequirePaymentToken ----------------------------------------------------

func TestRequirePaymentToken_NoHeader_401(t *testing.T) {
	h := middleware.RequirePaymentToken(testPaymentSecret)(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRequirePaymentToken_ValidToken_Pass(t *testing.T) {
	token := makePaymentJWT(t, testPaymentSecret, "bkg-001", "evt-001", false)
	h := middleware.RequirePaymentToken(testPaymentSecret)(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRequirePaymentToken_ExpiredToken_401(t *testing.T) {
	token := makePaymentJWT(t, testPaymentSecret, "bkg-001", "evt-001", true)
	h := middleware.RequirePaymentToken(testPaymentSecret)(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRequirePaymentToken_WrongSecret_401(t *testing.T) {
	token := makePaymentJWT(t, "wrong-secret", "bkg-001", "evt-001", false)
	h := middleware.RequirePaymentToken(testPaymentSecret)(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestRequirePaymentToken_ClaimsInjected(t *testing.T) {
	token := makePaymentJWT(t, testPaymentSecret, "bkg-001", "evt-001", false)
	var gotClaims *middleware.PaymentTokenClaims
	checkHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims, _ = middleware.GetPaymentTokenClaims(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := middleware.RequirePaymentToken(testPaymentSecret)(checkHandler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Payment-Token", token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, gotClaims)
	assert.Equal(t, "bkg-001", gotClaims.BookingID)
}

// ---- OptionalAuth -----------------------------------------------------------

func TestOptionalAuth_NoToken_PassThrough(t *testing.T) {
	h := middleware.OptionalAuth(testUserSecret)(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestOptionalAuth_InvalidToken_PassThrough(t *testing.T) {
	h := middleware.OptionalAuth(testUserSecret)(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid.token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ---- RequireRole ------------------------------------------------------------

func TestRequireRole_NoRole_403(t *testing.T) {
	h := middleware.RequireRole("admin")(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestRequireRole_CorrectRole_Pass(t *testing.T) {
	token := makeAdminJWT(t, testUserSecret, "admin")
	h := middleware.Chain(
		okHandler,
		middleware.OptionalAuth(testUserSecret),
		middleware.RequireRole("admin"),
	)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ---- Chain ------------------------------------------------------------------

func TestChain_Order(t *testing.T) {
	order := []string{}
	m1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "m1")
			next.ServeHTTP(w, r)
		})
	}
	m2 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "m2")
			next.ServeHTTP(w, r)
		})
	}
	h := middleware.Chain(okHandler, m1, m2)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, []string{"m1", "m2"}, order)
}
