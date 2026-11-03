package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/entity"
	"github.com/refreshTechRusuh/payment-service/pkg/apperror"
)

// sqlVerb menentukan jenis query dari KATA PERTAMA SQL asli (select/insert/update/delete/begin/commit/…).
// Akurat karena membaca SQL yang benar-benar dieksekusi, bukan menebak dari nama op.
func sqlVerb(sql string) string {
	s := strings.ToLower(strings.TrimSpace(sql))
	if i := strings.IndexAny(s, " \t\n"); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "query"
	}
	return s
}

// dbLog adalah helper untuk log query DB secara konsisten.
//   - INFO : ringkas — verb SQL (select/insert/update/delete) + op + duration + args.
//   - DEBUG: full SQL template (TANPA nilai args, jadi aman dari bocor PII seperti nik/email).
//   - ERROR: verb + full SQL + error, agar langsung terlihat tanpa menurunkan level.
//
// sql = query template dengan placeholder $1,$2 (bukan nilainya); untuk langkah
// transaksi kirim literal deskriptif seperti "BEGIN"/"COMMIT".
func dbLog(ctx context.Context, log *slog.Logger, op, sql string, start time.Time, err error, args ...any) {
	elapsed := time.Since(start)
	verb := sqlVerb(sql)
	base := append([]any{slog.String("op", op), slog.String("duration", elapsed.String())}, args...)
	if err != nil {
		log.ErrorContext(ctx, verb+" failed",
			append(base, slog.String("sql", sql), slog.String("error", err.Error()))...)
		return
	}
	log.InfoContext(ctx, verb, base...)
	// Full SQL hanya di DEBUG agar senyap di produksi (LOG_LEVEL=info).
	log.DebugContext(ctx, verb+" sql", append(base, slog.String("sql", sql))...)
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
	const qInsert = `INSERT INTO payment (id, booking_id, method, status, provider_ref, snap_token, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`
	_, err := tx.Exec(ctx, qInsert,
		p.ID, p.BookingID, p.Method, p.Status, p.ProviderRef, p.SnapToken, p.CreatedAt)
	dbLog(ctx, r.log, "payment.insert", qInsert, start, err,
		slog.String("paymentId", p.ID),
		slog.String("bookingId", p.BookingID))
	return err
}

func (r *PaymentRepository) GetByBookingID(ctx context.Context, bookingID string) (entity.Payment, error) {
	start := time.Now()
	const qGet = `SELECT id, booking_id, method, status, COALESCE(provider_ref,''), COALESCE(snap_token,''), created_at
		 FROM payment WHERE booking_id=$1 ORDER BY created_at DESC LIMIT 1`
	var p entity.Payment
	err := r.pool.QueryRow(ctx, qGet, bookingID).
		Scan(&p.ID, &p.BookingID, &p.Method, &p.Status, &p.ProviderRef, &p.SnapToken, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			dbLog(ctx, r.log, "payment.get_by_booking_id", qGet, start, err, slog.String("bookingId", bookingID))
			return entity.Payment{}, apperror.NotFound("payment")
		}
		dbLog(ctx, r.log, "payment.get_by_booking_id", qGet, start, err, slog.String("bookingId", bookingID))
		return entity.Payment{}, err
	}
	dbLog(ctx, r.log, "payment.get_by_booking_id", qGet, start, nil,
		slog.String("bookingId", bookingID),
		slog.String("paymentId", p.ID),
		slog.String("status", string(p.Status)))
	return p, nil
}

func (r *PaymentRepository) UpdateStatus(ctx context.Context, tx pgx.Tx, paymentID string, status entity.PaymentStatus) error {
	start := time.Now()
	const qUpdate = `UPDATE payment SET status=$2 WHERE id=$1`
	_, err := tx.Exec(ctx, qUpdate, paymentID, status)
	dbLog(ctx, r.log, "payment.update_status", qUpdate, start, err,
		slog.String("paymentId", paymentID),
		slog.String("status", string(status)))
	return err
}

// UpdatePaymentStatusByBookingID update status payment terbaru milik bookingID.
func (r *PaymentRepository) UpdatePaymentStatusByBookingID(ctx context.Context, tx pgx.Tx, bookingID string, status entity.PaymentStatus) error {
	start := time.Now()
	const qUpdate = `UPDATE payment SET status=$2 WHERE booking_id=$1 AND status='PENDING'`
	_, err := tx.Exec(ctx, qUpdate, bookingID, status)
	dbLog(ctx, r.log, "payment.update_status_by_booking", qUpdate, start, err,
		slog.String("bookingId", bookingID),
		slog.String("status", string(status)))
	return err
}

// UpdateStatusesForCallback update booking + payment status dalam satu transaksi.
// method adalah payment_type dari Midtrans callback — disimpan ke kolom method.
func (r *PaymentRepository) UpdateStatusesForCallback(ctx context.Context, bookingID, internalStatus, method string) error {
	start := time.Now()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		dbLog(ctx, r.log, "payment.callback_tx.begin", "BEGIN", start, err, slog.String("bookingId", bookingID))
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Update method dari payment_type callback (bisa kosong jika Midtrans tidak kirim)
	if method != "" {
		const qMethod = `UPDATE payment SET method=$2 WHERE booking_id=$1 AND status='PENDING'`
		tx.Exec(ctx, qMethod, bookingID, method) //nolint:errcheck
	}

	switch internalStatus {
	case "SUCCESS":
		r.UpdateBookingStatus(ctx, tx, bookingID, "PAID")                                         //nolint:errcheck
		r.UpdatePaymentStatusByBookingID(ctx, tx, bookingID, entity.PaymentStatusSuccess)           //nolint:errcheck
	case "FAILED", "EXPIRED":
		r.UpdateBookingStatus(ctx, tx, bookingID, "EXPIRED")                                       //nolint:errcheck
		r.UpdatePaymentStatusByBookingID(ctx, tx, bookingID, entity.PaymentStatusFailed)            //nolint:errcheck
	}

	if err := tx.Commit(ctx); err != nil {
		dbLog(ctx, r.log, "payment.callback_tx.commit", "COMMIT", start, err, slog.String("bookingId", bookingID))
		return err
	}
	dbLog(ctx, r.log, "payment.callback_tx", "TX callback", start, nil,
		slog.String("bookingId", bookingID),
		slog.String("internalStatus", internalStatus),
		slog.String("method", method))
	return nil
}

// GetBookingSnapshot membaca booking + items (payment-service mengakses tabel booking read-only).
func (r *PaymentRepository) GetBookingSnapshot(ctx context.Context, bookingID string) (entity.BookingSnapshot, error) {
	start := time.Now()
	const qSnapshot = `
		SELECT b.id, b.event_id, e.name, b.email, b.status, b.payment_expires_at
		FROM booking b
		JOIN event e ON e.id = b.event_id
		WHERE b.id=$1`
	const qItems = `
		SELECT bi.category_id, tc.name, bi.nik, bi.price_snapshot
		FROM booking_item bi
		JOIN ticket_category tc ON tc.id = bi.category_id
		WHERE bi.booking_id=$1`
	var snap entity.BookingSnapshot
	err := r.pool.QueryRow(ctx, qSnapshot, bookingID).
		Scan(&snap.ID, &snap.EventID, &snap.EventName, &snap.Email, &snap.Status, &snap.PaymentExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			dbLog(ctx, r.log, "payment.get_booking_snapshot", qSnapshot, start, err, slog.String("bookingId", bookingID))
			return entity.BookingSnapshot{}, apperror.NotFound("booking")
		}
		dbLog(ctx, r.log, "payment.get_booking_snapshot", qSnapshot, start, err, slog.String("bookingId", bookingID))
		return entity.BookingSnapshot{}, fmt.Errorf("booking snapshot: %w", err)
	}
	rows, err := r.pool.Query(ctx, qItems, bookingID)
	if err != nil {
		return snap, err
	}
	defer rows.Close()
	for rows.Next() {
		var item entity.BookingItemSnapshot
		rows.Scan(&item.CategoryID, &item.CategoryName, &item.NIK, &item.PriceSnapshot)
		snap.Items = append(snap.Items, item)
	}
	dbLog(ctx, r.log, "payment.get_booking_snapshot", qSnapshot, start, nil,
		slog.String("bookingId", bookingID),
		slog.String("status", snap.Status),
		slog.Int("items", len(snap.Items)))
	return snap, nil
}

func (r *PaymentRepository) UpdateBookingStatus(ctx context.Context, tx pgx.Tx, bookingID string, status string) error {
	start := time.Now()
	const qUpdate = `UPDATE booking SET status=$2 WHERE id=$1`
	_, err := tx.Exec(ctx, qUpdate, bookingID, status)
	dbLog(ctx, r.log, "payment.update_booking_status", qUpdate, start, err,
		slog.String("bookingId", bookingID),
		slog.String("status", status))
	return err
}

// LogCallback menyimpan idempotency key webhook Midtrans.
func (r *PaymentRepository) LogCallback(ctx context.Context, idempotencyKey, bookingID, result string) (bool, error) {
	start := time.Now()
	const qLog = `INSERT INTO payment_callback_log (idempotency_key, booking_id, result, processed_at)
		 VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`
	tag, err := r.pool.Exec(ctx, qLog,
		idempotencyKey, bookingID, result, time.Now())
	if err != nil {
		dbLog(ctx, r.log, "payment.log_callback", qLog, start, err,
			slog.String("idempotencyKey", idempotencyKey),
			slog.String("bookingId", bookingID))
		return false, fmt.Errorf("log callback: %w", err)
	}
	alreadyProcessed := tag.RowsAffected() == 0
	dbLog(ctx, r.log, "payment.log_callback", qLog, start, nil,
		slog.String("idempotencyKey", idempotencyKey),
		slog.String("bookingId", bookingID),
		slog.Bool("duplicate", alreadyProcessed))
	return alreadyProcessed, nil
}

// ListExpiredPending mengembalikan payment PENDING yang booking TTL-nya habis.
func (r *PaymentRepository) ListExpiredPending(ctx context.Context, limit int) ([]entity.Payment, error) {
	start := time.Now()
	const qList = `
		SELECT p.id, p.booking_id, p.method, p.status, COALESCE(p.provider_ref,''), COALESCE(p.snap_token,''), p.created_at
		FROM payment p
		JOIN booking b ON b.id = p.booking_id
		WHERE p.status = 'PENDING'
		  AND b.payment_expires_at < NOW()
		LIMIT $1
		FOR UPDATE OF p SKIP LOCKED`
	rows, err := r.pool.Query(ctx, qList, limit)
	if err != nil {
		dbLog(ctx, r.log, "payment.list_expired_pending", qList, start, err)
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
	dbLog(ctx, r.log, "payment.list_expired_pending", qList, start, nil, slog.Int("count", len(result)))
	return result, nil
}

// ExpirePaymentTx mengeksekusi UPDATE payment=EXPIRED + booking=EXPIRED dalam satu transaksi.
func (r *PaymentRepository) ExpirePaymentTx(ctx context.Context, paymentID, bookingID string) error {
	start := time.Now()
	const qExpirePayment = `UPDATE payment SET status='EXPIRED' WHERE id=$1 AND status='PENDING'`
	const qExpireBooking = `UPDATE booking SET status='EXPIRED' WHERE id=$1 AND status='HELD'`
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		dbLog(ctx, r.log, "payment.expire_tx.begin", "BEGIN", start, err, slog.String("paymentId", paymentID))
		return fmt.Errorf("begin expire tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, qExpirePayment, paymentID); err != nil {
		dbLog(ctx, r.log, "payment.expire_tx.update_payment", qExpirePayment, start, err, slog.String("paymentId", paymentID))
		return fmt.Errorf("expire payment: %w", err)
	}
	if _, err := tx.Exec(ctx, qExpireBooking, bookingID); err != nil {
		dbLog(ctx, r.log, "payment.expire_tx.update_booking", qExpireBooking, start, err, slog.String("bookingId", bookingID))
		return fmt.Errorf("expire booking: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		dbLog(ctx, r.log, "payment.expire_tx.commit", "COMMIT", start, err, slog.String("paymentId", paymentID))
		return err
	}
	dbLog(ctx, r.log, "payment.expire_tx", "TX expire payment", start, nil,
		slog.String("paymentId", paymentID),
		slog.String("bookingId", bookingID))
	return nil
}

// GetStockByEvent mengembalikan snapshot stok per kategori untuk sold-out detection.
func (r *PaymentRepository) GetStockByEvent(ctx context.Context, eventID string) ([]entity.CategoryStock, error) {
	start := time.Now()
	const qStock = `SELECT id, COALESCE(name,''), available FROM ticket_category WHERE event_id=$1`
	rows, err := r.pool.Query(ctx, qStock, eventID)
	if err != nil {
		dbLog(ctx, r.log, "payment.get_stock_by_event", qStock, start, err, slog.String("eventId", eventID))
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
	dbLog(ctx, r.log, "payment.get_stock_by_event", qStock, start, nil,
		slog.String("eventId", eventID),
		slog.Int("categories", len(result)))
	return result, nil
}

// HasActiveBooking mengembalikan true bila masih ada booking HELD untuk event.
func (r *PaymentRepository) HasActiveBooking(ctx context.Context, eventID string) (bool, error) {
	start := time.Now()
	const qHasActive = `
		SELECT COUNT(*) FROM booking
		WHERE event_id=$1
		  AND status='HELD'
		  AND payment_expires_at > NOW()
		LIMIT 1`
	var count int
	err := r.pool.QueryRow(ctx, qHasActive, eventID).Scan(&count)
	dbLog(ctx, r.log, "payment.has_active_booking", qHasActive, start, err,
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
		dbLog(ctx, r.log, "payment.create_tx.begin", "BEGIN", start, err, slog.String("paymentId", p.ID))
		return fmt.Errorf("create payment tx begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := r.Create(ctx, tx, p); err != nil {
		return fmt.Errorf("create payment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		dbLog(ctx, r.log, "payment.create_tx.commit", "COMMIT", start, err, slog.String("paymentId", p.ID))
		return err
	}
	dbLog(ctx, r.log, "payment.create_tx", "TX create payment", start, nil,
		slog.String("paymentId", p.ID),
		slog.String("bookingId", p.BookingID),
		slog.String("method", string(p.Method)))
	return nil
}
