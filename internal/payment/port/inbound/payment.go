package inbound

import (
	"context"

	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/dto"
)

type IPaymentUsecase interface {
	GetBookingPayment(ctx context.Context, bookingID string) (dto.BookingPaymentDetailResponse, error)
	Charge(ctx context.Context, req dto.ChargeRequest) (dto.ChargeResponse, error)
	HandleCallback(ctx context.Context, callback dto.MidtransCallback) error
}
