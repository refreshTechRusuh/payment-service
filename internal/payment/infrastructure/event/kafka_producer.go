package event

import (
	"context"
	"log/slog"

	"github.com/refreshTechRusuh/payment-service/pkg/kafka"
)

// IPublisher adalah interface minimal untuk kafka producer.
// *kafka.Producer memenuhi interface ini di production.
type IPublisher interface {
	Publish(ctx context.Context, topic, key string, payload any) error
}

type KafkaProducer struct {
	p   IPublisher
	log *slog.Logger
}

func NewKafkaProducer(p *kafka.Producer, log *slog.Logger) *KafkaProducer {
	return &KafkaProducer{p: p, log: log}
}

// NewKafkaProducerWithPublisher membuat KafkaProducer dengan custom publisher (untuk test).
func NewKafkaProducerWithPublisher(p IPublisher, log *slog.Logger) *KafkaProducer {
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
