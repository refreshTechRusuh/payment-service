package http

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/refreshTechRusuh/payment-service/internal/config"
	"github.com/refreshTechRusuh/payment-service/internal/payment/application/usecase"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/dto"
	"github.com/refreshTechRusuh/payment-service/internal/payment/infrastructure/db"
	"github.com/refreshTechRusuh/payment-service/internal/payment/infrastructure/event"
	"github.com/refreshTechRusuh/payment-service/internal/payment/infrastructure/gateway"
	"github.com/refreshTechRusuh/payment-service/internal/payment/port/inbound"
	"github.com/refreshTechRusuh/payment-service/pkg/apinizer"
	"github.com/refreshTechRusuh/payment-service/pkg/kafka"
	"github.com/refreshTechRusuh/payment-service/pkg/middleware"
	vk "github.com/valkey-io/valkey-go"
)

var validate = validator.New()

type Handler struct{ usecase inbound.IPaymentUsecase }

// NewHandlerWithUsecase membuat Handler dengan usecase yang sudah di-inject.
// Dipakai di unit test untuk inject mock usecase tanpa butuh DB/Kafka/Midtrans.
func NewHandlerWithUsecase(uc inbound.IPaymentUsecase) *Handler {
	return &Handler{usecase: uc}
}

// RegisterRoutesWithSecret mendaftarkan routes dengan payment token secret eksplisit.
// Dipakai di unit test agar secret bisa dikontrol tanpa config lengkap.
func RegisterRoutesWithSecret(mux *http.ServeMux, h *Handler, paymentTokenSecret string) {
	withPaymentToken := func(next http.HandlerFunc) http.Handler {
		return middleware.Chain(http.HandlerFunc(next), middleware.RequirePaymentToken(paymentTokenSecret))
	}

	mux.Handle("GET /api/payment/v1/booking/{bookingId}", withPaymentToken(h.GetBookingPayment))
	mux.Handle("POST /api/payment/v1/charge", withPaymentToken(h.Charge))
	mux.HandleFunc("POST /api/payment/v1/callback", h.Callback)

	mux.HandleFunc("GET /sys/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"00000","message":"pong","data":{}}`)) // #nosec G104 -- health endpoint
	})
}

func NewWiredHandler(pool *pgxpool.Pool, cfg *config.Config, producer *kafka.Producer, valkeyClient vk.Client) *Handler {
	h, _ := NewWiredHandlerWithUC(pool, cfg, producer, valkeyClient)
	return h
}

// NewWiredHandlerWithUC sama dengan NewWiredHandler tapi juga mengembalikan PaymentUsecase
// agar delivery layer bisa start sweeper.
func NewWiredHandlerWithUC(pool *pgxpool.Pool, cfg *config.Config, producer *kafka.Producer, valkeyClient vk.Client) (*Handler, *usecase.PaymentUsecase) {
	log := slog.Default()
	repo := db.NewPaymentRepository(pool, log)
	gw := gateway.NewMidtransGateway(cfg.Midtrans.ServerKey, cfg.Midtrans.Env, log)
	ep := event.NewKafkaProducer(producer, log)
	var redirectStore *db.RedirectStore
	if valkeyClient != nil {
		redirectStore = db.NewRedirectStore(valkeyClient)
	}
	uc := usecase.NewPaymentUsecase(pool, repo, gw, ep, redirectStore, cfg.Midtrans.ServerKey, cfg.Midtrans.SignatureKey, log)
	return &Handler{usecase: uc}, uc
}
func RegisterRoutes(mux *http.ServeMux, h *Handler, cfg *config.Config) {
	withPaymentToken := func(next http.HandlerFunc) http.Handler {
		return middleware.Chain(http.HandlerFunc(next), middleware.RequirePaymentToken(cfg.JWT.PaymentTokenSecret))
	}

	mux.Handle("GET /api/payment/v1/booking/{bookingId}", withPaymentToken(h.GetBookingPayment))
	mux.Handle("POST /api/payment/v1/charge", withPaymentToken(h.Charge))
	// Callback: no auth middleware (dipanggil Midtrans, diverifikasi via signature_key)
	mux.HandleFunc("POST /api/payment/v1/callback", h.Callback)

	mux.HandleFunc("GET /sys/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"00000","message":"pong","data":{}}`)) // #nosec G104 -- health endpoint
	})
}

func (h *Handler) GetBookingPayment(w http.ResponseWriter, r *http.Request) {
	bookingID := r.PathValue("bookingId")
	// Wajib cek: claims.bookingId == bookingId di path agar token booking A tidak bisa dipakai untuk booking B.
	if claims, ok := middleware.GetPaymentTokenClaims(r.Context()); ok {
		if claims.BookingID != bookingID {
			http.Error(w, `{"status":"403","message":"Payment Token tidak cocok dengan booking ini","data":{}}`, http.StatusForbidden)
			return
		}
	}
	result, err := h.usecase.GetBookingPayment(r.Context(), bookingID)
	if err != nil { apinizer.WriteError(w, err); return }
	apinizer.Success(w, "OK", result)
}

func (h *Handler) Charge(w http.ResponseWriter, r *http.Request) {
	var req dto.ChargeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apinizer.WriteError(w, fmt.Errorf("invalid JSON")); return
	}
	if err := validate.Struct(req); err != nil {
		apinizer.WriteError(w, fmt.Errorf("validation: %s", err.Error())); return
	}
	// Wajib cek: claims.bookingId == req.bookingId agar token booking A tidak bisa dipakai untuk booking B.
	if claims, ok := middleware.GetPaymentTokenClaims(r.Context()); ok {
		if claims.BookingID != req.BookingID {
			http.Error(w, `{"status":"403","message":"Payment Token tidak cocok dengan booking ini","data":{}}`, http.StatusForbidden)
			return
		}
	}
	result, err := h.usecase.Charge(r.Context(), req)
	if err != nil { apinizer.WriteError(w, err); return }
	apinizer.Success(w, "Pembayaran dimulai", result)
}

func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	var cb dto.MidtransCallback
	if err := json.NewDecoder(r.Body).Decode(&cb); err != nil {
		apinizer.WriteError(w, fmt.Errorf("invalid JSON")); return
	}
	if err := h.usecase.HandleCallback(r.Context(), cb); err != nil {
		apinizer.WriteError(w, err); return
	}
	apinizer.Success(w, "OK", map[string]bool{"received": true})
}
