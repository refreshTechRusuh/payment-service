// Package usecase mengimplementasikan logika bisnis payment-service.
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/dto"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/entity"
	"github.com/refreshTechRusuh/payment-service/internal/payment/port/outbound"
	"github.com/refreshTechRusuh/payment-service/pkg/apperror"
	"github.com/refreshTechRusuh/payment-service/pkg/kafka"
)

const backDisabledThreshold = 30 // seconds

// defaultMethods adalah konfigurasi metode bayar + estimasi durasi settle.
// Nilai estSeconds diukur dari sandbox Midtrans — sesuaikan setelah benchmark.
var defaultMethods = []dto.PaymentMethod{
	{Code: "bca_va", Name: "BCA Virtual Account", EstSeconds: 180},
	{Code: "qris", Name: "QRIS", EstSeconds: 60},
	{Code: "gopay", Name: "GoPay", EstSeconds: 45},
}

// PaymentUsecase mengimplementasikan inbound.IPaymentUsecase.
type PaymentUsecase struct {
	pool        *pgxpool.Pool
	repo        outbound.IPaymentRepository
	gateway     outbound.IPaymentGateway
	producer    outbound.IEventProducer
	midtransKey string
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
// Req 7.1, 7.5 — enabled = false bila remainingSeconds < estSeconds.
func (u *PaymentUsecase) GetBookingPayment(ctx context.Context, bookingID string) (dto.BookingPaymentDetailResponse, error) {
	snap, err := u.repo.GetBookingSnapshot(ctx, bookingID)
	if err != nil {
		return dto.BookingPaymentDetailResponse{}, apperror.NotFound("booking")
	}

	remaining := remainingSeconds(snap.PaymentExpiresAt)

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
			Enabled:    remaining >= m.EstSeconds,
		}
	}

	// Jika sudah ada payment sebelumnya, sertakan snapToken-nya
	var snapToken string
	if existing, err := u.repo.GetByBookingID(ctx, bookingID); err == nil {
		snapToken = existing.SnapToken
	}

	return dto.BookingPaymentDetailResponse{
		BookingID:        bookingID,
		Total:            total,
		Methods:          methods,
		SnapToken:        snapToken,
		RemainingSeconds: remaining,
		Status:           snap.Status,
		BackDisabled:     remaining < backDisabledThreshold,
	}, nil
}

// Charge membuat transaksi Midtrans dan menyimpan payment.
// Idempoten: jika sudah ada payment PENDING untuk bookingId ini, return yang lama.
// Req 7.4 (booking HELD + TTL), Req 7.6 (enforce server-side method time), Req 8.1.
func (u *PaymentUsecase) Charge(ctx context.Context, req dto.ChargeRequest) (dto.ChargeResponse, error) {
	snap, err := u.repo.GetBookingSnapshot(ctx, req.BookingID)
	if err != nil {
		return dto.ChargeResponse{}, apperror.NotFound("booking")
	}

	// Validasi booking HELD + belum expired
	if snap.Status != "HELD" {
		return dto.ChargeResponse{}, apperror.BookingExpired()
	}
	remaining := remainingSeconds(snap.PaymentExpiresAt)
	if remaining <= 0 {
		return dto.ChargeResponse{}, apperror.BookingExpired()
	}
	// Enforce server-side: metode harus punya waktu cukup
	methodValid := false
	for _, m := range defaultMethods {
		if m.Code == req.Method {
			methodValid = true
			if remaining < m.EstSeconds {
				return dto.ChargeResponse{}, apperror.MethodTimeInsufficient()
			}
			break
		}
	}
	if !methodValid {
		return dto.ChargeResponse{}, apperror.New(apperror.ErrValidation,
			fmt.Sprintf("metode pembayaran '%s' tidak dikenal", req.Method))
	}

	// Idempotency: cek apakah sudah ada payment PENDING untuk booking ini
	if existing, err := u.repo.GetByBookingID(ctx, req.BookingID); err == nil &&
		existing.Status == entity.PaymentStatusPending {
		u.log.InfoContext(ctx, "charge idempoten: payment PENDING sudah ada",
			slog.String("bookingId", req.BookingID),
			slog.String("paymentId", existing.ID))
		return dto.ChargeResponse{
			PaymentID:   existing.ID,
			Status:      string(existing.Status),
			SnapToken:   existing.SnapToken,
			Method:      existing.Method,
		}, nil
	}

	// Hitung total + build Snap items
	var total int64
	snapItems := make([]outbound.SnapItem, 0, len(snap.Items))
	for _, item := range snap.Items {
		total += item.PriceSnapshot
		name := fmt.Sprintf("Tiket NIK %s****", safePrefix(item.NIK, 4))
		snapItems = append(snapItems, outbound.SnapItem{
			ID:       item.CategoryID,
			Name:     name,
			Price:    item.PriceSnapshot,
			Quantity: 1,
		})
	}

	// Panggil Midtrans Snap dengan expiry eksplisit agar konsisten dengan payment_expires_at di DB.
	now := time.Now()
	snapReq := outbound.SnapRequest{
		OrderID:     req.BookingID, // bookingId = order_id → idempoten di Midtrans juga
		GrossAmount: total,
		Email:       snap.Email,
		Items:       snapItems,
		Expiry: &outbound.ExpiryDetails{
			// Format Midtrans: "yyyy-MM-dd HH:mm:ss Z" dengan offset tanpa titik dua (+0700 bukan +07:00)
			StartTime: now.Format("2006-01-02 15:04:05 -0700"),
			Unit:      "seconds",
			Duration:  remaining,
		},
	}
	u.log.InfoContext(ctx, "midtrans snap expiry set",
		slog.Int("remaining_secs", remaining),
	)

	snapResult, err := u.gateway.CreateSnapToken(ctx, snapReq)
	if err != nil {
		return dto.ChargeResponse{}, apperror.Internal(fmt.Errorf("midtrans: %w", err))
	}

	// Simpan payment via CreatePaymentTx (pool.Begin dikelola di repo — testable tanpa DB)
	paymentID := uuid.New().String()
	p := entity.Payment{
		ID:          paymentID,
		BookingID:   req.BookingID,
		Method:      req.Method,
		Status:      entity.PaymentStatusPending,
		ProviderRef: req.BookingID, // order_id Midtrans = bookingId
		SnapToken:   snapResult.SnapToken,
		CreatedAt:   time.Now(),
	}
	if err := u.repo.CreatePaymentTx(ctx, p); err != nil {
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
// Req 8.3 (idempoten), Req 8.4, Req 8.5 (signature verify).
func (u *PaymentUsecase) HandleCallback(ctx context.Context, cb dto.MidtransCallback) error {
	// 1. Verifikasi signature_key Midtrans DULU
	if u.midtransKey != "" && !verifyMidtransSignature(u.midtransKey, cb.OrderID, cb.StatusCode, cb.GrossAmount, cb.SignatureKey) {
		u.log.WarnContext(ctx, "callback signature invalid", slog.String("orderID", cb.OrderID))
		return apperror.New(apperror.ErrUnauthorized, "Signature tidak valid")
	}

	// 2. Idempotency: INSERT ON CONFLICT DO NOTHING
	idempotencyKey := cb.OrderID + ":" + cb.TransactionStatus
	alreadyProcessed, err := u.repo.LogCallback(ctx, idempotencyKey, cb.OrderID, cb.TransactionStatus)
	if err != nil {
		return apperror.Internal(err)
	}
	if alreadyProcessed {
		u.log.InfoContext(ctx, "callback already processed (idempoten)", slog.String("key", idempotencyKey))
		return nil
	}

	// 3. Map status
	internalStatus := mapTransactionStatus(cb.TransactionStatus, cb.FraudStatus)
	u.log.InfoContext(ctx, "processing callback",
		slog.String("orderID", cb.OrderID),
		slog.String("txStatus", cb.TransactionStatus),
		slog.String("internalStatus", internalStatus),
	)

	// 4. Baca snapshot (untuk validasi + Kafka payload)
	snap, err := u.repo.GetBookingSnapshot(ctx, cb.OrderID)
	if err != nil {
		return apperror.NotFound("booking")
	}

	// 5. Validasi gross_amount
	if err := u.validateGrossAmount(cb.GrossAmount, snap); err != nil {
		u.log.WarnContext(ctx, "gross amount mismatch", slog.String("orderID", cb.OrderID), slog.Any("err", err))
		return err
	}

	// 6. Update status via repo (repo manages tx internally)
	if err := u.repo.UpdateStatusesForCallback(ctx, cb.OrderID, internalStatus); err != nil {
		return apperror.Internal(err)
	}

	// 7. Publish Kafka events (async, best-effort)
	go u.publishCallbackEvents(context.WithoutCancel(ctx), internalStatus, cb.OrderID, snap)

	return nil
}

// publishCallbackEvents mempublish event Kafka sesuai status.
func (u *PaymentUsecase) publishCallbackEvents(ctx context.Context, status, bookingID string, snap entity.BookingSnapshot) {
	itemEvents := make([]kafka.BookingItemEvent, 0, len(snap.Items))
	for _, item := range snap.Items {
		itemEvents = append(itemEvents, kafka.BookingItemEvent{
			CategoryID:   item.CategoryID,
			CategoryName: item.CategoryName,
			NIK:          item.NIK,
			Price:        item.PriceSnapshot,
		})
	}

	switch status {
	case "SUCCESS":
		// payment.succeeded → Notification Worker (email #2 e-tiket)
		u.producer.PublishPaymentSucceeded(ctx, snap.EventID, kafka.PaymentSucceededEvent{
			BookingID: bookingID,
			EventID:   snap.EventID,
			EventName: snap.EventName,
			Email:     snap.Email, // tujuan email e-tiket
			Items:     itemEvents,
		})
		// slot.released (PAID) → Queue Service promosi antrian
		u.producer.PublishSlotReleased(ctx, snap.EventID, kafka.SlotReleasedEvent{
			BookingID: bookingID, EventID: snap.EventID, Reason: "PAID",
		})

	case "FAILED", "EXPIRED":
		// payment.expired → Notification Worker (email #3)
		u.producer.PublishPaymentExpired(ctx, snap.EventID, kafka.PaymentExpiredEvent{
			BookingID: bookingID,
			EventID:   snap.EventID,
			Email:     snap.Email, // tujuan email pembatalan
		})
		// slot.released (PAYMENT_TTL) → Queue Service promosi antrian
		u.producer.PublishSlotReleased(ctx, snap.EventID, kafka.SlotReleasedEvent{
			BookingID: bookingID, EventID: snap.EventID, Reason: "PAYMENT_TTL",
		})
	}
}

// validateGrossAmount memverifikasi gross_amount dari Midtrans cocok dengan total di DB.
func (u *PaymentUsecase) validateGrossAmount(grossAmountStr string, snap entity.BookingSnapshot) error {
	var total int64
	for _, item := range snap.Items {
		total += item.PriceSnapshot
	}
	// Midtrans kirim "1500000.00" — parse dan bandingkan
	var grossAmount float64
	if _, err := fmt.Sscanf(grossAmountStr, "%f", &grossAmount); err != nil {
		return nil // tidak bisa parse → lewati (jangan block proses)
	}
	expected := float64(total)
	if grossAmount != expected {
		return apperror.New(apperror.ErrValidation,
			fmt.Sprintf("gross_amount mismatch: got %.2f, expected %.2f", grossAmount, expected))
	}
	return nil
}

// ---- Helpers -----------------------------------------------------------------

func remainingSeconds(t *time.Time) int {
	if t == nil {
		return 0
	}
	rem := int(time.Until(*t).Seconds())
	if rem < 0 {
		return 0
	}
	return rem
}

func safePrefix(s string, n int) string {
	r := []rune(s)
	if len(r) < n {
		return s
	}
	return string(r[:n])
}
