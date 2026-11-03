package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type contextKey string

const (
	ctxUserID      contextKey = "userId"
	ctxUserRole    contextKey = "userRole"
	ctxPaymentToken contextKey = "paymentTokenClaims"
)

// PaymentTokenClaims adalah claims dari Payment Token yang diterbitkan booking-service.
type PaymentTokenClaims struct {
	BookingID string `json:"bookingId"`
	EventID   string `json:"eventId"`
	jwt.RegisteredClaims
}

// RequirePaymentToken memvalidasi X-Payment-Token header (JWT HS256 dari booking-service).
// Jika tidak ada, expired, atau signature tidak valid → 401.
func RequirePaymentToken(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := r.Header.Get("X-Payment-Token")
			if tokenStr == "" {
				http.Error(w, `{"status":"401","message":"Payment Token tidak ada","data":{}}`, http.StatusUnauthorized)
				return
			}
			claims, err := parsePaymentToken(tokenStr, secret)
			if err != nil {
				http.Error(w, `{"status":"401","message":"Payment Token tidak valid atau sudah expired","data":{}}`, http.StatusUnauthorized)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), ctxPaymentToken, claims))
			next.ServeHTTP(w, r)
		})
	}
}

// GetPaymentTokenClaims mengambil PaymentTokenClaims dari context.
func GetPaymentTokenClaims(ctx context.Context) (*PaymentTokenClaims, bool) {
	v, ok := ctx.Value(ctxPaymentToken).(*PaymentTokenClaims)
	return v, ok
}

func OptionalAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token := extractBearer(r); token != "" {
				if userID, role, ok := parseUserJWT(token, secret); ok {
					r = r.WithContext(context.WithValue(r.Context(), ctxUserID, userID))
					r = r.WithContext(context.WithValue(r.Context(), ctxUserRole, role))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func RequireAccessToken(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Access-Token") == "" {
				http.Error(w, `{"status":"401","message":"Access Token tidak ada","data":{}}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Cek apakah user sudah di-inject oleh OptionalAuth
			if userRole, _ := r.Context().Value(ctxUserRole).(string); userRole != role {
				http.Error(w, `{"status":"403","message":"Akses ditolak","data":{}}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func TransactionID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		txID := r.Header.Get("X-Transaction-Id")
		if txID == "" { txID = "tx-placeholder" }
		w.Header().Set("X-Transaction-Id", txID)
		next.ServeHTTP(w, r)
	})
}

func Chain(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- { h = middlewares[i](h) }
	return h
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") { return strings.TrimPrefix(auth, "Bearer ") }
	return ""
}

func parseUserJWT(tokenStr, secret string) (string, string, bool) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok { return nil, jwt.ErrSignatureInvalid }
		return []byte(secret), nil
	})
	if err != nil || !token.Valid { return "", "", false }
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok { return "", "", false }
	sub, _ := claims["sub"].(string)
	role, _ := claims["role"].(string)
	return sub, role, sub != ""
}

func parsePaymentToken(tokenStr, secret string) (*PaymentTokenClaims, error) {
	claims := &PaymentTokenClaims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}
