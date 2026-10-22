-- payment-service owns payment and payment_callback_log.
-- booking/booking_item are read by payment-service (shared DB, read-only access).
CREATE TABLE IF NOT EXISTS payment (
    id           UUID PRIMARY KEY,
    booking_id   UUID NOT NULL,
    method       TEXT,
    status       TEXT NOT NULL DEFAULT 'PENDING',
    provider_ref TEXT,
    snap_token   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT payment_status_check CHECK (status IN ('PENDING','SUCCESS','FAILED','EXPIRED'))
);
CREATE TABLE IF NOT EXISTS payment_callback_log (
    idempotency_key TEXT PRIMARY KEY,
    booking_id      UUID,
    result          TEXT,
    processed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_payment_booking_id ON payment(booking_id);
