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
	// UpdatePaymentStatusByBookingID update status payment terbaru milik bookingID.
	UpdatePaymentStatusByBookingID(ctx context.Context, tx pgx.Tx, bookingID string, status entity.PaymentStatus) error

	// UpdateStatusesForCallback update booking + payment status dalam satu transaksi (dikelola repo).
	// Dipakai HandleCallback agar usecase tidak perlu pool.Begin langsung.
	UpdateStatusesForCallback(ctx context.Context, bookingID string, internalStatus string) error

	// GetBookingSnapshot membaca booking + items dari DB (payment-service punya akses read ke tabel booking).
	GetBookingSnapshot(ctx context.Context, bookingID string) (entity.BookingSnapshot, error)
	UpdateBookingStatus(ctx context.Context, tx pgx.Tx, bookingID string, status string) error

	// LogCallback menyimpan idempotency key webhook.
	// Mengembalikan (sudah_ada bool, error).
	LogCallback(ctx context.Context, idempotencyKey, bookingID, result string) (bool, error)

	// ListExpiredPending mengembalikan payment PENDING yang booking TTL-nya sudah habis.
	// Dipakai sweeper untuk mendeteksi pembayaran yang tidak diselesaikan tepat waktu.
	// FOR UPDATE SKIP LOCKED agar sweeper multi-instance tidak konflik.
	ListExpiredPending(ctx context.Context, limit int) ([]entity.Payment, error)

	// ExpirePaymentTx menjalankan UPDATE payment+booking dalam satu tx (dikelola repo).
	// Status payment → EXPIRED, status booking → EXPIRED.
	ExpirePaymentTx(ctx context.Context, paymentID, bookingID string) error

	// GetStockByEvent mengembalikan snapshot stok per kategori event.
	// Dipakai sold-out detection setelah payment EXPIRED.
	GetStockByEvent(ctx context.Context, eventID string) ([]entity.CategoryStock, error)

	// HasActiveBooking mengembalikan true bila masih ada booking HELD/PENDING payment untuk event ini.
	// Dipakai sold-out detection: hanya mark sold-out bila stok=0 DAN tidak ada aktif.
	HasActiveBooking(ctx context.Context, eventID string) (bool, error)

	// CreatePaymentTx menyimpan payment baru dalam satu transaksi (dikelola repo).
	// Agar usecase Charge tidak perlu pool.Begin langsung → fully testable.
	CreatePaymentTx(ctx context.Context, p entity.Payment) error
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
	// PublishEventSoldOut mempublish event.sold_out ke Kafka.
	// Dipanggil setelah sold-out detection mendeteksi event benar-benar habis.
	PublishEventSoldOut(ctx context.Context, eventID string, payload any) error
}
