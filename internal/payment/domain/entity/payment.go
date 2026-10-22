package entity

import "time"

type PaymentStatus string

const (
	PaymentStatusPending  PaymentStatus = "PENDING"
	PaymentStatusSuccess  PaymentStatus = "SUCCESS"
	PaymentStatusFailed   PaymentStatus = "FAILED"
	PaymentStatusExpired  PaymentStatus = "EXPIRED"
)

type Payment struct {
	ID          string
	BookingID   string
	Method      string
	Status      PaymentStatus
	ProviderRef string
	SnapToken   string
	CreatedAt   time.Time
}

// PaymentMethod adalah satu metode pembayaran dengan flag enabled dinamis.
type PaymentMethod struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	EstSeconds int    `json:"estSeconds"` // estimasi durasi settle
	Enabled    bool   `json:"enabled"`    // false bila remainingSeconds < estSeconds
}

// BookingSnapshot adalah data booking yang diambil dari DB untuk validasi payment.
type BookingSnapshot struct {
	ID               string
	EventID          string
	Email            string
	Status           string        // HELD|PAID|EXPIRED|CANCELLED
	PaymentExpiresAt *time.Time
	Items            []BookingItemSnapshot
}

type BookingItemSnapshot struct {
	CategoryID    string
	NIK           string
	PriceSnapshot int64
}
