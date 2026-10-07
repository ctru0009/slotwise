//go:build integration

package worker_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/internal/adapters/worker"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// The kill test re-executes this test binary as a real worker process, so the
// SIGKILL lands on the production loop instead of a test double. TestMain
// switches the child on before the test framework runs anything.
const (
	childModeEnv     = "SLOTWISE_TEST_WORKER_MODE"
	childDSNEnv      = "SLOTWISE_TEST_WORKER_DSN"
	childIDEnv       = "SLOTWISE_TEST_WORKER_ID"
	childBaseURL     = "http://slotwise.test"
	testWorkerSecret = "worker-integration-cancel-secret-32-bytes"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(childModeEnv); mode != "" {
		os.Exit(runWorkerChild(mode))
	}
	os.Exit(m.Run())
}

// runWorkerChild is the child process: the real worker loop, the real store,
// the real Jobs handler, and a sender that either blocks inside the handler or
// records what it delivered on stdout.
func runWorkerChild(mode string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := postgres.New(ctx, os.Getenv(childDSNEnv))
	if err != nil {
		return childFailed("opening the database", err)
	}
	defer db.Close()

	signer, err := app.NewSigner(testWorkerSecret)
	if err != nil {
		return childFailed("building the cancel signer", err)
	}
	jobs := app.NewJobs(db, signer, childSender{mode: mode}, childBaseURL, clock.System{})
	w, err := worker.New(childWorkerConfig(), worker.Deps{
		Jobs:    db,
		Handler: jobs.Handle,
		Clock:   clock.System{},
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	if err != nil {
		return childFailed("building the worker", err)
	}
	if err := w.Run(ctx); err != nil {
		return childFailed("running the worker", err)
	}
	return 0
}

func childFailed(doing string, err error) int {
	fmt.Fprintf(os.Stderr, "child: %s: %v\n", doing, err)
	return 1
}

// childWorkerConfig leases for five seconds: long enough for the parent to read
// the held lease and SIGKILL, short enough that the kill test can wait the
// lease out instead of shortening it.
func childWorkerConfig() worker.Config {
	return worker.Config{
		WorkerID:       os.Getenv(childIDEnv),
		Lease:          5 * time.Second,
		HandlerTimeout: time.Second,
		PollInterval:   20 * time.Millisecond,
		MaxAttempts:    3,
		BackoffBase:    time.Second,
		BackoffCap:     time.Minute,
	}
}

// childSender reports from the worker process on stdout: the parent waits for
// "handler-started" before it kills the process, and reads the delivered
// message from the recording mode. The blocking mode ignores its context on
// purpose — a sender that does not honour cancellation is exactly what leaves a
// killed worker's lease held.
type childSender struct{ mode string }

func (s childSender) Send(_ context.Context, msg domain.Message) error {
	if s.mode == "recording" {
		fmt.Printf("child: sent to=%s subject=%s\n", msg.To, msg.Subject)
		return nil
	}
	fmt.Println("child: handler-started")
	<-time.After(time.Hour)
	return errors.New("the blocked sender never delivers")
}

// childProcess is one re-executed worker under the parent's control.
type childProcess struct {
	cmd   *exec.Cmd
	lines chan string
}

func startChild(t *testing.T, mode, id, dsn string) *childProcess {
	t.Helper()

	//nolint:gosec // the test binary re-executes itself as the worker child
	cmd := exec.CommandContext(t.Context(), os.Args[0])
	cmd.Env = append(os.Environ(),
		childModeEnv+"="+mode,
		childDSNEnv+"="+dsn,
		childIDEnv+"="+id,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("piping the child's stdout: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the worker child: %v", err)
	}

	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()

	child := &childProcess{cmd: cmd, lines: lines}
	t.Cleanup(func() { killChild(t, child) })
	return child
}

// killChild sends SIGKILL, which cannot run any deferred cleanup, and reaps the
// process. It is safe to call more than once.
func killChild(t *testing.T, child *childProcess) {
	t.Helper()
	if child.cmd.Process == nil {
		return
	}
	if err := child.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("killing the worker child: %v", err)
	}
	_ = child.cmd.Wait()
}

func waitForChildLine(t *testing.T, child *childProcess, want string) string {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case line, ok := <-child.lines:
			if !ok {
				t.Fatalf("the worker child exited before printing %q", want)
			}
			if strings.Contains(line, want) {
				return line
			}
		case <-deadline:
			t.Fatalf("the worker child did not print %q within 30s", want)
		}
	}
}

// TestWorkerKilledMidJobIsRetried is the requirement 6 test: a real process is
// SIGKILLed while its handler runs, the job stays leased because nothing could
// clean up, the lease expires, and the next worker reclaims the job and
// delivers it. The intermediate assertions are what pin the lease semantics: an
// implementation that claimed inside the handler's transaction would roll the
// claim back on SIGKILL and the retry would pass without a lease ever being
// held.
func TestWorkerKilledMidJobIsRetried(t *testing.T) {
	t.Parallel()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, "kill")
	booking := pgtest.SeedBooking(t, owner, fixture, "2026-11-02T09:00:00Z")
	job := pgtest.SeedJob(t, owner, fixture.Tenant, booking, "booking_confirmation", time.Now().Add(-time.Minute).UTC())

	// Worker A claims the job and blocks inside the handler.
	a := startChild(t, "blocking", "child-a", appDSN)
	waitForChildLine(t, a, "child: handler-started")
	assertHeldLease(t, owner, job, "child-a", 1)

	killChild(t, a)

	// SIGKILL cannot run cleanup: the lease is still held, not released.
	assertHeldLease(t, owner, job, "child-a", 1)

	// The lease's expiry is the only thing that frees the job; wait it out
	// rather than shortening it, so the reclaim runs the same predicate
	// production does when a worker dies.
	waitForLeaseExpiry(t, owner, job)

	// Worker B reclaims the expired lease and delivers.
	b := startChild(t, "recording", "child-b", appDSN)
	line := waitForChildLine(t, b, "child: sent to=")
	state := waitForJobState(t, owner, job, "done", func(jobState) bool { return true })

	if state.lockedBy != "" || !state.lockedUntil.IsZero() {
		t.Errorf("delivered job still carries a lease: %q %v", state.lockedBy, state.lockedUntil)
	}
	if state.attempts != 2 {
		t.Errorf("attempts = %d, want 2: the killed claim and the reclaim", state.attempts)
	}
	if want := "to=ada@example.com"; !strings.Contains(line, want) {
		t.Errorf("delivered line %q does not carry %q", line, want)
	}
}

// assertHeldLease pins the state the killed worker left behind: the job is
// running, leased to that worker, unexpired, and its claim counted.
func assertHeldLease(t *testing.T, owner *pgxpool.Pool, jobID uuid.UUID, workerID string, attempts int) {
	t.Helper()
	state := readJobState(t, owner, jobID)
	if state.status != "running" || state.lockedBy != workerID || state.attempts != attempts {
		t.Errorf("job = %+v, want running by %s attempts=%d", state, workerID, attempts)
	}
	if !state.lockedUntil.After(time.Now()) {
		t.Errorf("locked_until = %v is not in the future, so no lease is held", state.lockedUntil)
	}
}

// waitForLeaseExpiry waits until the dead worker's lease has passed, reading
// the row as the owner.
func waitForLeaseExpiry(t *testing.T, owner *pgxpool.Pool, jobID uuid.UUID) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		state := readJobState(t, owner, jobID)
		if !state.lockedUntil.After(time.Now()) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the lease on job %s did not expire within 15s", jobID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
