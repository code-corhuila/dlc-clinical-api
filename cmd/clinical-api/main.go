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
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
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

	keys, err := signingKeys(cfg)
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

	own, err := owners(cfg)
	if err != nil {
		return err
	}
	care, patients := own.care, own.patients
	// Writes of patient clinical data run under the Appointments patient gate (ADR-017).
	guard := &usecase.GuardedWrites{Intents: store, Admissions: store, Gate: own.gate, Care: care, Patients: patients,
		Clock: system.Clock{}, IDs: system.UUIDs{}}
	records := &usecase.RecordService{
		Repo: store, Care: care, Patients: patients, Guard: guard,
		Clock: system.Clock{}, IDs: system.UUIDs{},
	}
	entries := &usecase.EntryService{
		Records: store, Entries: store, Closures: store, Care: care, Patients: patients, Guard: guard,
		Clock: system.Clock{}, IDs: system.UUIDs{},
	}
	treatments := &usecase.TreatmentService{
		Records: store, Entries: store, Treatments: store, Closures: store, Care: care, Patients: patients, Guard: guard,
		Clock: system.Clock{}, IDs: system.UUIDs{},
	}
	procedures := &usecase.ProcedureService{
		Procedures: store, Treatments: store, Closures: store, Care: care, Patients: patients,
		Appointments: own.appointments, Consultations: store, Guard: guard, Clock: system.Clock{}, IDs: system.UUIDs{},
	}
	routes := append(httpapi.RecordRoutes(records), httpapi.EntryRoutes(entries)...)
	routes = append(routes, httpapi.ProcedureRoutes(procedures)...)
	careCompletions := &usecase.CareCompletionService{
		Records: store, Entries: store, Closures: store, Care: care, Patients: patients,
		Appointments: own.appointments, Billing: own.billing, Intents: store, Admissions: store,
		Clock: system.Clock{}, IDs: system.UUIDs{},
	}
	routes = append(routes, httpapi.CareCompletionRoutes(careCompletions)...)
	// HU-CLN-003: read-only dashboard over the analytics projections.
	analytics := &usecase.DashboardService{Analytics: store, Clock: system.Clock{}, Location: cfg.ClinicTimezone}
	routes = append(routes, httpapi.AnalyticsRoutes(analytics)...)
	router := httpapi.NewRouter(httpapi.Options{
		Verifier: &httpapi.Verifier{Keys: keys, Issuer: cfg.JWTIssuer, Audience: cfg.JWTAudience, Now: time.Now, Sessions: own.sessions},
		Logger:   logger,
		Ready:    store.Ping,
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

// signingKeys reads the Auth JWKS (rotation by kid) or, in dev, one configured key.
func signingKeys(cfg config.Config) (httpapi.KeySet, error) {
	if cfg.JWTJWKSURL != "" {
		return &httpapi.JWKS{URL: cfg.JWTJWKSURL, Client: &http.Client{Timeout: cfg.InternalTimeout},
			MinRefresh: cfg.JWKSMinRefresh, Now: time.Now}, nil
	}
	key, err := loadPublicKey(cfg)
	if err != nil {
		return nil, err
	}
	return httpapi.StaticKey{Key: key}, nil
}

// loadPublicKey reads the one configured public key.
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

// ownerSet holds the private owner contracts. Every owner without a configured
// URL answers out.ErrUnavailable, so its operations fail closed (503).
type ownerSet struct {
	care         out.CareAuthority
	appointments out.AppointmentAuthority
	gate         out.PatientGate
	patients     out.PatientDirectory
	billing      out.BillingAuthority
	sessions     httpapi.SessionValidator
}

func owners(cfg config.Config) (ownerSet, error) {
	set := ownerSet{care: authority.Unavailable{}, appointments: authority.Unavailable{}, gate: authority.Unavailable{}, patients: authority.Unavailable{}, billing: authority.Unavailable{}}
	if cfg.AuthURL == "" && cfg.PatientsURL == "" && cfg.AppointmentsURL == "" && cfg.BillingURL == "" {
		return set, nil
	}
	token, err := authority.ServiceToken(cfg.ServiceToken, cfg.ServiceTokenFile)
	if err != nil {
		return ownerSet{}, err
	}
	client := func(base string) *authority.Client { return authority.NewClient(base, token, cfg.InternalTimeout) }
	if cfg.AppointmentsURL != "" {
		apts := authority.Appointments{Client: client(cfg.AppointmentsURL)}
		set.care, set.appointments = apts, apts
		set.gate = authority.PatientGates{Client: apts.Client}
	}
	if cfg.PatientsURL != "" {
		set.patients = authority.Patients{Client: client(cfg.PatientsURL)}
	}
	if cfg.BillingURL != "" {
		set.billing = authority.Billing{Client: client(cfg.BillingURL)}
	}
	if cfg.AuthURL != "" {
		set.sessions = authority.Sessions{Client: client(cfg.AuthURL)}
	}
	return set, nil
}
