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
