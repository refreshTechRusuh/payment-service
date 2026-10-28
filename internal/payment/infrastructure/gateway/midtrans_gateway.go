package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/midtrans/midtrans-go"
	"github.com/midtrans/midtrans-go/snap"
	"github.com/refreshTechRusuh/payment-service/internal/payment/port/outbound"
)

// MidtransGateway mengimplementasikan outbound.IPaymentGateway.
type MidtransGateway struct {
	snapClient snap.Client
	log        *slog.Logger
}

func NewMidtransGateway(serverKey, env string, log *slog.Logger) *MidtransGateway {
	envType := midtrans.Sandbox
	if env == "production" {
		envType = midtrans.Production
	}
	c := snap.Client{}
	c.New(serverKey, envType)
	return &MidtransGateway{snapClient: c, log: log}
}

func (g *MidtransGateway) CreateSnapToken(ctx context.Context, req outbound.SnapRequest) (outbound.SnapResult, error) {
	items := make([]midtrans.ItemDetails, 0, len(req.Items))
	for _, it := range req.Items {
		name := it.Name
		if len([]rune(name)) > 50 {
			name = string([]rune(name)[:50])
		}
		items = append(items, midtrans.ItemDetails{
			ID:    it.ID,
			Name:  name,
			Price: it.Price,
			Qty:   int32(it.Quantity),
		})
	}

	snapReq := &snap.Request{
		TransactionDetails: midtrans.TransactionDetails{
			OrderID:  req.OrderID,
			GrossAmt: req.GrossAmount,
		},
		CustomerDetail: &midtrans.CustomerDetails{Email: req.Email},
		Items:          &items,
	}

	start := time.Now()
	g.log.InfoContext(ctx, "midtrans snap request",
		slog.String("orderID", req.OrderID),
		slog.Int64("grossAmount", req.GrossAmount),
		slog.Int("itemCount", len(items)),
	)

	resp, err := g.snapClient.CreateTransaction(snapReq)
	if err != nil {
		g.log.ErrorContext(ctx, "midtrans snap failed",
			slog.String("orderID", req.OrderID),
			slog.String("duration", time.Since(start).String()),
			slog.String("error", err.Error()),
		)
		return outbound.SnapResult{}, fmt.Errorf("midtrans gateway: %w", err)
	}

	g.log.InfoContext(ctx, "midtrans snap response",
		slog.String("orderID", req.OrderID),
		slog.String("snapToken", resp.Token[:min(8, len(resp.Token))]+"..."),
		slog.String("duration", time.Since(start).String()),
	)
	return outbound.SnapResult{SnapToken: resp.Token, RedirectURL: resp.RedirectURL}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
