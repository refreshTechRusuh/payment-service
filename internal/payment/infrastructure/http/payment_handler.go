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
)

var validate = validator.New()

type Handler struct{ usecase inbound.IPaymentUsecase }

func NewWiredHandler(pool *pgxpool.Pool, cfg *config.Config, producer *kafka.Producer) *Handler {
	log := slog.Default()
	repo := db.NewPaymentRepository(pool, log)
	gw := gateway.NewMidtransGateway(cfg.Midtrans.ServerKey, cfg.Midtrans.Env, log)
	ep := event.NewKafkaProducer(producer, log)
	uc := usecase.NewPaymentUsecase(pool, repo, gw, ep, cfg.Midtrans.ServerKey, slog.Default())
	return &Handler{usecase: uc}
}

// NewWiredHandlerWithUC sama dengan NewWiredHandler tapi juga mengembalikan PaymentUsecase
// agar delivery layer bisa start sweeper.
func NewWiredHandlerWithUC(pool *pgxpool.Pool, cfg *config.Config, producer *kafka.Producer) (*Handler, *usecase.PaymentUsecase) {
	log := slog.Default()
	repo := db.NewPaymentRepository(pool, log)
	gw := gateway.NewMidtransGateway(cfg.Midtrans.ServerKey, cfg.Midtrans.Env, log)
	ep := event.NewKafkaProducer(producer, log)
	uc := usecase.NewPaymentUsecase(pool, repo, gw, ep, cfg.Midtrans.ServerKey, log)
	return &Handler{usecase: uc}, uc
}
func RegisterRoutes(mux *http.ServeMux, h *Handler, accessTokenSecret string) {
	withAccessToken := func(next http.HandlerFunc) http.Handler {
		return middleware.Chain(http.HandlerFunc(next), middleware.RequireAccessToken(accessTokenSecret))
	}

	mux.Handle("GET /api/payment/v1/booking/{bookingId}", withAccessToken(h.GetBookingPayment))
	mux.Handle("POST /api/payment/v1/charge", withAccessToken(h.Charge))
	// Callback: no auth middleware (dipanggil Midtrans, diverifikasi via signature_key)
	mux.HandleFunc("POST /api/payment/v1/callback", h.Callback)

	mux.HandleFunc("GET /sys/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"00000","message":"pong","data":{}}`))
	})
}

func (h *Handler) GetBookingPayment(w http.ResponseWriter, r *http.Request) {
	result, err := h.usecase.GetBookingPayment(r.Context(), r.PathValue("bookingId"))
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
