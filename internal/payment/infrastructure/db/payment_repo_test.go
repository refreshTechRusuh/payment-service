package db_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v3"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/entity"
	"github.com/refreshTechRusuh/payment-service/internal/payment/infrastructure/db"
	"github.com/refreshTechRusuh/payment-service/pkg/apperror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelError + 10}))
}

func newMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, mock.ExpectationsWereMet()) })
	return mock
}

// ── PaymentRepository ─────────────────────────────────────────────────────────

func TestPaymentRepository_GetByBookingID_found(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	now := time.Now().UTC()
	rows := pgxmock.NewRows([]string{"id", "booking_id", "method", "status", "provider_ref", "snap_token", "created_at"}).
		AddRow("pay-1", "booking-1", "gopay", entity.PaymentStatusPending, "prov-ref", "snap-token", now)
	mock.ExpectQuery("SELECT").WithArgs("booking-1").WillReturnRows(rows)

	payment, err := repo.GetByBookingID(context.Background(), "booking-1")
	require.NoError(t, err)
	assert.Equal(t, "pay-1", payment.ID)
	assert.Equal(t, entity.PaymentStatusPending, payment.Status)
}

func TestPaymentRepository_GetByBookingID_notFound(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	mock.ExpectQuery("SELECT").WithArgs("unknown").WillReturnError(pgx.ErrNoRows)

	_, err := repo.GetByBookingID(context.Background(), "unknown")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.True(t, errors.As(err, &appErr))
	assert.Equal(t, apperror.ErrNotFound, appErr.Code)
}

func TestPaymentRepository_GetByBookingID_dbError(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	mock.ExpectQuery("SELECT").WithArgs("booking-1").WillReturnError(errors.New("connection error"))

	_, err := repo.GetByBookingID(context.Background(), "booking-1")
	require.Error(t, err)
}

func TestPaymentRepository_LogCallback_newEntry(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	// RowsAffected = 1 → baru diproses
	mock.ExpectExec("INSERT INTO payment_callback_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	duplicate, err := repo.LogCallback(context.Background(), "idem-key-1", "booking-1", "SUCCESS")
	require.NoError(t, err)
	assert.False(t, duplicate, "bukan duplikat karena row baru diinsert")
}

func TestPaymentRepository_LogCallback_duplicate(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	// RowsAffected = 0 → ON CONFLICT DO NOTHING → sudah diproses
	mock.ExpectExec("INSERT INTO payment_callback_log").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))

	duplicate, err := repo.LogCallback(context.Background(), "idem-key-1", "booking-1", "SUCCESS")
	require.NoError(t, err)
	assert.True(t, duplicate, "duplikat karena 0 rows affected")
}

func TestPaymentRepository_ListExpiredPending_empty(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	rows := pgxmock.NewRows([]string{"id", "booking_id", "method", "status", "provider_ref", "snap_token", "created_at"})
	mock.ExpectQuery("SELECT").WithArgs(pgxmock.AnyArg()).WillReturnRows(rows)

	payments, err := repo.ListExpiredPending(context.Background(), 50)
	require.NoError(t, err)
	assert.Empty(t, payments)
}

func TestPaymentRepository_ListExpiredPending_dbError(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	mock.ExpectQuery("SELECT").WithArgs(pgxmock.AnyArg()).WillReturnError(errors.New("db error"))

	_, err := repo.ListExpiredPending(context.Background(), 50)
	require.Error(t, err)
}

func TestPaymentRepository_GetBookingSnapshot_notFound(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	mock.ExpectQuery("SELECT").WithArgs("unknown").WillReturnError(pgx.ErrNoRows)

	_, err := repo.GetBookingSnapshot(context.Background(), "unknown")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.True(t, errors.As(err, &appErr))
	assert.Equal(t, apperror.ErrNotFound, appErr.Code)
}

func TestPaymentRepository_GetBookingSnapshot_found(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	now := time.Now().UTC()
	snapRows := pgxmock.NewRows([]string{"id", "event_id", "name", "email", "status", "payment_expires_at"}).
		AddRow("booking-1", "event-1", "Konser A", "a@b.com", "HELD", now)
	mock.ExpectQuery("SELECT").WithArgs("booking-1").WillReturnRows(snapRows)

	itemRows := pgxmock.NewRows([]string{"category_id", "name", "nik", "price_snapshot"}).
		AddRow("cat-1", "VIP", "3273000000000001", int64(500000))
	mock.ExpectQuery("SELECT").WithArgs("booking-1").WillReturnRows(itemRows)

	snap, err := repo.GetBookingSnapshot(context.Background(), "booking-1")
	require.NoError(t, err)
	assert.Equal(t, "booking-1", snap.ID)
	assert.Equal(t, "Konser A", snap.EventName)
	assert.Len(t, snap.Items, 1)
}

func TestPaymentRepository_HasActiveBooking_true(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	rows := pgxmock.NewRows([]string{"count"}).AddRow(2)
	mock.ExpectQuery("SELECT COUNT").WithArgs("event-1").WillReturnRows(rows)

	has, err := repo.HasActiveBooking(context.Background(), "event-1")
	require.NoError(t, err)
	assert.True(t, has)
}

func TestPaymentRepository_HasActiveBooking_false(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	rows := pgxmock.NewRows([]string{"count"}).AddRow(0)
	mock.ExpectQuery("SELECT COUNT").WithArgs("event-1").WillReturnRows(rows)

	has, err := repo.HasActiveBooking(context.Background(), "event-1")
	require.NoError(t, err)
	assert.False(t, has)
}

// CreatePaymentTx — kelola transaksi sendiri.
func TestPaymentRepository_CreatePaymentTx_success(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	now := time.Now().UTC()
	p := entity.Payment{
		ID: "pay-1", BookingID: "booking-1",
		Method: "gopay", Status: entity.PaymentStatusPending,
		CreatedAt: now,
	}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO payment").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err := repo.CreatePaymentTx(context.Background(), p)
	require.NoError(t, err)
}

func TestPaymentRepository_CreatePaymentTx_beginError(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	mock.ExpectBegin().WillReturnError(errors.New("pool exhausted"))

	err := repo.CreatePaymentTx(context.Background(), entity.Payment{})
	require.Error(t, err)
}

func TestPaymentRepository_GetStockByEvent(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	rows := pgxmock.NewRows([]string{"id", "name", "available"}).
		AddRow("cat-1", "VIP", 45).
		AddRow("cat-2", "Festival", 200)
	mock.ExpectQuery("SELECT").WithArgs("event-1").WillReturnRows(rows)

	stocks, err := repo.GetStockByEvent(context.Background(), "event-1")
	require.NoError(t, err)
	assert.Len(t, stocks, 2)
	assert.Equal(t, 45, stocks[0].Available)
}

// ExpirePaymentTx — kelola transaksi sendiri.
func TestPaymentRepository_ExpirePaymentTx_success(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE payment SET status").WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE booking SET status").WithArgs(pgxmock.AnyArg()).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	err := repo.ExpirePaymentTx(context.Background(), "pay-1", "booking-1")
	require.NoError(t, err)
}

func TestPaymentRepository_ExpirePaymentTx_beginError(t *testing.T) {
	mock := newMock(t)
	repo := db.NewPaymentRepository(mock, discardLog())

	mock.ExpectBegin().WillReturnError(errors.New("pool exhausted"))

	err := repo.ExpirePaymentTx(context.Background(), "pay-1", "booking-1")
	require.Error(t, err)
}
