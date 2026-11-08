package event_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/refreshTechRusuh/payment-service/internal/payment/infrastructure/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelError + 10}))
}

type mockPublisher struct {
	calls []struct {
		topic, key string
		payload    any
	}
	err error
}

func (m *mockPublisher) Publish(_ context.Context, topic, key string, payload any) error {
	m.calls = append(m.calls, struct {
		topic, key string
		payload    any
	}{topic, key, payload})
	return m.err
}

func TestKafkaProducer_PublishPaymentSucceeded(t *testing.T) {
	mp := &mockPublisher{}
	p := event.NewKafkaProducerWithPublisher(mp, discardLog())

	err := p.PublishPaymentSucceeded(context.Background(), "event-1", map[string]string{"paymentId": "pay-1"})
	require.NoError(t, err)
	require.Len(t, mp.calls, 1)
	assert.Equal(t, "payment.succeeded", mp.calls[0].topic)
	assert.Equal(t, "event-1", mp.calls[0].key)
}

func TestKafkaProducer_PublishPaymentExpired(t *testing.T) {
	mp := &mockPublisher{}
	p := event.NewKafkaProducerWithPublisher(mp, discardLog())

	err := p.PublishPaymentExpired(context.Background(), "event-1", map[string]string{"paymentId": "pay-1"})
	require.NoError(t, err)
	require.Len(t, mp.calls, 1)
	assert.Equal(t, "payment.expired", mp.calls[0].topic)
}

func TestKafkaProducer_PublishSlotReleased(t *testing.T) {
	mp := &mockPublisher{}
	p := event.NewKafkaProducerWithPublisher(mp, discardLog())

	err := p.PublishSlotReleased(context.Background(), "event-1", map[string]string{"reason": "PAID"})
	require.NoError(t, err)
	assert.Equal(t, "slot.released", mp.calls[0].topic)
}

func TestKafkaProducer_PublishEventSoldOut(t *testing.T) {
	mp := &mockPublisher{}
	p := event.NewKafkaProducerWithPublisher(mp, discardLog())

	err := p.PublishEventSoldOut(context.Background(), "event-1", map[string]string{"eventId": "event-1"})
	require.NoError(t, err)
	assert.Equal(t, "event.sold_out", mp.calls[0].topic)
}

func TestKafkaProducer_Error_propagated(t *testing.T) {
	mp := &mockPublisher{err: errors.New("kafka down")}
	p := event.NewKafkaProducerWithPublisher(mp, discardLog())

	err := p.PublishPaymentSucceeded(context.Background(), "event-1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kafka down")
}

func TestKafkaProducer_AllTopics(t *testing.T) {
	mp := &mockPublisher{}
	p := event.NewKafkaProducerWithPublisher(mp, discardLog())

	_ = p.PublishPaymentSucceeded(context.Background(), "e1", nil)
	_ = p.PublishPaymentExpired(context.Background(), "e1", nil)
	_ = p.PublishSlotReleased(context.Background(), "e1", nil)
	_ = p.PublishEventSoldOut(context.Background(), "e1", nil)

	require.Len(t, mp.calls, 4)
	var topics []string
	for _, c := range mp.calls {
		topics = append(topics, c.topic)
	}
	assert.ElementsMatch(t, []string{"payment.succeeded", "payment.expired", "slot.released", "event.sold_out"}, topics)
}

// ── NewKafkaProducer constructor ───────────────────────────────────────────────

func TestNewKafkaProducer_nilProducer(t *testing.T) {
	// NewKafkaProducer menerima nil — tidak connect ke Kafka.
	p := event.NewKafkaProducer(nil, discardLog())
	assert.NotNil(t, p)
}
