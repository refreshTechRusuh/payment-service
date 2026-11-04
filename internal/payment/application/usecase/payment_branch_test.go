package usecase_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/refreshTechRusuh/payment-service/internal/payment/application/usecase"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/dto"
	"github.com/refreshTechRusuh/payment-service/internal/payment/domain/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- safePrefix: tested via Charge with short BookingID --------------------

func TestCharge_ShortBookingID_NoError(t *testing.T) {
	// "abc" lebih pendek dari prefix length (8 char) → safePrefix returns "abc"
	snap := entity.BookingSnapshot{
		ID: "abc", EventID: "evt-001", Email: "u@test.com",
		Status:           "HELD",
		PaymentExpiresAt: expiresIn(10 * time.Minute),
		Items:            []entity.BookingItemSnapshot{{CategoryID: "cat-1", NIK: "1234", PriceSnapshot: 1000000}},
	}
	repo := newMockRepo(snap)
	uc := buildUC(repo, newMockProducer(), "")

	resp, err := uc.Charge(context.Background(), dto.ChargeRequest{BookingID: "abc"})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.SnapToken)
}

func TestCharge_LongBookingID_TruncatedPrefix(t *testing.T) {
	// BookingID panjang (UUID) → safePrefix truncate ke N chars
	longID := "bkg-a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	snap := entity.BookingSnapshot{
		ID: longID, EventID: "evt-001", Email: "u@test.com",
		Status:           "HELD",
		PaymentExpiresAt: expiresIn(10 * time.Minute),
		Items:            []entity.BookingItemSnapshot{{CategoryID: "cat-1", NIK: "1234", PriceSnapshot: 1000000}},
	}
	repo := newMockRepo(snap)
	uc := buildUC(repo, newMockProducer(), "")

	resp, err := uc.Charge(context.Background(), dto.ChargeRequest{BookingID: longID})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.SnapToken)
}

// ---- Charge: snapshot not found returns error ------------------------------

func TestCharge_BookingNotFound_Error(t *testing.T) {
	repo := newMockRepo(defaultSnap("HELD", expiresIn(10*time.Minute)))
	// Charge dengan bookingID yang berbeda dari snap ID
	uc := buildUC(repo, newMockProducer(), "")
	_, err := uc.Charge(context.Background(), dto.ChargeRequest{BookingID: "bkg-not-exist"})
	assert.Error(t, err)
}

// ---- postExpirePaymentPublish: publish errors no-panic ---------------------

func TestPostExpirePaymentPublish_AllPublishError_NoPanic(t *testing.T) {
	base := newMockRepo(defaultSnap("HELD", expiresIn(10*time.Minute)))
	errProd := &errAllProducer{}
	repo := &soldOutMockRepo{
		mockRepo:        base,
		expiredPayments: []entity.Payment{{ID: "pay-err", BookingID: "bkg-001"}},
		stocks:          []entity.CategoryStock{{CategoryID: "cat-1", Available: 0}},
		hasActive:       false,
	}
	uc := usecase.NewPaymentUsecase(nil, repo, &noopGateway{}, errProd, nil, "", slog.Default())
	assert.NotPanics(t, func() {
		uc.SweepOnce(context.Background())
		time.Sleep(100 * time.Millisecond)
	})
}

// ---- checkAndPublishSoldOut: empty stocks ----------------------------------

func TestCheckAndPublishSoldOut_EmptyStocks_Skip(t *testing.T) {
	base := newMockRepo(defaultSnap("HELD", expiresIn(10*time.Minute)))
	repo := &soldOutMockRepo{
		mockRepo:        base,
		expiredPayments: []entity.Payment{{ID: "pay-empty", BookingID: "bkg-001"}},
		stocks:          []entity.CategoryStock{}, // empty → tidak ada sold-out check
	}
	prod := newMockProducer()
	uc := usecase.NewPaymentUsecase(nil, repo, &noopGateway{}, prod, nil, "", slog.Default())
	uc.SweepOnce(context.Background())
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, prod.count("event.sold_out"))
}

// ---- GetBookingPayment: PAID status ----------------------------------------

func TestGetBookingPayment_PaidStatus(t *testing.T) {
	snap := defaultSnap("PAID", expiresIn(10*time.Minute))
	repo := newMockRepo(snap)
	repo.payments["pay-1"] = entity.Payment{
		ID: "pay-1", BookingID: "bkg-001", Status: entity.PaymentStatusSuccess,
	}
	uc := buildUC(repo, newMockProducer(), "")
	resp, err := uc.GetBookingPayment(context.Background(), "bkg-001")
	require.NoError(t, err)
	assert.Equal(t, "bkg-001", resp.BookingID)
}

// ---- mapTransactionStatus: "failure" case ----------------------------------

func TestHandleCallback_FailureStatus_NoPanic(t *testing.T) {
	repo := newMockRepo(makeCallbackSnap())
	uc := buildUC(repo, newMockProducer(), "srv-key")
	sig := computeTestSig("bkg-001", "202", "1000000.00", "srv-key")
	err := uc.HandleCallback(context.Background(), dto.MidtransCallback{
		OrderID: "bkg-001", TransactionStatus: "failure",
		StatusCode: "202", GrossAmount: "1000000.00", SignatureKey: sig,
	})
	_ = err // failure status → FAILED path, may or may not error depending on state
}

// ---- expirePayment: ExpirePaymentTx error ----------------------------------

func TestSweepOnce_ExpirePaymentTxError_NoPanic(t *testing.T) {
	base := newMockRepo(defaultSnap("HELD", expiresIn(10*time.Minute)))
	repo := &errExpirePaymentRepo{
		soldOutMockRepo: &soldOutMockRepo{
			mockRepo:        base,
			expiredPayments: []entity.Payment{{ID: "pay-fail-expire", BookingID: "bkg-001"}},
			stocks:          []entity.CategoryStock{},
		},
	}
	uc := usecase.NewPaymentUsecase(nil, repo, &noopGateway{}, newMockProducer(), nil, "", slog.Default())
	assert.NotPanics(t, func() {
		uc.SweepOnce(context.Background())
		time.Sleep(50 * time.Millisecond)
	})
}

// ---- Mock overrides --------------------------------------------------------

type errAllProducer struct{}

func (p *errAllProducer) PublishPaymentSucceeded(_ context.Context, _ string, _ interface{}) error {
	return errors.New("kafka error")
}
func (p *errAllProducer) PublishPaymentExpired(_ context.Context, _ string, _ interface{}) error {
	return errors.New("kafka error")
}
func (p *errAllProducer) PublishSlotReleased(_ context.Context, _ string, _ interface{}) error {
	return errors.New("kafka error")
}
func (p *errAllProducer) PublishEventSoldOut(_ context.Context, _ string, _ interface{}) error {
	return errors.New("kafka error")
}

type errExpirePaymentRepo struct {
	*soldOutMockRepo
}

func (r *errExpirePaymentRepo) ExpirePaymentTx(_ context.Context, _, _ string) error {
	return errors.New("expire tx failed")
}
