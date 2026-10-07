package worker

import (
	"errors"
	"fmt"
	"time"
)

// Worker policy defaults. The lease holds a job for four handler runs, so a
// handler that overruns its budget still leaves room for the guarded
// transition write before another worker may reclaim the row.
const (
	defaultLease          = 2 * time.Minute
	defaultHandlerTimeout = 30 * time.Second
	defaultPollInterval   = time.Second
	defaultMaxAttempts    = 8
	defaultBackoffBase    = 30 * time.Second
	defaultBackoffCap     = 30 * time.Minute
	// transitionTimeout bounds the guarded write that records a job's outcome,
	// so shutdown cannot wait forever for a database that stopped answering.
	transitionTimeout = 5 * time.Second
)

// Config is one worker process's queue policy: how long a claim is leased, how
// long a handler may run, how often an empty queue is polled, and how a failed
// job is retried.
type Config struct {
	// WorkerID names this process on every lease it takes. A restarted process
	// gets a new id, so the leases its predecessor left behind are reclaimed
	// by expiry instead of waiting for the old name to come back.
	WorkerID       string
	Lease          time.Duration
	HandlerTimeout time.Duration
	PollInterval   time.Duration
	MaxAttempts    int
	BackoffBase    time.Duration
	BackoffCap     time.Duration
}

// DefaultConfig returns the production policy for the process named workerID.
func DefaultConfig(workerID string) Config {
	return Config{
		WorkerID:       workerID,
		Lease:          defaultLease,
		HandlerTimeout: defaultHandlerTimeout,
		PollInterval:   defaultPollInterval,
		MaxAttempts:    defaultMaxAttempts,
		BackoffBase:    defaultBackoffBase,
		BackoffCap:     defaultBackoffCap,
	}
}

// validate refuses settings that would silently misbehave at runtime: a lease
// that expires before its handler is done lets another worker start the same
// job mid-run, and the SQL claim takes whole seconds.
func (c Config) validate() error {
	if c.WorkerID == "" {
		return errors.New("worker id is required")
	}
	if c.Lease < time.Second || c.Lease%time.Second != 0 {
		return fmt.Errorf("lease %s must be a whole number of seconds of at least one second", c.Lease)
	}
	if c.HandlerTimeout <= 0 {
		return fmt.Errorf("handler timeout %s must be positive", c.HandlerTimeout)
	}
	if c.Lease < 2*c.HandlerTimeout {
		return fmt.Errorf("lease %s must be at least twice the handler timeout %s", c.Lease, c.HandlerTimeout)
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("poll interval %s must be positive", c.PollInterval)
	}
	if c.MaxAttempts < 1 {
		return fmt.Errorf("max attempts %d must be at least 1", c.MaxAttempts)
	}
	if c.BackoffBase <= 0 {
		return fmt.Errorf("backoff base %s must be positive", c.BackoffBase)
	}
	if c.BackoffCap < c.BackoffBase {
		return fmt.Errorf("backoff cap %s must not be below the base %s", c.BackoffCap, c.BackoffBase)
	}
	return nil
}
