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

// paymentMethod adalah konfigurasi internal metode bayar untuk time-enforcement.
// Tidak dikembalikan di API — hanya dipakai untuk validasi durasi di Charge.
type paymentMethod struct {
	Code       string
	EstSeconds int
}

// defaultMethods adalah konfigurasi metode bayar + estimasi durasi settle.
// Dipakai hanya untuk time-enforcement di Charge — Methods tidak lagi dikembalikan di API
// karena Midtrans Snap menampilkan pilihan metode di checkout page-nya sendiri.
var defaultMethods = []paymentMethod{
	{Code: "bca_va", EstSeconds: 180},
	{Code: "qris", EstSeconds: 60},
	{Code: "gopay", EstSeconds: 45},
}

// PaymentUsecase mengimplementasikan inbound.IPaymentUsecase.
type PaymentUsecase struct {
	pool          *pgxpool.Pool
	repo          outbound.IPaymentRepository
	gateway       outbound.IPaymentGateway
	producer      outbound.IEventProducer
	redirectStore outbound.IRedirectStore
	midtransKey   string
	log           *slog.Logger
}

func NewPaymentUsecase(
	pool *pgxpool.Pool,
	repo outbound.IPaymentRepository,
	gateway outbound.IPaymentGateway,
	producer outbound.IEventProducer,
	redirectStore outbound.IRedirectStore,
	midtransServerKey string,
	log *slog.Logger,
) *PaymentUsecase {
	return &PaymentUsecase{
		pool:          pool,
		repo:          repo,
		gateway:       gateway,
		producer:      producer,
		redirectStore: redirectStore,
		midtransKey:   midtransServerKey,
		log:           log,
	}
}

// GetBookingPayment mengembalikan detail payment page.
// Methods dihapus — Midtrans Snap menampilkan pilihan metode sendiri.
// RedirectURL diambil dari Redis agar FE bisa redirect ulang tanpa Charge baru.
// Req 7.1, 7.5.
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

	// Ambil redirectUrl dari Redis (di-cache saat Charge) agar FE bisa redirect ulang tanpa Charge baru.
	// SnapToken tidak dikembalikan — FE selalu ambil dari POST /charge (idempoten).
	var redirectURL string
	if u.redirectStore != nil {
		redirectURL, _ = u.redirectStore.GetRedirectURL(ctx, bookingID)
	}

	return dto.BookingPaymentDetailResponse{
		BookingID:        bookingID,
		Total:            total,
		RedirectURL:      redirectURL,
		RemainingSeconds: remaining,
		Status:           snap.Status,
		BackDisabled:     remaining < backDisabledThreshold,
	}, nil
}

// Charge membuat transaksi Midtrans Snap dan menyimpan payment.
// Idempoten: jika sudah ada payment PENDING untuk bookingId ini, return yang lama.
// Method dihapus dari request — Midtrans Snap menampilkan semua pilihan, payment_type
// diterima dari callback dan disimpan saat HandleCallback.
// Req 7.4 (booking HELD + TTL), Req 8.1.
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

	// Idempotency: cek apakah sudah ada payment PENDING untuk booking ini
	if existing, err := u.repo.GetByBookingID(ctx, req.BookingID); err == nil &&
		existing.Status == entity.PaymentStatusPending {
		u.log.InfoContext(ctx, "charge idempoten: payment PENDING sudah ada",
			slog.String("bookingId", req.BookingID),
			slog.String("paymentId", existing.ID))
		// Ambil redirectUrl dari Redis jika ada
		redirectURL := ""
		if u.redirectStore != nil {
			redirectURL, _ = u.redirectStore.GetRedirectURL(ctx, req.BookingID)
		}
		return dto.ChargeResponse{
			PaymentID:   existing.ID,
			Status:      string(existing.Status),
			SnapToken:   existing.SnapToken,
			RedirectURL: redirectURL,
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

	// Simpan payment via CreatePaymentTx. Method kosong dulu, diisi dari callback payment_type.
	paymentID := uuid.New().String()
	p := entity.Payment{
		ID:          paymentID,
		BookingID:   req.BookingID,
		Method:      "", // diisi dari Midtrans callback payment_type
		Status:      entity.PaymentStatusPending,
		ProviderRef: req.BookingID, // order_id Midtrans = bookingId
		SnapToken:   snapResult.SnapToken,
		CreatedAt:   time.Now(),
	}
	if err := u.repo.CreatePaymentTx(ctx, p); err != nil {
		return dto.ChargeResponse{}, apperror.Internal(err)
	}

	// Simpan redirectUrl ke Redis dengan TTL = remaining seconds.
	if u.redirectStore != nil && snapResult.RedirectURL != "" {
		if err := u.redirectStore.SetRedirectURL(ctx, req.BookingID, snapResult.RedirectURL, remaining); err != nil {
			// Non-fatal: log saja, jangan gagalkan Charge
			u.log.WarnContext(ctx, "gagal simpan redirectUrl ke Redis",
				slog.String("bookingId", req.BookingID),
				slog.String("err", err.Error()))
		}
	}

	return dto.ChargeResponse{
		PaymentID:   paymentID,
		Status:      "PENDING",
		SnapToken:   snapResult.SnapToken,
		RedirectURL: snapResult.RedirectURL,
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

	// 6. Update status via repo (repo manages tx internally), sekaligus simpan payment_type sebagai method
	if err := u.repo.UpdateStatusesForCallback(ctx, cb.OrderID, internalStatus, cb.PaymentType); err != nil {
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
		u.producer.PublishPaymentSucceeded(ctx, snap.EventID, kafka.PaymentSucceededEvent{ // #nosec G104
			BookingID: bookingID,
			EventID:   snap.EventID,
			EventName: snap.EventName,
			Email:     snap.Email, // tujuan email e-tiket
			Items:     itemEvents,
		})
		// slot.released (PAID) → Queue Service promosi antrian
		u.producer.PublishSlotReleased(ctx, snap.EventID, kafka.SlotReleasedEvent{ // #nosec G104
			BookingID: bookingID, EventID: snap.EventID, Reason: "PAID",
		})

	case "FAILED", "EXPIRED":
		// payment.expired → Notification Worker (email #3)
		u.producer.PublishPaymentExpired(ctx, snap.EventID, kafka.PaymentExpiredEvent{ // #nosec G104
			BookingID: bookingID,
			EventID:   snap.EventID,
			Email:     snap.Email, // tujuan email pembatalan
		})
		// slot.released (PAYMENT_TTL) → Queue Service promosi antrian
		u.producer.PublishSlotReleased(ctx, snap.EventID, kafka.SlotReleasedEvent{ // #nosec G104
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
