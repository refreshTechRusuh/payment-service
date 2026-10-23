package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/refreshTechRusuh/payment-service/internal/config"
	"github.com/refreshTechRusuh/payment-service/internal/delivery"
	pkgdb "github.com/refreshTechRusuh/payment-service/pkg/db"
	"github.com/refreshTechRusuh/payment-service/pkg/kafka"
	"github.com/refreshTechRusuh/payment-service/pkg/middleware"
)

func main() {
	cfg, _ := config.Load()
	config.SetupLogger(cfg.Log)
	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pkgdb.NewPool(rootCtx, cfg.DB)
	if err != nil {
		slog.Error("db connect", "err", err)
		os.Exit(1)
	}

	producer := kafka.NewProducer(cfg.Kafka, slog.Default())
	mux, uc, closeDeps := delivery.New(rootCtx, cfg, pool, producer)
	defer closeDeps()

	// Start payment TTL sweeper (goroutine background)
	uc.StartSweeper(rootCtx)

	handler := middleware.Chain(mux, middleware.TransactionID, middleware.OptionalAuth(cfg.JWT.AccessTokenSecret))
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.App.Port),
		ReadHeaderTimeout: 5 * time.Second,
		Handler:           handler,
	}
	go func() {
		slog.Info("payment-service listening", "port", cfg.App.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	cancel() // stop sweeper
	shutCtx, cancelShut := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShut()
	srv.Shutdown(shutCtx) //nolint:errcheck
}
