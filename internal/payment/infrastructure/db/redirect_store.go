package db

import (
	"context"
	"fmt"

	vk "github.com/valkey-io/valkey-go"
)

const redirectKeyPrefix = "payment:redirect:"

// RedirectStore mengimplementasikan outbound.IRedirectStore via Valkey/Redis.
type RedirectStore struct {
	client vk.Client
}

func NewRedirectStore(client vk.Client) *RedirectStore {
	return &RedirectStore{client: client}
}

// SetRedirectURL menyimpan redirectUrl dengan TTL dalam detik.
func (s *RedirectStore) SetRedirectURL(ctx context.Context, bookingID, redirectURL string, ttlSeconds int) error {
	key := redirectKeyPrefix + bookingID
	cmd := s.client.B().Set().Key(key).Value(redirectURL).ExSeconds(int64(ttlSeconds)).Build()
	if err := s.client.Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("redirect store set: %w", err)
	}
	return nil
}

// GetRedirectURL mengambil redirectUrl; returns "" jika tidak ada atau sudah expired.
func (s *RedirectStore) GetRedirectURL(ctx context.Context, bookingID string) (string, error) {
	key := redirectKeyPrefix + bookingID
	cmd := s.client.B().Get().Key(key).Build()
	val, err := s.client.Do(ctx, cmd).ToString()
	if err != nil {
		if vk.IsValkeyNil(err) {
			return "", nil
		}
		return "", fmt.Errorf("redirect store get: %w", err)
	}
	return val, nil
}
