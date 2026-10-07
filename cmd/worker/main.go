// Package main starts the Slotwise queue worker. It is a separate process so a
// stuck handler, a panic or a SIGKILL takes down one queue worker and not the
// HTTP server the businesses and their customers are using, and so the queue
// scales on its own.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ctru0009/slotwise/internal/adapters/email"
	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/internal/adapters/worker"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/clock"
)

// defaultBaseURL is the public address the mail's cancel links point at when
// the deployment does not name one.
const defaultBaseURL = "http://localhost:8080"

// shutdownGrace bounds how long a shutdown waits for the in-flight job before
// letting its lease expire into another worker's hands instead.
const shutdownGrace = 10 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}

	db, err := postgres.New(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("opening the database: %w", err)
	}
	defer db.Close()

	// The same secret as cmd/web: the cancel links the mail carries must
	// verify on the web server that serves them.
	signer, err := app.NewCancelSigner(cfg.cancelSecret)
	if err != nil {
		return fmt.Errorf("building the cancel signer: %w", err)
	}

	sender := cfg.sender
	if sender == nil {
		sender = email.NewLogSender(slog.Default())
	}
	jobs := app.NewJobs(db, signer, sender, cfg.baseURL, clock.System{})

	wk, err := worker.New(cfg.worker, worker.Deps{
		Jobs:    db,
		Handler: jobs.Handle,
		Clock:   clock.System{},
		Logger:  slog.Default(),
	})
	if err != nil {
		return fmt.Errorf("building the worker: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- wk.Run(ctx) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	select {
	case err := <-done:
		return err
	case <-time.After(shutdownGrace):
		return fmt.Errorf("the in-flight job did not stop within %s; its lease will hand it to another worker", shutdownGrace)
	}
}

// config is the deployment's environment plus the sender seam tests replace: a
// nil sender means the logging one.
type config struct {
	databaseURL  string
	baseURL      string
	cancelSecret string
	sender       app.Sender
	worker       worker.Config
}

// loadConfig reads the environment. getenv is a parameter so tests can supply a
// map instead of the process environment.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		databaseURL:  getenv("DATABASE_URL"),
		baseURL:      getenv("SLOTWISE_BASE_URL"),
		cancelSecret: getenv("SLOTWISE_CANCEL_SECRET"),
	}
	if cfg.databaseURL == "" {
		return config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.baseURL == "" {
		cfg.baseURL = defaultBaseURL
	}
	if cfg.cancelSecret == "" {
		return config{}, errors.New("SLOTWISE_CANCEL_SECRET is required")
	}
	// The queue policy is the same for every worker process; only the lease
	// name — which must not be — differs.
	cfg.worker = worker.DefaultConfig(workerID())
	return cfg, nil
}

// workerID names this process on every lease it takes: "<hostname>-<pid>". A
// restart gets a new id, and the leases the old process left behind are
// reclaimed by expiry rather than waiting for the name to come back.
func workerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}
