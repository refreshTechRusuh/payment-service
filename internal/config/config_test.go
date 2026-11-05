package config_test

import (
	"testing"

	"github.com/refreshTechRusuh/payment-service/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("PAYMENT_TOKEN_SECRET", "test-payment-secret")
}

func TestLoad_defaults(t *testing.T) {
	setRequired(t)

	cfg, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "local", cfg.App.Env)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, "localhost", cfg.DB.Host)
	assert.Equal(t, 5432, cfg.DB.Port)
	assert.Equal(t, "postgres", cfg.DB.User)
	assert.Equal(t, "sandbox", cfg.Midtrans.Env)
	assert.Equal(t, "", cfg.Midtrans.ServerKey)
	assert.Equal(t, "test-payment-secret", cfg.JWT.PaymentTokenSecret)
}

func TestLoad_customMidtrans(t *testing.T) {
	setRequired(t)
	t.Setenv("MIDTRANS_ENV", "production")
	t.Setenv("MIDTRANS_SERVER_KEY", "server-key-prod-123")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "production", cfg.Midtrans.Env)
	assert.Equal(t, "server-key-prod-123", cfg.Midtrans.ServerKey)
}

func TestLoad_customDB(t *testing.T) {
	setRequired(t)
	t.Setenv("DB_HOST", "pg-host")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("DB_USER", "pgadmin")
	t.Setenv("DB_NAME", "payment_db")
	t.Setenv("DB_SSL_MODE", "require")
	t.Setenv("DB_MAX_OPEN_CONNS", "30")
	t.Setenv("DB_CONN_TIMEOUT_SECONDS", "8")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "pg-host", cfg.DB.Host)
	assert.Equal(t, 5433, cfg.DB.Port)
	assert.Equal(t, "pgadmin", cfg.DB.User)
	assert.Equal(t, "payment_db", cfg.DB.Name)
	assert.Equal(t, "require", cfg.DB.SSLMode)
	assert.Equal(t, 30, cfg.DB.MaxOpenConns)
	assert.Equal(t, 8, cfg.DB.ConnTimeoutSecs)
}

func TestLoad_customPort(t *testing.T) {
	setRequired(t)
	t.Setenv("PORT", "8083")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 8083, cfg.App.Port)
}

func TestLoad_boolEnvs(t *testing.T) {
	setRequired(t)
	t.Setenv("VALKEY_TLS", "true")
	t.Setenv("KAFKA_TLS", "1")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.True(t, cfg.Valkey.TLS)
	assert.True(t, cfg.Kafka.TLS)
}

func TestLoad_DSN(t *testing.T) {
	setRequired(t)
	t.Setenv("DB_HOST", "myhost")
	t.Setenv("DB_PORT", "5432")
	t.Setenv("DB_USER", "user1")
	t.Setenv("DB_PASSWORD", "pwd1")
	t.Setenv("DB_NAME", "paymentdb")
	t.Setenv("DB_SSL_MODE", "disable")
	t.Setenv("DB_CONN_TIMEOUT_SECONDS", "5")

	cfg, err := config.Load()
	require.NoError(t, err)
	dsn := cfg.DB.DSN()
	assert.Contains(t, dsn, "host=myhost")
	assert.Contains(t, dsn, "user=user1")
	assert.Contains(t, dsn, "dbname=paymentdb")
	assert.Contains(t, dsn, "sslmode=disable")
}

func TestLoad_bookingServiceURL(t *testing.T) {
	setRequired(t)
	// payment-service tidak punya BookingSvc config field
	// Verify default SSE (yang ada)
	t.Setenv("SSE_HEARTBEAT_SECONDS", "45")
	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 45, cfg.SSE.HeartbeatSeconds)
}

func TestSetupLogger_allLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error", "unknown"} {
		t.Run(level, func(t *testing.T) {
			config.SetupLogger(config.LogConfig{Level: level, Format: "json"})
		})
	}
}

func TestSetupLogger_textFormat(t *testing.T) {
	config.SetupLogger(config.LogConfig{Level: "info", Format: "text"})
}
