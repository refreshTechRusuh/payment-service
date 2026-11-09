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
	pkgvalkey "github.com/refreshTechRusuh/payment-service/pkg/valkey"
)

// New mewiring semua dependency payment-service dan mengembalikan:
//   - handler: HTTP handler yang sudah di-wrap logging middleware
//   - uc: PaymentUsecase yang mengekspos StartSweeper (untuk cmd/main.go)
//   - closeFn: fungsi cleanup
func New(_ context.Context, cfg *config.Config, pool *pgxpool.Pool, producer *kafka.Producer) (http.Handler, *usecase.PaymentUsecase, func()) {
	mux := http.NewServeMux()

	// Init Valkey client untuk menyimpan redirectUrl (non-fatal jika gagal)
	valkeyClient, err := pkgvalkey.NewClient(cfg.Valkey)
	if err != nil {
		slog.Warn("valkey: tidak bisa konek, redirectUrl tidak akan disimpan", slog.String("err", err.Error()))
		valkeyClient = nil
	}

	h, uc := payhttp.NewWiredHandlerWithUC(pool, cfg, producer, valkeyClient)
	payhttp.RegisterRoutes(mux, h, cfg)

	// Wrap seluruh mux dengan request logger.
	logged := middleware.RequestLogger(slog.Default())(mux)
	traced := middleware.OtelMiddleware("payment-service")(logged)
	corsed := middleware.CORSMiddleware("*")(traced)

	closeFn := func() {
		pool.Close()
		producer.Close() // #nosec G104 -- shutdown, error irrelevant
		if valkeyClient != nil {
			valkeyClient.Close()
		}
	}
	return corsed, uc, closeFn
}
