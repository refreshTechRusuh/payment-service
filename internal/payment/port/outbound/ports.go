package outbound

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/entity"
)

type IPaymentRepository interface {
	Create(ctx context.Context, tx pgx.Tx, p entity.Payment) error
	GetByBookingID(ctx context.Context, bookingID string) (entity.Payment, error)
	UpdateStatus(ctx context.Context, tx pgx.Tx, paymentID string, status entity.PaymentStatus) error

	// GetBookingSnapshot membaca booking + items dari DB (payment-service punya akses read ke tabel booking).
	GetBookingSnapshot(ctx context.Context, bookingID string) (entity.BookingSnapshot, error)
	UpdateBookingStatus(ctx context.Context, tx pgx.Tx, bookingID string, status string) error

	// LogCallback menyimpan idempotency key webhook.
	// Mengembalikan (sudah_ada bool, error).
	LogCallback(ctx context.Context, idempotencyKey, bookingID, result string) (bool, error)
}

type IPaymentGateway interface {
	// CreateSnapToken membuat transaksi Midtrans dan mengembalikan snap_token + redirect_url.
	CreateSnapToken(ctx context.Context, req SnapRequest) (SnapResult, error)
}

type SnapRequest struct {
	OrderID     string
	GrossAmount int64
	Email       string
	Items       []SnapItem
}

type SnapItem struct {
	ID       string
	Name     string
	Price    int64
	Quantity int
}

type SnapResult struct {
	SnapToken   string
	RedirectURL string
}

type IEventProducer interface {
	PublishPaymentSucceeded(ctx context.Context, eventID string, payload any) error
	PublishPaymentExpired(ctx context.Context, eventID string, payload any) error
	PublishSlotReleased(ctx context.Context, eventID string, payload any) error
}
