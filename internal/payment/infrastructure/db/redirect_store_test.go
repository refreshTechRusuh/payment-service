package db_test

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/refreshTechRusuh/payment-service/internal/payment/infrastructure/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	vk "github.com/valkey-io/valkey-go"
)

func newMiniredisValkeyClient(t *testing.T) vk.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := vk.NewClient(vk.ClientOption{
		InitAddress:  []string{mr.Addr()},
		DisableCache: true,
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

// ── NewRedirectStore ──────────────────────────────────────────────────────────

func TestNewRedirectStore_created(t *testing.T) {
	client := newMiniredisValkeyClient(t)
	store := db.NewRedirectStore(client)
	assert.NotNil(t, store)
}

// ── SetRedirectURL + GetRedirectURL ───────────────────────────────────────────

func TestRedirectStore_SetAndGet(t *testing.T) {
	client := newMiniredisValkeyClient(t)
	store := db.NewRedirectStore(client)

	err := store.SetRedirectURL(context.Background(), "booking-1",
		"https://app.sandbox.midtrans.com/snap/v2/vtweb/tok-abc", 600)
	require.NoError(t, err)

	url, err := store.GetRedirectURL(context.Background(), "booking-1")
	require.NoError(t, err)
	assert.Equal(t, "https://app.sandbox.midtrans.com/snap/v2/vtweb/tok-abc", url)
}

func TestRedirectStore_GetNonExistent_returnsEmpty(t *testing.T) {
	client := newMiniredisValkeyClient(t)
	store := db.NewRedirectStore(client)

	url, err := store.GetRedirectURL(context.Background(), "unknown-booking")
	require.NoError(t, err)
	assert.Equal(t, "", url, "key tidak ada → return empty string, bukan error")
}

func TestRedirectStore_Set_overwrite(t *testing.T) {
	client := newMiniredisValkeyClient(t)
	store := db.NewRedirectStore(client)

	require.NoError(t, store.SetRedirectURL(context.Background(), "booking-1", "url-v1", 600))
	require.NoError(t, store.SetRedirectURL(context.Background(), "booking-1", "url-v2", 600))

	url, err := store.GetRedirectURL(context.Background(), "booking-1")
	require.NoError(t, err)
	assert.Equal(t, "url-v2", url)
}

func TestRedirectStore_MultipleBookings(t *testing.T) {
	client := newMiniredisValkeyClient(t)
	store := db.NewRedirectStore(client)

	require.NoError(t, store.SetRedirectURL(context.Background(), "booking-1", "url-1", 600))
	require.NoError(t, store.SetRedirectURL(context.Background(), "booking-2", "url-2", 600))

	url1, _ := store.GetRedirectURL(context.Background(), "booking-1")
	url2, _ := store.GetRedirectURL(context.Background(), "booking-2")
	assert.Equal(t, "url-1", url1)
	assert.Equal(t, "url-2", url2)
}
