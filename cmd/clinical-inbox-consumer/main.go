// Command clinical-inbox-consumer applies the closure outcome facts of other
// owners (AppointmentCompleted, EncounterChargesCompleted) through the inbox.
// It is a composition root separate from the HTTP API and exposes no business
// interface.
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
	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/system"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/usecase"
	"github.com/code-corhuila/dlc-clinical-api/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("clinical-inbox-consumer stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadConsumer(os.LookupEnv)
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
		Timeout: cfg.HandlerTimeout, RedeliveryDelay: cfg.RedeliveryDelay,
		Bindings: []messaging.Binding{
			{Exchange: "appointments.events", RoutingKey: "AppointmentCompleted"},
			{Exchange: "billing.events", RoutingKey: "EncounterChargesCompleted"},
		},
	})
	if err != nil {
		return err
	}
	defer subscriber.Close()

	inbox := &usecase.InboxService{Inbox: store, Encounters: store, Clock: system.Clock{}}
	logger.Info("clinical-inbox-consumer started", "queue", cfg.Queue, "deliveryLimit", cfg.DeliveryLimit)
	return subscriber.Run(ctx, func(ctx context.Context, e in.IncomingEvent) error {
		start := time.Now()
		log := logger.With("correlationId", e.CorrelationID, "eventId", e.EventID, "eventType", e.EventType)
		if err := inbox.Consume(ctx, e); err != nil {
			log.Warn("event not applied; it will be redelivered", "error", err.Error())
			return err
		}
		log.Info("event consumed", "durationMs", time.Since(start).Milliseconds())
		return nil
	})
}
