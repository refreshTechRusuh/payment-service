package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/dto"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/entity"
	"github.com/refreshTechRusuh/payment-service/internal/payment/port/outbound"
	"github.com/refreshTechRusuh/payment-service/pkg/apperror"
)

const backDisabledThreshold = 30 // seconds

// PaymentMethod config — estSeconds konfigurable via env (placeholder values).
var defaultMethods = []dto.PaymentMethod{
	{Code: "bca_va", Name: "BCA Virtual Account", EstSeconds: 180},
	{Code: "qris", Name: "QRIS", EstSeconds: 60},
	{Code: "gopay", Name: "GoPay", EstSeconds: 45},
}

type PaymentUsecase struct {
	pool        *pgxpool.Pool
	repo        outbound.IPaymentRepository
	gateway     outbound.IPaymentGateway
	producer    outbound.IEventProducer
	midtransKey string // server key untuk verifikasi signature
	log         *slog.Logger
}

func NewPaymentUsecase(
	pool *pgxpool.Pool,
	repo outbound.IPaymentRepository,
	gateway outbound.IPaymentGateway,
	producer outbound.IEventProducer,
	midtransServerKey string,
	log *slog.Logger,
) *PaymentUsecase {
	return &PaymentUsecase{pool: pool, repo: repo, gateway: gateway, producer: producer, midtransKey: midtransServerKey, log: log}
}

// GetBookingPayment mengembalikan detail payment page dengan enabled flag dinamis.
func (u *PaymentUsecase) GetBookingPayment(ctx context.Context, bookingID string) (dto.BookingPaymentDetailResponse, error) {
	snap, err := u.repo.GetBookingSnapshot(ctx, bookingID)
	if err != nil {
		return dto.BookingPaymentDetailResponse{}, apperror.NotFound("booking")
	}

	remaining := 0
	if snap.PaymentExpiresAt != nil {
		remaining = int(time.Until(*snap.PaymentExpiresAt).Seconds())
		if remaining < 0 {
			remaining = 0
		}
	}

	var total int64
	for _, item := range snap.Items {
		total += item.PriceSnapshot
	}

	methods := make([]dto.PaymentMethod, len(defaultMethods))
	for i, m := range defaultMethods {
		methods[i] = dto.PaymentMethod{
			Code:       m.Code,
			Name:       m.Name,
			EstSeconds: m.EstSeconds,
			Enabled:    remaining >= m.EstSeconds, // disable bila tak cukup waktu
		}
	}

	return dto.BookingPaymentDetailResponse{
		BookingID:        bookingID,
		Total:            total,
		Methods:          methods,
		RemainingSeconds: remaining,
		Status:           snap.Status,
		BackDisabled:     remaining < backDisabledThreshold,
	}, nil
}

// Charge memanggil Midtrans Snap API dan menyimpan payment.
func (u *PaymentUsecase) Charge(ctx context.Context, req dto.ChargeRequest) (dto.ChargeResponse, error) {
	snap, err := u.repo.GetBookingSnapshot(ctx, req.BookingID)
	if err != nil {
		return dto.ChargeResponse{}, apperror.NotFound("booking")
	}

	// Validasi booking HELD + TTL
	if snap.Status != "HELD" {
		return dto.ChargeResponse{}, apperror.BookingExpired()
	}
	if snap.PaymentExpiresAt != nil && time.Now().After(*snap.PaymentExpiresAt) {
		return dto.ChargeResponse{}, apperror.BookingExpired()
	}

	// Enforce server-side: cek method estSeconds
	remaining := int(time.Until(*snap.PaymentExpiresAt).Seconds())
	for _, m := range defaultMethods {
		if m.Code == req.Method && remaining < m.EstSeconds {
			return dto.ChargeResponse{}, apperror.MethodTimeInsufficient()
		}
	}

	// Kalkulasi total
	var total int64
	snapItems := make([]outbound.SnapItem, 0, len(snap.Items))
	for _, item := range snap.Items {
		total += item.PriceSnapshot
		snapItems = append(snapItems, outbound.SnapItem{
			ID:    item.CategoryID,
			Name:  "Tiket NIK " + item.NIK[:4] + "****",
			Price: item.PriceSnapshot,
			Quantity: 1,
		})
	}

	// Panggil Midtrans Snap
	snapResult, err := u.gateway.CreateSnapToken(ctx, outbound.SnapRequest{
		OrderID:     req.BookingID, // bookingId = order_id (idempoten)
		GrossAmount: total,
		Email:       snap.Email,
		Items:       snapItems,
	})
	if err != nil {
		return dto.ChargeResponse{}, apperror.Internal(err)
	}

	// Simpan payment
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return dto.ChargeResponse{}, apperror.Internal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	paymentID := fmt.Sprintf("pay-%d", time.Now().UnixNano())
	p := entity.Payment{
		ID:        paymentID,
		BookingID: req.BookingID,
		Method:    req.Method,
		Status:    entity.PaymentStatusPending,
		SnapToken: snapResult.SnapToken,
		CreatedAt: time.Now(),
	}
	if err := u.repo.Create(ctx, tx, p); err != nil {
		return dto.ChargeResponse{}, apperror.Internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return dto.ChargeResponse{}, apperror.Internal(err)
	}

	return dto.ChargeResponse{
		PaymentID:   paymentID,
		Status:      "PENDING",
		SnapToken:   snapResult.SnapToken,
		RedirectURL: snapResult.RedirectURL,
		Method:      req.Method,
	}, nil
}

// HandleCallback memproses webhook Midtrans secara idempoten.
func (u *PaymentUsecase) HandleCallback(ctx context.Context, cb dto.MidtransCallback) error {
	// 1. Verifikasi signature_key
	if !verifyMidtransSignature(u.midtransKey, cb.OrderID, cb.StatusCode, cb.GrossAmount, cb.SignatureKey) {
		return apperror.New(apperror.ErrUnauthorized, "Signature tidak valid")
	}

	// 2. Idempotency: INSERT ON CONFLICT DO NOTHING
	idempotencyKey := cb.OrderID + ":" + cb.TransactionStatus
	alreadyProcessed, err := u.repo.LogCallback(ctx, idempotencyKey, cb.OrderID, cb.TransactionStatus)
	if err != nil {
		return apperror.Internal(err)
	}
	if alreadyProcessed {
		u.log.InfoContext(ctx, "callback already processed (idempoten)", "key", idempotencyKey)
		return nil // idempoten — tidak proses ulang
	}

	// 3. Map status
	internalStatus := mapTransactionStatus(cb.TransactionStatus, cb.FraudStatus)

	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return apperror.Internal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	snap, _ := u.repo.GetBookingSnapshot(ctx, cb.OrderID)

	switch internalStatus {
	case "SUCCESS":
		u.repo.UpdateBookingStatus(ctx, tx, cb.OrderID, "PAID")
	case "FAILED", "EXPIRED":
		u.repo.UpdateBookingStatus(ctx, tx, cb.OrderID, "EXPIRED")
	}

	tx.Commit(ctx) //nolint:errcheck

	// 4. Publish Kafka events (async, non-blocking)
	go u.publishCallbackEvents(context.WithoutCancel(ctx), internalStatus, cb.OrderID, snap.EventID)

	return nil
}

func (u *PaymentUsecase) publishCallbackEvents(ctx context.Context, status, bookingID, eventID string) {
	switch status {
	case "SUCCESS":
		u.producer.PublishPaymentSucceeded(ctx, eventID, map[string]string{"bookingId": bookingID, "eventId": eventID})
		u.producer.PublishSlotReleased(ctx, eventID, map[string]string{"bookingId": bookingID, "eventId": eventID, "reason": "PAID"})
	case "FAILED", "EXPIRED":
		u.producer.PublishPaymentExpired(ctx, eventID, map[string]string{"bookingId": bookingID, "eventId": eventID})
		u.producer.PublishSlotReleased(ctx, eventID, map[string]string{"bookingId": bookingID, "eventId": eventID, "reason": "PAYMENT_TTL"})
	}
}
