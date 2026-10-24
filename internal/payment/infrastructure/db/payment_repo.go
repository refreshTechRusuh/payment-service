package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/entity"
	"github.com/refreshTechRusuh/payment-service/pkg/apperror"
)

// dbLog adalah helper untuk log query DB secara konsisten.
func dbLog(ctx context.Context, log *slog.Logger, op string, start time.Time, err error, args ...any) {
	elapsed := time.Since(start)
	base := append([]any{slog.String("op", op), slog.String("duration", elapsed.String())}, args...)
	if err != nil {
		log.ErrorContext(ctx, "db query failed", append(base, slog.String("error", err.Error()))...)
	} else {
		log.InfoContext(ctx, "db query ok", base...)
	}
}

type PaymentRepository struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func NewPaymentRepository(pool *pgxpool.Pool, log *slog.Logger) *PaymentRepository {
	return &PaymentRepository{pool: pool, log: log}
}

func (r *PaymentRepository) Create(ctx context.Context, tx pgx.Tx, p entity.Payment) error {
	start := time.Now()
	_, err := tx.Exec(ctx,
		`INSERT INTO payment (id, booking_id, method, status, provider_ref, snap_token, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		p.ID, p.BookingID, p.Method, p.Status, p.ProviderRef, p.SnapToken, p.CreatedAt)
	dbLog(ctx, r.log, "payment.insert", start, err,
		slog.String("paymentId", p.ID),
		slog.String("bookingId", p.BookingID))
	return err
}

func (r *PaymentRepository) GetByBookingID(ctx context.Context, bookingID string) (entity.Payment, error) {
	start := time.Now()
	var p entity.Payment
	err := r.pool.QueryRow(ctx,
		`SELECT id, booking_id, method, status, COALESCE(provider_ref,''), COALESCE(snap_token,''), created_at
		 FROM payment WHERE booking_id=$1 ORDER BY created_at DESC LIMIT 1`, bookingID).
		Scan(&p.ID, &p.BookingID, &p.Method, &p.Status, &p.ProviderRef, &p.SnapToken, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			dbLog(ctx, r.log, "payment.get_by_booking_id", start, err, slog.String("bookingId", bookingID))
			return entity.Payment{}, apperror.NotFound("payment")
		}
		dbLog(ctx, r.log, "payment.get_by_booking_id", start, err, slog.String("bookingId", bookingID))
		return entity.Payment{}, err
	}
	dbLog(ctx, r.log, "payment.get_by_booking_id", start, nil,
		slog.String("bookingId", bookingID),
		slog.String("paymentId", p.ID),
		slog.String("status", string(p.Status)))
	return p, nil
}

func (r *PaymentRepository) UpdateStatus(ctx context.Context, tx pgx.Tx, paymentID string, status entity.PaymentStatus) error {
	start := time.Now()
	_, err := tx.Exec(ctx, `UPDATE payment SET status=$2 WHERE id=$1`, paymentID, status)
	dbLog(ctx, r.log, "payment.update_status", start, err,
		slog.String("paymentId", paymentID),
		slog.String("status", string(status)))
	return err
}

// UpdatePaymentStatusByBookingID update status payment terbaru milik bookingID.
func (r *PaymentRepository) UpdatePaymentStatusByBookingID(ctx context.Context, tx pgx.Tx, bookingID string, status entity.PaymentStatus) error {
	start := time.Now()
	_, err := tx.Exec(ctx,
		`UPDATE payment SET status=$2 WHERE booking_id=$1 AND status='PENDING'`,
		bookingID, status)
	dbLog(ctx, r.log, "payment.update_status_by_booking", start, err,
		slog.String("bookingId", bookingID),
		slog.String("status", string(status)))
	return err
}

// UpdateStatusesForCallback update booking + payment status dalam satu transaksi.
func (r *PaymentRepository) UpdateStatusesForCallback(ctx context.Context, bookingID string, internalStatus string) error {
	start := time.Now()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		dbLog(ctx, r.log, "payment.callback_tx.begin", start, err, slog.String("bookingId", bookingID))
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	switch internalStatus {
	case "SUCCESS":
		r.UpdateBookingStatus(ctx, tx, bookingID, "PAID")                                         //nolint:errcheck
		r.UpdatePaymentStatusByBookingID(ctx, tx, bookingID, entity.PaymentStatusSuccess)           //nolint:errcheck
	case "FAILED", "EXPIRED":
		r.UpdateBookingStatus(ctx, tx, bookingID, "EXPIRED")                                       //nolint:errcheck
		r.UpdatePaymentStatusByBookingID(ctx, tx, bookingID, entity.PaymentStatusFailed)            //nolint:errcheck
	}

	if err := tx.Commit(ctx); err != nil {
		dbLog(ctx, r.log, "payment.callback_tx.commit", start, err, slog.String("bookingId", bookingID))
		return err
	}
	dbLog(ctx, r.log, "payment.callback_tx", start, nil,
		slog.String("bookingId", bookingID),
		slog.String("internalStatus", internalStatus))
	return nil
}

// GetBookingSnapshot membaca booking + items (payment-service mengakses tabel booking read-only).
func (r *PaymentRepository) GetBookingSnapshot(ctx context.Context, bookingID string) (entity.BookingSnapshot, error) {
	start := time.Now()
	var snap entity.BookingSnapshot
	err := r.pool.QueryRow(ctx,
		`SELECT id, event_id, email, status, payment_expires_at FROM booking WHERE id=$1`, bookingID).
		Scan(&snap.ID, &snap.EventID, &snap.Email, &snap.Status, &snap.PaymentExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			dbLog(ctx, r.log, "payment.get_booking_snapshot", start, err, slog.String("bookingId", bookingID))
			return entity.BookingSnapshot{}, apperror.NotFound("booking")
		}
		dbLog(ctx, r.log, "payment.get_booking_snapshot", start, err, slog.String("bookingId", bookingID))
		return entity.BookingSnapshot{}, fmt.Errorf("booking snapshot: %w", err)
	}
	rows, err := r.pool.Query(ctx,
		`SELECT category_id, nik, price_snapshot FROM booking_item WHERE booking_id=$1`, bookingID)
	if err != nil {
		return snap, err
	}
	defer rows.Close()
	for rows.Next() {
		var item entity.BookingItemSnapshot
		rows.Scan(&item.CategoryID, &item.NIK, &item.PriceSnapshot)
		snap.Items = append(snap.Items, item)
	}
	dbLog(ctx, r.log, "payment.get_booking_snapshot", start, nil,
		slog.String("bookingId", bookingID),
		slog.String("status", snap.Status),
		slog.Int("items", len(snap.Items)))
	return snap, nil
}

func (r *PaymentRepository) UpdateBookingStatus(ctx context.Context, tx pgx.Tx, bookingID string, status string) error {
	start := time.Now()
	_, err := tx.Exec(ctx, `UPDATE booking SET status=$2 WHERE id=$1`, bookingID, status)
	dbLog(ctx, r.log, "payment.update_booking_status", start, err,
		slog.String("bookingId", bookingID),
		slog.String("status", status))
	return err
}

// LogCallback menyimpan idempotency key webhook Midtrans.
func (r *PaymentRepository) LogCallback(ctx context.Context, idempotencyKey, bookingID, result string) (bool, error) {
	start := time.Now()
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO payment_callback_log (idempotency_key, booking_id, result, processed_at)
		 VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
		idempotencyKey, bookingID, result, time.Now())
	if err != nil {
		dbLog(ctx, r.log, "payment.log_callback", start, err,
			slog.String("idempotencyKey", idempotencyKey),
			slog.String("bookingId", bookingID))
		return false, fmt.Errorf("log callback: %w", err)
	}
	alreadyProcessed := tag.RowsAffected() == 0
	dbLog(ctx, r.log, "payment.log_callback", start, nil,
		slog.String("idempotencyKey", idempotencyKey),
		slog.String("bookingId", bookingID),
		slog.Bool("duplicate", alreadyProcessed))
	return alreadyProcessed, nil
}

// ListExpiredPending mengembalikan payment PENDING yang booking TTL-nya habis.
func (r *PaymentRepository) ListExpiredPending(ctx context.Context, limit int) ([]entity.Payment, error) {
	start := time.Now()
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.booking_id, p.method, p.status, COALESCE(p.provider_ref,''), COALESCE(p.snap_token,''), p.created_at
		FROM payment p
		JOIN booking b ON b.id = p.booking_id
		WHERE p.status = 'PENDING'
		  AND b.payment_expires_at < NOW()
		LIMIT $1
		FOR UPDATE OF p SKIP LOCKED`, limit)
	if err != nil {
		dbLog(ctx, r.log, "payment.list_expired_pending", start, err)
		return nil, fmt.Errorf("list expired pending: %w", err)
	}
	defer rows.Close()

	var result []entity.Payment
	for rows.Next() {
		var p entity.Payment
		if err := rows.Scan(&p.ID, &p.BookingID, &p.Method, &p.Status, &p.ProviderRef, &p.SnapToken, &p.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	dbLog(ctx, r.log, "payment.list_expired_pending", start, nil, slog.Int("count", len(result)))
	return result, nil
}

// ExpirePaymentTx mengeksekusi UPDATE payment=EXPIRED + booking=EXPIRED dalam satu transaksi.
func (r *PaymentRepository) ExpirePaymentTx(ctx context.Context, paymentID, bookingID string) error {
	start := time.Now()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		dbLog(ctx, r.log, "payment.expire_tx.begin", start, err, slog.String("paymentId", paymentID))
		return fmt.Errorf("begin expire tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`UPDATE payment SET status='EXPIRED' WHERE id=$1 AND status='PENDING'`, paymentID); err != nil {
		dbLog(ctx, r.log, "payment.expire_tx.update_payment", start, err, slog.String("paymentId", paymentID))
		return fmt.Errorf("expire payment: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE booking SET status='EXPIRED' WHERE id=$1 AND status='HELD'`, bookingID); err != nil {
		dbLog(ctx, r.log, "payment.expire_tx.update_booking", start, err, slog.String("bookingId", bookingID))
		return fmt.Errorf("expire booking: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		dbLog(ctx, r.log, "payment.expire_tx.commit", start, err, slog.String("paymentId", paymentID))
		return err
	}
	dbLog(ctx, r.log, "payment.expire_tx", start, nil,
		slog.String("paymentId", paymentID),
		slog.String("bookingId", bookingID))
	return nil
}

// GetStockByEvent mengembalikan snapshot stok per kategori untuk sold-out detection.
func (r *PaymentRepository) GetStockByEvent(ctx context.Context, eventID string) ([]entity.CategoryStock, error) {
	start := time.Now()
	rows, err := r.pool.Query(ctx,
		`SELECT id, COALESCE(name,''), available FROM ticket_category WHERE event_id=$1`, eventID)
	if err != nil {
		dbLog(ctx, r.log, "payment.get_stock_by_event", start, err, slog.String("eventId", eventID))
		return nil, fmt.Errorf("get stock by event: %w", err)
	}
	defer rows.Close()

	var result []entity.CategoryStock
	for rows.Next() {
		var cs entity.CategoryStock
		if err := rows.Scan(&cs.CategoryID, &cs.Name, &cs.Available); err != nil {
			return nil, err
		}
		result = append(result, cs)
	}
	dbLog(ctx, r.log, "payment.get_stock_by_event", start, nil,
		slog.String("eventId", eventID),
		slog.Int("categories", len(result)))
	return result, nil
}

// HasActiveBooking mengembalikan true bila masih ada booking HELD untuk event.
func (r *PaymentRepository) HasActiveBooking(ctx context.Context, eventID string) (bool, error) {
	start := time.Now()
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM booking
		WHERE event_id=$1
		  AND status='HELD'
		  AND payment_expires_at > NOW()
		LIMIT 1`, eventID).Scan(&count)
	dbLog(ctx, r.log, "payment.has_active_booking", start, err,
		slog.String("eventId", eventID),
		slog.Bool("result", count > 0))
	if err != nil {
		return false, fmt.Errorf("has active booking: %w", err)
	}
	return count > 0, nil
}

// CreatePaymentTx menyimpan payment baru dalam satu transaksi.
func (r *PaymentRepository) CreatePaymentTx(ctx context.Context, p entity.Payment) error {
	start := time.Now()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		dbLog(ctx, r.log, "payment.create_tx.begin", start, err, slog.String("paymentId", p.ID))
		return fmt.Errorf("create payment tx begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := r.Create(ctx, tx, p); err != nil {
		return fmt.Errorf("create payment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		dbLog(ctx, r.log, "payment.create_tx.commit", start, err, slog.String("paymentId", p.ID))
		return err
	}
	dbLog(ctx, r.log, "payment.create_tx", start, nil,
		slog.String("paymentId", p.ID),
		slog.String("bookingId", p.BookingID),
		slog.String("method", string(p.Method)))
	return nil
}
