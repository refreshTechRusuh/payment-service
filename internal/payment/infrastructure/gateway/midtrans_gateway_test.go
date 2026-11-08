package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	midtrans "github.com/midtrans/midtrans-go"
	"github.com/midtrans/midtrans-go/snap"
	"github.com/refreshTechRusuh/payment-service/internal/payment/infrastructure/gateway"
	"github.com/refreshTechRusuh/payment-service/internal/payment/port/outbound"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── mockMidtransClient ────────────────────────────────────────────────────────

// mockMidtransClient mengimplementasikan midtrans.HttpClient untuk test tanpa jaringan.
type mockMidtransClient struct {
	response interface{} // akan di-marshal ke JSON lalu unmarshal ke result
	err      *midtrans.Error
}

func (m *mockMidtransClient) Call(_ string, _ string, _ *string, _ *midtrans.ConfigOptions, _ io.Reader, result interface{}) *midtrans.Error {
	if m.err != nil {
		return m.err
	}
	b, _ := json.Marshal(m.response)
	json.Unmarshal(b, result) //nolint:errcheck
	return nil
}

// newGateway membuat MidtransGateway dengan mock HTTP client.
func newGateway(t *testing.T, mock *mockMidtransClient) *gateway.MidtransGateway {
	t.Helper()
	c := snap.Client{
		Env:        midtrans.Sandbox,
		ServerKey:  "test-server-key",
		HttpClient: mock,
	}
	return gateway.NewMidtransGatewayWithSnapClient(c, discardLog())
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestCreateSnapToken_success(t *testing.T) {
	mock := &mockMidtransClient{
		response: snap.Response{
			Token:       "snap-token-abc123",
			RedirectURL: "https://app.sandbox.midtrans.com/snap/v2/vtweb/snap-token-abc123",
		},
	}
	gw := newGateway(t, mock)

	req := outbound.SnapRequest{
		OrderID:     "booking-1",
		GrossAmount: 500000,
		Email:       "user@example.com",
		Items: []outbound.SnapItem{
			{ID: "cat-1", Name: "VIP Ticket", Price: 500000, Quantity: 1},
		},
	}

	result, err := gw.CreateSnapToken(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "snap-token-abc123", result.SnapToken)
	assert.Contains(t, result.RedirectURL, "snap-token-abc123")
}

func TestCreateSnapToken_withExpiry(t *testing.T) {
	mock := &mockMidtransClient{
		response: snap.Response{
			Token:       "token-with-expiry",
			RedirectURL: "https://sandbox.midtrans.com/token-with-expiry",
		},
	}
	gw := newGateway(t, mock)

	req := outbound.SnapRequest{
		OrderID:     "booking-2",
		GrossAmount: 150000,
		Email:       "buyer@example.com",
		Items: []outbound.SnapItem{
			{ID: "cat-2", Name: "Festival", Price: 75000, Quantity: 2},
		},
		Expiry: &outbound.ExpiryDetails{
			StartTime: "2026-10-30 14:34:15 +0700",
			Unit:      "seconds",
			Duration:  600,
		},
	}

	result, err := gw.CreateSnapToken(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "token-with-expiry", result.SnapToken)
}

func TestCreateSnapToken_itemNameTruncation(t *testing.T) {
	// Nama > 50 rune harus dipotong ke 50 rune sebelum dikirim ke Midtrans
	mock := &mockMidtransClient{
		response: snap.Response{Token: "tok-truncate", RedirectURL: "https://mock.url"},
	}
	gw := newGateway(t, mock)

	longName := strings.Repeat("A", 60) // 60 karakter, harus dipotong ke 50
	req := outbound.SnapRequest{
		OrderID:     "booking-trunc",
		GrossAmount: 100000,
		Email:       "a@b.com",
		Items: []outbound.SnapItem{
			{ID: "cat-x", Name: longName, Price: 100000, Quantity: 1},
		},
	}

	// Test hanya memverifikasi tidak ada error dan token dikembalikan.
	// Truncation terjadi sebelum request dikirim — log menunjukkan nama yang dipotong.
	result, err := gw.CreateSnapToken(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "tok-truncate", result.SnapToken)
}

func TestCreateSnapToken_multipleItems(t *testing.T) {
	mock := &mockMidtransClient{
		response: snap.Response{Token: "tok-multi", RedirectURL: "https://mock.url"},
	}
	gw := newGateway(t, mock)

	req := outbound.SnapRequest{
		OrderID:     "booking-multi",
		GrossAmount: 800000,
		Email:       "buyer@test.com",
		Items: []outbound.SnapItem{
			{ID: "cat-1", Name: "VIP", Price: 500000, Quantity: 1},
			{ID: "cat-2", Name: "Festival", Price: 150000, Quantity: 2},
		},
	}

	result, err := gw.CreateSnapToken(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, result.SnapToken)
}

func TestCreateSnapToken_midtransError(t *testing.T) {
	mock := &mockMidtransClient{
		err: &midtrans.Error{
			StatusCode: 401,
			Message:    "Access denied due to unauthorized transaction",
			RawError:   nil,
		},
	}
	gw := newGateway(t, mock)

	req := outbound.SnapRequest{
		OrderID:     "booking-err",
		GrossAmount: 100000,
		Email:       "a@b.com",
	}

	_, err := gw.CreateSnapToken(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "midtrans gateway")
}

func TestCreateSnapToken_noExpiry(t *testing.T) {
	// Nil expiry — harus tidak set field expiry di request Midtrans
	mock := &mockMidtransClient{
		response: snap.Response{Token: "tok-noexp", RedirectURL: "https://mock.url"},
	}
	gw := newGateway(t, mock)

	req := outbound.SnapRequest{
		OrderID:     "booking-noexp",
		GrossAmount: 200000,
		Email:       "c@d.com",
		Expiry:      nil, // eksplisit nil
	}

	result, err := gw.CreateSnapToken(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "tok-noexp", result.SnapToken)
}

// TestNewMidtransGateway memverifikasi konstruktor production berjalan tanpa error.
// Tidak hit jaringan karena tidak memanggil CreateSnapToken.
func TestNewMidtransGateway_sandbox(t *testing.T) {
	gw := gateway.NewMidtransGateway("test-server-key", "sandbox", discardLog())
	assert.NotNil(t, gw)
}

func TestNewMidtransGateway_production(t *testing.T) {
	gw := gateway.NewMidtransGateway("test-server-key", "production", discardLog())
	assert.NotNil(t, gw)
}

// ── min helper coverage ───────────────────────────────────────────────────────

// TestCreateSnapToken_shortSnapToken: covers min(8, len) where len < 8 (a >= b case).
func TestCreateSnapToken_shortSnapToken(t *testing.T) {
	mock := &mockMidtransClient{
		response: snap.Response{
			Token:       "tok12",     // len=5 < 8 → min(8,5)=5 covers a>=b branch
			RedirectURL: "https://mock.url/tok12",
		},
	}
	gw := newGateway(t, mock)
	req := outbound.SnapRequest{
		OrderID: "booking-short", GrossAmount: 100000, Email: "a@b.com",
	}
	result, err := gw.CreateSnapToken(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "tok12", result.SnapToken)
}
