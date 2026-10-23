package event

import (
	"context"
	"log/slog"

	"github.com/refreshTechRusuh/payment-service/pkg/kafka"
)

type KafkaProducer struct {
	p   *kafka.Producer
	log *slog.Logger
}

func NewKafkaProducer(p *kafka.Producer, log *slog.Logger) *KafkaProducer {
	return &KafkaProducer{p: p, log: log}
}

func (k *KafkaProducer) PublishPaymentSucceeded(ctx context.Context, eventID string, payload any) error {
	return k.p.Publish(ctx, kafka.TopicPaymentSucceeded, eventID, payload)
}

func (k *KafkaProducer) PublishPaymentExpired(ctx context.Context, eventID string, payload any) error {
	return k.p.Publish(ctx, kafka.TopicPaymentExpired, eventID, payload)
}

func (k *KafkaProducer) PublishSlotReleased(ctx context.Context, eventID string, payload any) error {
	return k.p.Publish(ctx, kafka.TopicSlotReleased, eventID, payload)
}

func (k *KafkaProducer) PublishEventSoldOut(ctx context.Context, eventID string, payload any) error {
	return k.p.Publish(ctx, kafka.TopicEventSoldOut, eventID, payload)
}
