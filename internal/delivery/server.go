package delivery

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/refreshTechRusuh/payment-service/internal/config"
	payhttp "github.com/refreshTechRusuh/payment-service/internal/payment/infrastructure/http"
	"github.com/refreshTechRusuh/payment-service/internal/payment/application/usecase"
	"github.com/refreshTechRusuh/payment-service/pkg/kafka"
)

// New mewiring semua dependency payment-service dan mengembalikan:
//   - mux: HTTP router siap digunakan
//   - uc: PaymentUsecase yang mengekspos StartSweeper (untuk cmd/main.go)
//   - closeFn: fungsi cleanup
func New(_ context.Context, cfg *config.Config, pool *pgxpool.Pool, producer *kafka.Producer) (*http.ServeMux, *usecase.PaymentUsecase, func()) {
	mux := http.NewServeMux()
	h, uc := payhttp.NewWiredHandlerWithUC(pool, cfg, producer)
	payhttp.RegisterRoutes(mux, h, cfg.JWT.AccessTokenSecret)
	closeFn := func() { pool.Close(); producer.Close() }
	return mux, uc, closeFn
}
