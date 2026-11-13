package gateway

import (
	"context"
	"encoding/json"
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

// NewMidtransGatewayWithSnapClient digunakan untuk testing — menerima snap.Client yang sudah dikonfigurasi
// (termasuk custom HttpClient untuk mock HTTP response tanpa koneksi jaringan).
func NewMidtransGatewayWithSnapClient(c snap.Client, log *slog.Logger) *MidtransGateway {
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
			Qty:   int32(it.Quantity), // #nosec G115 -- ticket quantity bounded in DB (< 10000)
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

	// Inject expiry jika ada — sinkronkan dengan payment_expires_at di DB.
	if req.Expiry != nil {
		snapReq.Expiry = &snap.ExpiryDetails{
			StartTime: req.Expiry.StartTime,
			Unit:      req.Expiry.Unit,
			Duration:  int64(req.Expiry.Duration),
		}
	}

	requestPayload, err := snapRequestPayload(snapReq)
	if err != nil {
		return outbound.SnapResult{}, fmt.Errorf("midtrans gateway: encode snap request: %w", err)
	}

	start := time.Now()
	g.log.InfoContext(ctx, "midtrans snap request",
		slog.String("method", "POST"),
		slog.String("endpoint", "/snap/v1/transactions"),
		slog.String("orderID", req.OrderID),
		slog.Int64("grossAmount", req.GrossAmount),
		slog.Int("itemCount", len(items)),
		slog.Any("requestPayload", payloadForLog(requestPayload)),
	)

	resp, midtransErr := g.snapClient.CreateTransactionWithMap(&requestPayload)
	if midtransErr != nil {
		g.log.ErrorContext(ctx, "midtrans snap failed",
			slog.String("orderID", req.OrderID),
			slog.String("duration", time.Since(start).String()),
			slog.String("error", midtransErr.Error()),
			slog.Any("requestPayload", payloadForLog(requestPayload)),
			slog.Any("responsePayload", midtransErrorPayload(midtransErr)),
		)
		return outbound.SnapResult{}, fmt.Errorf("midtrans gateway: %w", midtransErr)
	}
	token, _ := resp["token"].(string)
	redirectURL, _ := resp["redirect_url"].(string)

	g.log.InfoContext(ctx, "midtrans snap response",
		slog.String("orderID", req.OrderID),
		slog.String("duration", time.Since(start).String()),
		slog.Any("responsePayload", payloadForLog(resp)),
	)
	return outbound.SnapResult{SnapToken: token, RedirectURL: redirectURL}, nil
}

// payloadForLog converts SDK payload values to JSON-shaped data for structured logs.
// The original value passed to the Midtrans SDK is never modified.
func payloadForLog(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "[payload unavailable]"
	}

	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return "[payload unavailable]"
	}
	return decoded
}

func midtransErrorPayload(err *midtrans.Error) map[string]any {
	payload := map[string]any{
		"statusCode": err.StatusCode,
		"message":    err.Message,
	}
	if err.RawError != nil {
		payload["transportError"] = err.RawError.Error()
	}
	if response := err.RawApiResponse; response != nil {
		payload["httpStatus"] = response.Status
		payload["httpStatusCode"] = response.StatusCode
		if len(response.RawBody) > 0 {
			var body any
			if json.Unmarshal(response.RawBody, &body) == nil {
				payload["body"] = body
			} else {
				payload["body"] = "[non-JSON response omitted]"
			}
		}
	}
	return payload
}

func snapRequestPayload(req *snap.Request) (snap.RequestParamWithMap, error) {
	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	payloadFields := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &payloadFields); err != nil {
		return nil, err
	}

	if req.Expiry != nil {
		customExpiry, err := json.Marshal(struct {
			OrderTime      string `json:"order_time"`
			ExpiryDuration int    `json:"expiry_duration"`
			Unit           string `json:"unit"`
		}{
			OrderTime:      req.Expiry.StartTime,
			ExpiryDuration: int(req.Expiry.Duration),
			Unit:           req.Expiry.Unit,
		})
		if err != nil {
			return nil, err
		}
		payloadFields["custom_expiry"] = customExpiry
	}

	payload := snap.RequestParamWithMap{}
	for key, value := range payloadFields {
		payload[key] = value
	}
	return payload, nil
}
