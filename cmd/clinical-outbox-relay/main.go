// Command clinical-outbox-relay publishes committed Clinical outbox events to
// the broker (Norma 5.3.11). It is the composition root of the relay and runs
// apart from the HTTP API; it exposes no business interface.
package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/messaging"
	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/persistence"
	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/system"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/usecase"
	"github.com/code-corhuila/dlc-clinical-api/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("clinical-outbox-relay stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadRelay(os.LookupEnv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := persistence.OpenMongo(ctx, persistence.MongoOptions{
		URI: cfg.MongoURI, Database: cfg.MongoDatabase, MaxPoolSize: cfg.MongoMaxPoolSize, Timeout: cfg.MongoTimeout,
	})
	if err != nil {
		return err
	}
	defer closeWithin(cfg.ShutdownTimeout, store.Close)
	publisher, err := messaging.NewPublisher(cfg.BrokerURL, cfg.Exchange, cfg.PublishTimeout)
	if err != nil {
		return err
	}
	defer publisher.Close()

	relay := &usecase.RelayService{Outbox: store, Publisher: publisher, Clock: system.Clock{}, Random: rand.Float64,
		BatchSize: cfg.BatchSize, MaxAttempts: cfg.MaxAttempts, BaseDelay: cfg.BaseDelay, MaxDelay: cfg.MaxDelay}
	logger.Info("clinical-outbox-relay started", "exchange", cfg.Exchange, "batchSize", cfg.BatchSize, "interval", cfg.Interval.String())

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		runOnce(ctx, logger, relay, cfg.RunTimeout)
		select {
		case <-ctx.Done():
			logger.Info("clinical-outbox-relay shutting down")
			return nil
		case <-ticker.C:
		}
	}
}

// runOnce executes one bounded run with its own correlation identifier.
func runOnce(ctx context.Context, base *slog.Logger, relay *usecase.RelayService, timeout time.Duration) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	logger := base.With("runId", system.UUIDs{}.NewID())
	report, err := relay.RunOnce(runCtx)
	if err != nil {
		logger.Error("relay run failed", "error", err.Error())
		return
	}
	if report.Published+report.Retried+report.Failed > 0 {
		logger.Info("relay run", "published", report.Published, "retried", report.Retried, "failed", report.Failed)
	}
}

func closeWithin(timeout time.Duration, closeFn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = closeFn(ctx)
}
