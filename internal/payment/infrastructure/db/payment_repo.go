package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/entity"
	"github.com/refreshTechRusuh/payment-service/pkg/apperror"
)

type PaymentRepository struct{ pool *pgxpool.Pool }

func NewPaymentRepository(pool *pgxpool.Pool) *PaymentRepository {
	return &PaymentRepository{pool: pool}
}

func (r *PaymentRepository) Create(ctx context.Context, tx pgx.Tx, p entity.Payment) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO payment (id, booking_id, method, status, provider_ref, snap_token, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		p.ID, p.BookingID, p.Method, p.Status, p.ProviderRef, p.SnapToken, p.CreatedAt)
	return err
}

func (r *PaymentRepository) GetByBookingID(ctx context.Context, bookingID string) (entity.Payment, error) {
	var p entity.Payment
	err := r.pool.QueryRow(ctx,
		`SELECT id, booking_id, method, status, COALESCE(provider_ref,''), COALESCE(snap_token,''), created_at
		 FROM payment WHERE booking_id=$1 ORDER BY created_at DESC LIMIT 1`, bookingID).
		Scan(&p.ID, &p.BookingID, &p.Method, &p.Status, &p.ProviderRef, &p.SnapToken, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) { return entity.Payment{}, apperror.NotFound("payment") }
		return entity.Payment{}, err
	}
	return p, nil
}

func (r *PaymentRepository) UpdateStatus(ctx context.Context, tx pgx.Tx, paymentID string, status entity.PaymentStatus) error {
	_, err := tx.Exec(ctx, `UPDATE payment SET status=$2 WHERE id=$1`, paymentID, status)
	return err
}

// UpdatePaymentStatusByBookingID update status payment terbaru milik bookingID.
func (r *PaymentRepository) UpdatePaymentStatusByBookingID(ctx context.Context, tx pgx.Tx, bookingID string, status entity.PaymentStatus) error {
	_, err := tx.Exec(ctx,
		`UPDATE payment SET status=$2 WHERE booking_id=$1 AND status='PENDING'`,
		bookingID, status)
	return err
}

// UpdateStatusesForCallback update booking + payment status dalam satu transaksi.
// Pool dikelola di sini agar usecase tidak perlu pool langsung (testable).
func (r *PaymentRepository) UpdateStatusesForCallback(ctx context.Context, bookingID string, internalStatus string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
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

	return tx.Commit(ctx)
}

// GetBookingSnapshot membaca booking + items (payment-service mengakses tabel booking read-only).
func (r *PaymentRepository) GetBookingSnapshot(ctx context.Context, bookingID string) (entity.BookingSnapshot, error) {
	var snap entity.BookingSnapshot
	err := r.pool.QueryRow(ctx,
		`SELECT id, event_id, email, status, payment_expires_at FROM booking WHERE id=$1`, bookingID).
		Scan(&snap.ID, &snap.EventID, &snap.Email, &snap.Status, &snap.PaymentExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) { return entity.BookingSnapshot{}, apperror.NotFound("booking") }
		return entity.BookingSnapshot{}, fmt.Errorf("booking snapshot: %w", err)
	}
	rows, err := r.pool.Query(ctx,
		`SELECT category_id, nik, price_snapshot FROM booking_item WHERE booking_id=$1`, bookingID)
	if err != nil { return snap, err }
	defer rows.Close()
	for rows.Next() {
		var item entity.BookingItemSnapshot
		rows.Scan(&item.CategoryID, &item.NIK, &item.PriceSnapshot)
		snap.Items = append(snap.Items, item)
	}
	return snap, nil
}

func (r *PaymentRepository) UpdateBookingStatus(ctx context.Context, tx pgx.Tx, bookingID string, status string) error {
	_, err := tx.Exec(ctx, `UPDATE booking SET status=$2 WHERE id=$1`, bookingID, status)
	return err
}

// LogCallback menyimpan idempotency key webhook Midtrans.
// Mengembalikan (alreadyProcessed bool, error).
func (r *PaymentRepository) LogCallback(ctx context.Context, idempotencyKey, bookingID, result string) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO payment_callback_log (idempotency_key, booking_id, result, processed_at)
		 VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
		idempotencyKey, bookingID, result, time.Now())
	if err != nil { return false, fmt.Errorf("log callback: %w", err) }
	return tag.RowsAffected() == 0, nil // 0 rows = konflik = sudah ada
}

// ListExpiredPending mengembalikan payment PENDING yang booking TTL-nya habis.
// FOR UPDATE SKIP LOCKED: sweeper multi-instance tidak mengolah baris yang sama.
func (r *PaymentRepository) ListExpiredPending(ctx context.Context, limit int) ([]entity.Payment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.booking_id, p.method, p.status, COALESCE(p.provider_ref,''), COALESCE(p.snap_token,''), p.created_at
		FROM payment p
		JOIN booking b ON b.id = p.booking_id
		WHERE p.status = 'PENDING'
		  AND b.payment_expires_at < NOW()
		LIMIT $1
		FOR UPDATE OF p SKIP LOCKED`, limit)
	if err != nil {
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
	return result, nil
}

// ExpirePaymentTx mengeksekusi UPDATE payment=EXPIRED + booking=EXPIRED dalam satu transaksi.
// Transaksi dikelola di sini (pool.Begin) agar usecase tidak perlu pool langsung.
func (r *PaymentRepository) ExpirePaymentTx(ctx context.Context, paymentID, bookingID string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin expire tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`UPDATE payment SET status='EXPIRED' WHERE id=$1 AND status='PENDING'`, paymentID); err != nil {
		return fmt.Errorf("expire payment: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE booking SET status='EXPIRED' WHERE id=$1 AND status='HELD'`, bookingID); err != nil {
		return fmt.Errorf("expire booking: %w", err)
	}

	return tx.Commit(ctx)
}

// GetStockByEvent mengembalikan snapshot stok per kategori untuk sold-out detection.
func (r *PaymentRepository) GetStockByEvent(ctx context.Context, eventID string) ([]entity.CategoryStock, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, COALESCE(name,''), available FROM ticket_category WHERE event_id=$1`, eventID)
	if err != nil {
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
	return result, nil
}

// HasActiveBooking mengembalikan true bila masih ada booking HELD atau payment PENDING untuk event.
func (r *PaymentRepository) HasActiveBooking(ctx context.Context, eventID string) (bool, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM booking
		WHERE event_id=$1
		  AND status='HELD'
		  AND payment_expires_at > NOW()
		LIMIT 1`, eventID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("has active booking: %w", err)
	}
	return count > 0, nil
}

// CreatePaymentTx menyimpan payment baru dalam satu transaksi (dikelola di sini).
// Agar usecase Charge tidak perlu pool.Begin langsung → fully testable tanpa DB.
func (r *PaymentRepository) CreatePaymentTx(ctx context.Context, p entity.Payment) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create payment tx begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := r.Create(ctx, tx, p); err != nil {
		return fmt.Errorf("create payment: %w", err)
	}
	return tx.Commit(ctx)
}
