package dto

// BookingPaymentDetailResponse adalah response GET /bookings/{bookingId}/payment.
type BookingPaymentDetailResponse struct {
	BookingID        string          `json:"bookingId"`
	Total            int64           `json:"total"`
	Methods          []PaymentMethod `json:"methods"`
	SnapToken        string          `json:"snapToken,omitempty"`
	RemainingSeconds int             `json:"remainingSeconds"`
	Status           string          `json:"status"`
	BackDisabled     bool            `json:"backDisabled"`
}

type PaymentMethod struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	EstSeconds int    `json:"estSeconds"`
	Enabled    bool   `json:"enabled"`
}

// ChargeRequest adalah body POST /payments/charge.
type ChargeRequest struct {
	BookingID      string `json:"bookingId"      validate:"required"`
	Method         string `json:"method"         validate:"required"`
	IdempotencyKey string `json:"idempotencyKey" validate:"required"`
}

// ChargeResponse adalah response POST /payments/charge.
type ChargeResponse struct {
	PaymentID   string `json:"paymentId"`
	Status      string `json:"status"`
	SnapToken   string `json:"snapToken,omitempty"`
	RedirectURL string `json:"redirectUrl,omitempty"`
	VANumber    string `json:"vaNumber,omitempty"`
	Method      string `json:"method,omitempty"`
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
