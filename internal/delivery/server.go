package delivery

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/refreshTechRusuh/payment-service/internal/config"
	payhttp "github.com/refreshTechRusuh/payment-service/internal/payment/infrastructure/http"
	"github.com/refreshTechRusuh/payment-service/internal/payment/application/usecase"
	"github.com/refreshTechRusuh/payment-service/pkg/kafka"
	"github.com/refreshTechRusuh/payment-service/pkg/middleware"
)

// New mewiring semua dependency payment-service dan mengembalikan:
//   - handler: HTTP handler yang sudah di-wrap logging middleware
//   - uc: PaymentUsecase yang mengekspos StartSweeper (untuk cmd/main.go)
//   - closeFn: fungsi cleanup
func New(_ context.Context, cfg *config.Config, pool *pgxpool.Pool, producer *kafka.Producer) (http.Handler, *usecase.PaymentUsecase, func()) {
	mux := http.NewServeMux()
	h, uc := payhttp.NewWiredHandlerWithUC(pool, cfg, producer)
	payhttp.RegisterRoutes(mux, h, cfg.JWT.AccessTokenSecret)

	// Wrap seluruh mux dengan request logger.
	logged := middleware.RequestLogger(slog.Default())(mux)

	closeFn := func() { pool.Close(); producer.Close() }
	return logged, uc, closeFn
}
