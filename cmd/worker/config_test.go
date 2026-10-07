package main

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/adapters/worker"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// envMap turns a map into the getenv func loadConfig takes.
func envMap(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// testCancelKey is long enough for app.NewSigner, so this environment
// matches one the real composition accepts.
const testCancelKey = "config-test-cancel-secret-32-bytes-plus"

// requiredEnv is the environment every worker configuration needs.
func requiredEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":           "postgres://example/db",
		"SLOTWISE_CANCEL_SECRET": testCancelKey,
	}
}

// environmentDatabaseURL is what requiredEnv points the worker at.
const environmentDatabaseURL = "postgres://example/db"

// workerIDPattern is the "<hostname>-<pid>" lease name.
var workerIDPattern = regexp.MustCompile(`^.+-[0-9]+$`)

// stubStore satisfies app.JobStore so the configuration test can build a real
// worker; no test calls it.
type stubStore struct{}

func (stubStore) ClaimJob(context.Context, string, time.Time, int) (domain.Job, error) {
	return domain.Job{}, domain.ErrNotFound
}

func (stubStore) CompleteJob(context.Context, string, domain.Job) (bool, error) {
	return true, nil
}

func (stubStore) RetryJob(context.Context, string, domain.Job, time.Time, string) (bool, error) {
	return true, nil
}

func (stubStore) DeadLetterJob(context.Context, string, domain.Job, string) (bool, error) {
	return true, nil
}

func (stubStore) ListDeadJobs(context.Context, uuid.UUID, int) ([]app.DeadJob, error) {
	return nil, nil
}

func (stubStore) ReleaseJob(context.Context, string, domain.Job) (bool, error) {
	return true, nil
}

func TestLoadConfigRequiresDatabaseURL(t *testing.T) {
	t.Parallel()

	_, err := loadConfig(envMap(map[string]string{"SLOTWISE_CANCEL_SECRET": testCancelKey}))
	if err == nil {
		t.Fatal("loadConfig without DATABASE_URL returned no error")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error %q does not name DATABASE_URL", err)
	}
}

// TestLoadConfigRequiresCancelSecret pins the other required setting: the mail
// carries signed cancel links, so a deployment without the secret cannot build
// them.
func TestLoadConfigRequiresCancelSecret(t *testing.T) {
	t.Parallel()

	_, err := loadConfig(envMap(map[string]string{"DATABASE_URL": environmentDatabaseURL}))
	if err == nil {
		t.Fatal("loadConfig without SLOTWISE_CANCEL_SECRET returned no error")
	}
	if !strings.Contains(err.Error(), "SLOTWISE_CANCEL_SECRET") {
		t.Errorf("error %q does not name SLOTWISE_CANCEL_SECRET", err)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := loadConfig(envMap(requiredEnv()))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.databaseURL != environmentDatabaseURL {
		t.Errorf("databaseURL = %q, want the configured value", cfg.databaseURL)
	}
	if cfg.baseURL != defaultBaseURL {
		t.Errorf("baseURL = %q, want the default %q", cfg.baseURL, defaultBaseURL)
	}
	if cfg.cancelSecret != testCancelKey {
		t.Errorf("cancelSecret = %q, want the configured value", cfg.cancelSecret)
	}
	if cfg.sender != nil {
		t.Error("sender is set, want the nil seam the composition replaces")
	}

	cfg, err = loadConfig(envMap(map[string]string{
		"DATABASE_URL":           environmentDatabaseURL,
		"SLOTWISE_CANCEL_SECRET": testCancelKey,
		"SLOTWISE_BASE_URL":      "https://slotwise.example",
	}))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.baseURL != "https://slotwise.example" {
		t.Errorf("baseURL = %q, want the configured value", cfg.baseURL)
	}
}

// TestLoadConfigWorker pins the queue policy: the default configuration for
// this process, which the worker must accept. New validates the configuration
// before it looks at the dependencies, so the stub store is never called.
func TestLoadConfigWorker(t *testing.T) {
	t.Parallel()

	cfg, err := loadConfig(envMap(requiredEnv()))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if want := worker.DefaultConfig(workerID()); cfg.worker != want {
		t.Errorf("worker = %+v, want %+v", cfg.worker, want)
	}
	if _, err := worker.New(cfg.worker, worker.Deps{
		Jobs:    stubStore{},
		Handler: func(context.Context, domain.Job) error { return nil },
		Clock:   clock.System{},
	}); err != nil {
		t.Errorf("worker.New rejected the loaded configuration: %v", err)
	}
}

// TestWorkerIDNamesTheProcess pins the lease name: a process-shaped id that a
// restart changes, so the leases of a dead worker are reclaimed by expiry.
func TestWorkerIDNamesTheProcess(t *testing.T) {
	t.Parallel()

	id := workerID()
	if id == "" {
		t.Fatal("workerID returned an empty id")
	}
	if !strings.HasSuffix(id, "-"+strconv.Itoa(os.Getpid())) {
		t.Errorf("workerID = %q, want it to end in the process id %d", id, os.Getpid())
	}
	if !workerIDPattern.MatchString(id) {
		t.Errorf("workerID = %q, want the <hostname>-<pid> shape", id)
	}
	if again := workerID(); again != id {
		t.Errorf("workerID = %q then %q, want a stable id within the process", id, again)
	}
}
