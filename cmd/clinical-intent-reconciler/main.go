// Command clinical-intent-reconciler settles Clinical write intents whose
// outcome is unknown after a lost owner answer or a crash (ADR-017 §2.4,
// §6.6). It is a composition root apart from the HTTP API and exposes no
// business interface.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/authority"
	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/persistence"
	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/system"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/usecase"
	"github.com/code-corhuila/dlc-clinical-api/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("clinical-intent-reconciler stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadReconciler(os.LookupEnv)
	if err != nil {
		return err
	}
	token, err := authority.ServiceToken(cfg.ServiceToken, cfg.ServiceTokenFile)
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

	reconciler := &usecase.ReconcileService{
		Intents: store, Admissions: store,
		Gate:    authority.PatientGates{Client: authority.NewClient(cfg.AppointmentsURL, token, cfg.InternalTimeout)},
		Billing: authority.Billing{Client: authority.NewClient(cfg.BillingURL, token, cfg.InternalTimeout)},
		Clock:   system.Clock{}, MinAge: cfg.MinAge, BatchSize: cfg.BatchSize,
	}
	logger.Info("clinical-intent-reconciler started", "interval", cfg.Interval.String(), "minAge", cfg.MinAge.String(), "batchSize", cfg.BatchSize)

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		runOnce(ctx, logger, reconciler, cfg.RunTimeout)
		select {
		case <-ctx.Done():
			logger.Info("clinical-intent-reconciler shutting down")
			return nil
		case <-ticker.C:
		}
	}
}

// runOnce executes one bounded run with its own correlation identifier.
func runOnce(ctx context.Context, base *slog.Logger, reconciler *usecase.ReconcileService, timeout time.Duration) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	logger := base.With("runId", system.UUIDs{}.NewID())
	report, err := reconciler.RunOnce(runCtx)
	if err != nil {
		logger.Error("reconcile run failed", "error", err.Error())
		return
	}
	if report.Completed+report.Aborted+report.Skipped > 0 {
		logger.Info("reconcile run", "completed", report.Completed, "aborted", report.Aborted, "skipped", report.Skipped)
	}
}

func closeWithin(timeout time.Duration, closeFn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = closeFn(ctx)
}
