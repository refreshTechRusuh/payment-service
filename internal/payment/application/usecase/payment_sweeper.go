package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/refreshTechRusuh/payment-service/pkg/kafka"
)

const (
	sweeperInterval = 60 * time.Second // cek tiap 60 detik (payment TTL 10 menit)
	sweeperBatch    = 50
)

// StartSweeper menjalankan payment TTL sweeper sebagai goroutine latar belakang.
// Bertanggung jawab mendeteksi payment PENDING yang booking TTL-nya habis tanpa callback Midtrans.
// Kasus ini terjadi bila: user menutup browser, koneksi putus, atau Midtrans gagal kirim webhook.
// Berhenti ketika ctx di-cancel (graceful shutdown).
func (u *PaymentUsecase) StartSweeper(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(sweeperInterval)
		defer ticker.Stop()
		u.log.InfoContext(ctx, "payment sweeper started", slog.Duration("interval", sweeperInterval))

		for {
			select {
			case <-ctx.Done():
				u.log.InfoContext(ctx, "payment sweeper stopped")
				return
			case <-ticker.C:
				u.sweepExpiredPayments(ctx)
			}
		}
	}()
}

// SweepOnce menjalankan satu siklus sweep synchronously (untuk testing).
func (u *PaymentUsecase) SweepOnce(ctx context.Context) {
	u.sweepExpiredPayments(ctx)
}

// sweepExpiredPayments mendeteksi dan memproses payment PENDING yang TTL-nya habis.
func (u *PaymentUsecase) sweepExpiredPayments(ctx context.Context) {
	payments, err := u.repo.ListExpiredPending(ctx, sweeperBatch)
	if err != nil {
		u.log.ErrorContext(ctx, "sweeper: list expired pending failed", slog.Any("err", err))
		return
	}
	if len(payments) == 0 {
		return
	}

	u.log.InfoContext(ctx, "sweeper: processing expired payments", slog.Int("count", len(payments)))
	for _, p := range payments {
		if err := u.expirePayment(ctx, p.ID, p.BookingID); err != nil {
			u.log.ErrorContext(ctx, "sweeper: expire payment failed",
				slog.String("paymentId", p.ID),
				slog.String("bookingId", p.BookingID),
				slog.Any("err", err))
		}
	}
}

// expirePayment mengeksekusi satu payment expired:
// UPDATE payment=EXPIRED + booking=EXPIRED dalam satu tx, lalu publish Kafka events.
func (u *PaymentUsecase) expirePayment(ctx context.Context, paymentID, bookingID string) error {
	// 1. Ambil snapshot sebelum update (untuk Kafka payload + eventID)
	snap, snapErr := u.repo.GetBookingSnapshot(ctx, bookingID)
	eventID := ""
	if snapErr == nil {
		eventID = snap.EventID
	}

	// 2. Atomically update payment=EXPIRED + booking=EXPIRED
	if err := u.repo.ExpirePaymentTx(ctx, paymentID, bookingID); err != nil {
		return err
	}

	// 3. Publish Kafka events setelah commit (best-effort, tidak rollback expire)
	go u.postExpirePaymentPublish(context.WithoutCancel(ctx), paymentID, bookingID, eventID)

	return nil
}

// postExpirePaymentPublish mempublish semua side-effect setelah payment di-expire.
func (u *PaymentUsecase) postExpirePaymentPublish(ctx context.Context, paymentID, bookingID, eventID string) {
	if eventID == "" {
		u.log.WarnContext(ctx, "sweeper: no eventId for expired payment, skipping Kafka publish",
			slog.String("paymentId", paymentID))
		return
	}

	// payment.expired → Notification Worker (email #3 pembatalan)
	if err := u.producer.PublishPaymentExpired(ctx, eventID, kafka.PaymentExpiredEvent{
		PaymentID: paymentID,
		BookingID: bookingID,
		EventID:   eventID,
	}); err != nil {
		u.log.ErrorContext(ctx, "publish payment.expired failed", slog.Any("err", err))
	}

	// slot.released (PAYMENT_TTL) → Queue Service promosi antrian berikutnya
	if err := u.producer.PublishSlotReleased(ctx, eventID, kafka.SlotReleasedEvent{
		BookingID: bookingID,
		EventID:   eventID,
		Reason:    "PAYMENT_TTL",
	}); err != nil {
		u.log.ErrorContext(ctx, "publish slot.released failed", slog.Any("err", err))
	}

	// Sold-out detection: cek stok + active bookings setelah release
	u.checkAndPublishSoldOut(ctx, eventID)
}

// checkAndPublishSoldOut memeriksa apakah event sold-out setelah perubahan stok.
// Hanya publish event.sold_out bila SEMUA stok 0 DAN tidak ada booking HELD aktif.
func (u *PaymentUsecase) checkAndPublishSoldOut(ctx context.Context, eventID string) {
	stocks, err := u.repo.GetStockByEvent(ctx, eventID)
	if err != nil {
		u.log.WarnContext(ctx, "sold-out check: get stock failed",
			slog.String("eventId", eventID), slog.Any("err", err))
		return
	}
	if len(stocks) == 0 {
		return
	}

	// Cek semua kategori = 0
	for _, s := range stocks {
		if s.Available > 0 {
			return // masih ada stok → tidak sold-out
		}
	}

	// Semua 0 → cek apakah masih ada booking HELD dalam TTL
	hasActive, err := u.repo.HasActiveBooking(ctx, eventID)
	if err != nil {
		u.log.WarnContext(ctx, "sold-out check: has active booking failed", slog.Any("err", err))
		return
	}
	if hasActive {
		// Ada HELD aktif = stok mungkin akan di-release setelah TTL → tunda sold-out
		u.log.InfoContext(ctx, "sold-out check: stok 0 tapi masih ada HELD aktif, skip",
			slog.String("eventId", eventID))
		return
	}

	// Event benar-benar sold-out: semua stok 0 + tidak ada HELD aktif
	u.log.InfoContext(ctx, "event sold-out detected dari payment sweeper",
		slog.String("eventId", eventID))

	if err := u.producer.PublishEventSoldOut(ctx, eventID, kafka.EventSoldOutEvent{
		EventID: eventID,
	}); err != nil {
		u.log.ErrorContext(ctx, "publish event.sold_out failed", slog.Any("err", err))
	}
}
