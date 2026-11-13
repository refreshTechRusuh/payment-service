package usecase_test

import (
	"context"
	"testing"
	"time"

	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/dto"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Callback untuk booking yang sudah ditutup (CANCELLED/EXPIRED) tidak boleh menerbitkan
// e-tiket / email / slot.released: stoknya sudah dilepas, menerbitkan tiket = oversell.
func TestHandleCallback_ShouldSkipEvents_WhenBookingClosed(t *testing.T) {
	cases := []struct{ bookingStatus, txStatus string }{
		{"CANCELLED", "settlement"},
		{"CANCELLED", "expire"},
		{"EXPIRED", "settlement"},
	}
	for _, tc := range cases {
		t.Run(tc.bookingStatus+"_"+tc.txStatus, func(t *testing.T) {
			repo := newMockRepo(defaultSnap(tc.bookingStatus, expiresIn(10*time.Minute)))
			repo.payments["pay-bkg-001"] = entity.Payment{
				ID: "pay-bkg-001", BookingID: "bkg-001", Status: entity.PaymentStatusPending,
			}
			prod := newMockProducer()
			uc := buildUC(repo, prod, "srv-key")

			err := uc.HandleCallback(context.Background(), dto.MidtransCallback{
				OrderID: "bkg-001", TransactionStatus: tc.txStatus,
				StatusCode: "200", GrossAmount: "1000000.00",
				SignatureKey: computeTestSig("bkg-001", "200", "1000000.00", "srv-key"),
			})

			require.NoError(t, err)
			time.Sleep(50 * time.Millisecond)
			for _, topic := range []string{"payment.succeeded", "payment.expired", "slot.released"} {
				assert.Equal(t, 0, prod.count(topic), topic)
			}
		})
	}
}
