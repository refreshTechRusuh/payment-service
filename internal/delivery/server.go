package delivery

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/refreshTechRusuh/payment-service/internal/config"
	payhttp "github.com/refreshTechRusuh/payment-service/internal/payment/infrastructure/http"
	"github.com/refreshTechRusuh/payment-service/pkg/kafka"
)

func New(_ context.Context, cfg *config.Config, pool *pgxpool.Pool, producer *kafka.Producer) (*http.ServeMux, func(), error) {
	mux := http.NewServeMux()
	h := payhttp.NewWiredHandler(pool, cfg, producer)
	payhttp.RegisterRoutes(mux, h, cfg.JWT.AccessTokenSecret)
	closeFn := func() { pool.Close(); producer.Close() }
	return mux, closeFn, nil
}
