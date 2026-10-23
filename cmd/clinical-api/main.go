// Command clinical-api is the composition root: the only place that knows the
// concrete types and declares every runtime limit.
package main

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/in/httpapi"
	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/authority"
	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/persistence"
	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/system"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/usecase"
	"github.com/code-corhuila/dlc-clinical-api/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("clinical-api stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}

	key, err := loadPublicKey(cfg)
	if err != nil {
		return err
	}
	store, err := persistence.OpenMongo(context.Background(), persistence.MongoOptions{
		URI: cfg.MongoURI, Database: cfg.MongoDatabase, MaxPoolSize: cfg.MongoMaxPoolSize, Timeout: cfg.MongoTimeout,
	})
	if err != nil {
		return err
	}
	defer store.Close(context.Background())

	// Appointments and Patients are not deployed yet: their authority fails closed.
	records := &usecase.RecordService{
		Repo: store, Care: authority.Unavailable{}, Patients: authority.Unavailable{},
		Clock: system.Clock{}, IDs: system.UUIDs{},
	}
	entries := &usecase.EntryService{
		Records: store, Entries: store, Closures: store, Care: authority.Unavailable{}, Patients: authority.Unavailable{},
		Clock: system.Clock{}, IDs: system.UUIDs{},
	}
	treatments := &usecase.TreatmentService{
		Records: store, Entries: store, Treatments: store, Closures: store, Care: authority.Unavailable{}, Patients: authority.Unavailable{},
		Clock: system.Clock{}, IDs: system.UUIDs{},
	}
	procedures := &usecase.ProcedureService{
		Procedures: store, Treatments: store, Closures: store, Care: authority.Unavailable{}, Patients: authority.Unavailable{},
		Appointments: authority.Unavailable{}, Clock: system.Clock{}, IDs: system.UUIDs{},
	}
	routes := append(httpapi.RecordRoutes(records), httpapi.EntryRoutes(entries)...)
	routes = append(routes, httpapi.ProcedureRoutes(procedures)...)
	// The cross-owner write gate and Billing are release gates: declarations fail closed (503).
	careCompletions := &usecase.CareCompletionService{
		Records: store, Entries: store, Closures: store, Care: authority.Unavailable{}, Patients: authority.Unavailable{},
		Appointments: authority.Unavailable{}, Billing: authority.Unavailable{}, Gate: authority.Unavailable{},
		Clock: system.Clock{}, IDs: system.UUIDs{},
	}
	routes = append(routes, httpapi.CareCompletionRoutes(careCompletions)...)
	router := httpapi.NewRouter(httpapi.Options{
		Verifier: &httpapi.Verifier{Key: key, Issuer: cfg.JWTIssuer, Audience: cfg.JWTAudience, Now: time.Now},
		Logger:   logger,
		Routes:   append(routes, httpapi.TreatmentRoutes(treatments)...),
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           http.MaxBytesHandler(router, cfg.MaxBodyBytes),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("clinical-api listening", "addr", cfg.HTTPAddr, "env", cfg.AppEnv)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	logger.Info("clinical-api shutting down", "timeout", cfg.ShutdownTimeout.String())
	return srv.Shutdown(shutdownCtx)
}

// loadPublicKey reads the identity service's public key from exactly one source.
func loadPublicKey(cfg config.Config) (*rsa.PublicKey, error) {
	text := cfg.JWTPublicKey
	if cfg.JWTPublicKeyFile != "" {
		raw, err := os.ReadFile(cfg.JWTPublicKeyFile)
		if err != nil {
			return nil, fmt.Errorf("JWT_PUBLIC_KEY_FILE: %w", err)
		}
		text = string(raw)
	}
	return httpapi.ParseRSAPublicKey(text)
}
