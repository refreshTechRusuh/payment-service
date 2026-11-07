package delivery_test

import (
	"context"
	"testing"

	"github.com/refreshTechRusuh/payment-service/internal/config"
	"github.com/refreshTechRusuh/payment-service/internal/delivery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDelivery_New_nilDeps(t *testing.T) {
	// delivery.New menerima nil pool/producer.
	// closeFn TIDAK dipanggil karena nil.Close() akan panic.
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.JWT.PaymentTokenSecret = "test-payment"
	cfg.Midtrans.Env = "sandbox"
	cfg.Midtrans.ServerKey = "test-server-key"
	// Valkey kosong → pkgvalkey.NewClient akan return error (graceful warning)

	handler, uc, closeFn := delivery.New(ctx, cfg, nil, nil)
	require.NotNil(t, handler)
	assert.NotNil(t, uc)
	assert.NotNil(t, closeFn)
	// Tidak memanggil closeFn() karena pool/producer = nil → panic
}
