package dto

// BookingPaymentDetailResponse adalah response GET /booking/{bookingId}.
// Methods dihapus karena Midtrans Snap menampilkan semua metode di checkout page-nya sendiri.
// RedirectURL diambil dari Redis (di-cache saat Charge) agar FE bisa redirect ulang tanpa Charge baru.
type BookingPaymentDetailResponse struct {
	BookingID        string `json:"bookingId"`
	Total            int64  `json:"total"`
	SnapToken        string `json:"snapToken,omitempty"`
	RedirectURL      string `json:"redirectUrl,omitempty"`
	RemainingSeconds int    `json:"remainingSeconds"`
	Status           string `json:"status"`
	BackDisabled     bool   `json:"backDisabled"`
}

// ChargeRequest adalah body POST /payments/charge.
// Method dan IdempotencyKey dihapus:
//   - Method: Midtrans Snap menampilkan semua metode, payment_type diambil dari callback.
//   - IdempotencyKey: idempotency dijamin via bookingId (satu booking → satu payment PENDING).
type ChargeRequest struct {
	BookingID string `json:"bookingId" validate:"required"`
}

// ChargeResponse adalah response POST /payments/charge.
type ChargeResponse struct {
	PaymentID   string `json:"paymentId"`
	Status      string `json:"status"`
	SnapToken   string `json:"snapToken,omitempty"`
	RedirectURL string `json:"redirectUrl,omitempty"`
}

// MidtransCallback adalah body POST /payments/callback (webhook Midtrans).
type MidtransCallback struct {
	OrderID           string `json:"order_id"`
	TransactionStatus string `json:"transaction_status"`
	StatusCode        string `json:"status_code"`
	GrossAmount       string `json:"gross_amount"`
	PaymentType       string `json:"payment_type"`
	SignatureKey      string `json:"signature_key"`
	FraudStatus       string `json:"fraud_status,omitempty"`
}
