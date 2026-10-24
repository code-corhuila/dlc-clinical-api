// Command clinical-analytics-consumer keeps the HU-CLN-003 analytics
// projections from Appointments, Billing and Clinical events (ADR-006). It
// has its own queue, so analytics never delays closure outcomes, and it
// exposes no business interface.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/in/messaging"
	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/persistence"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/usecase"
	"github.com/code-corhuila/dlc-clinical-api/internal/config"
)

// sources are the approved analytics facts (event-catalog.md, HU-CLN-003).
var sources = []messaging.Binding{
	{Exchange: "appointments.events", RoutingKey: "AppointmentScheduled"},
	{Exchange: "appointments.events", RoutingKey: "AppointmentRescheduled"},
	{Exchange: "appointments.events", RoutingKey: "AppointmentCancelled"},
	{Exchange: "appointments.events", RoutingKey: "AppointmentNoShow"},
	{Exchange: "appointments.events", RoutingKey: "AppointmentCompleted"},
	{Exchange: "billing.events", RoutingKey: "PaymentRecorded"},
	{Exchange: "clinical.events", RoutingKey: "ClinicalCareCompleted"},
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("clinical-analytics-consumer stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadAnalyticsConsumer(os.LookupEnv)
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
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = store.Close(closeCtx)
	}()
	subscriber, err := messaging.NewSubscriber(messaging.Options{
		URL: cfg.BrokerURL, Queue: cfg.Queue, DeliveryLimit: cfg.DeliveryLimit, Prefetch: cfg.Prefetch,
		Timeout: cfg.HandlerTimeout, RedeliveryDelay: cfg.RedeliveryDelay, Bindings: sources,
	})
	if err != nil {
		return err
	}
	defer subscriber.Close()

	projector := &usecase.AnalyticsProjector{Projections: store, Location: cfg.ClinicTimezone}
	logger.Info("clinical-analytics-consumer started", "queue", cfg.Queue, "timezone", cfg.ClinicTimezone.String())
	return subscriber.Run(ctx, func(ctx context.Context, e in.IncomingEvent) error {
		start := time.Now()
		log := logger.With("correlationId", e.CorrelationID, "eventId", e.EventID, "eventType", e.EventType)
		if err := projector.Consume(ctx, e); err != nil {
			log.Warn("event not projected; it will be redelivered", "error", err.Error())
			return err
		}
		log.Info("event projected", "durationMs", time.Since(start).Milliseconds())
		return nil
	})
}
