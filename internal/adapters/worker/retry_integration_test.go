//go:build integration

package worker_test

import (
	"context"
	"errors"
	"sync"
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

// retryFixture is a real queue in a throwaway Postgres with a fake clock, so
// the tests can jump past a backoff without sleeping it out.
type retryFixture struct {
	owner   *pgxpool.Pool
	db      *postgres.DB
	clock   *clock.Fake
	fixture pgtest.Fixture
	tenant  uuid.UUID
	booking uuid.UUID
}

func newRetryFixture(t *testing.T, slug string) retryFixture {
	t.Helper()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, slug)
	booking := pgtest.SeedBooking(t, owner, fixture, "2026-11-02T09:00:00Z")
	return retryFixture{
		owner:   owner,
		db:      pgtest.AppDB(t, appDSN),
		clock:   clock.NewFake(time.Date(2026, time.November, 1, 9, 0, 0, 0, time.UTC)),
		fixture: fixture,
		tenant:  fixture.Tenant,
		booking: booking,
	}
}

// seedDueJob queues a confirmation job that is already due on the fake clock.
func (f retryFixture) seedDueJob(t *testing.T) uuid.UUID {
	t.Helper()
	return pgtest.SeedJob(t, f.owner, f.tenant, f.booking, "booking_confirmation", f.clock.Now().Add(-time.Second))
}

// failingSender counts attempts and fails every one of them.
type failingSender struct {
	calls int
}

func (s *failingSender) Send(context.Context, domain.Message) error {
	s.calls++
	return errors.New("smtp refused")
}

// blockingSender blocks until the handler's context is canceled, then reports
// the cancellation the way a sender that respects its deadline would.
type blockingSender struct {
	started chan struct{}
}

func (s *blockingSender) Send(ctx context.Context, _ domain.Message) error {
	s.started <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

// testWorkerConfig is the policy the tests drive: small numbers, a whole-second
// lease, and a lease twice the handler timeout so the invariant holds.
func testWorkerConfig(id string) worker.Config {
	return worker.Config{
		WorkerID:       id,
		Lease:          30 * time.Second,
		HandlerTimeout: time.Second,
		PollInterval:   5 * time.Millisecond,
		MaxAttempts:    3,
		BackoffBase:    30 * time.Second,
		BackoffCap:     5 * time.Minute,
	}
}

// newTestWorker builds the production composition over the test database: the
// real postgres store, the real Jobs handler, the injected clock, and a test
// sender.
func newTestWorker(t *testing.T, clk clock.Clock, id string, db *postgres.DB, sender app.Sender) *worker.Worker {
	t.Helper()
	signer, err := app.NewSigner(testWorkerSecret)
	if err != nil {
		t.Fatalf("building the signer: %v", err)
	}
	jobs := app.NewJobs(db, signer, sender, "http://slotwise.test", clk)
	w, err := worker.New(testWorkerConfig(id), worker.Deps{
		Jobs:    db,
		Handler: jobs.Handle,
		Clock:   clk,
	})
	if err != nil {
		t.Fatalf("building the worker: %v", err)
	}
	return w
}

// runUntil launches Run and returns a stop function that cancels it and waits
// for it to return.
func runUntil(t *testing.T, w *worker.Worker) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run = %v, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not stop within 10s")
		}
	}
}

// waitForJobState polls the queue as the owner until the job has the wanted
// status and shape, which is how these tests observe the transitions the worker
// commits. The predicate matters: a seeded row is already ready, so status
// alone would read the state before the worker ever claimed it.
func waitForJobState(t *testing.T, owner *pgxpool.Pool, jobID uuid.UUID, want string, ok func(jobState) bool) jobState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		state := readJobState(t, owner, jobID)
		if state.status == want && ok(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s = %+v after 10s, want %s", jobID, state, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// driveAttempt runs the worker until the job reaches want with ok, then stops
// it: the three steps every retry assertion needs.
func driveAttempt(t *testing.T, w *worker.Worker, owner *pgxpool.Pool, jobID uuid.UUID, want string, ok func(jobState) bool) jobState {
	t.Helper()
	stop := runUntil(t, w)
	state := waitForJobState(t, owner, jobID, want, ok)
	stop()
	return state
}

// retriedWith matches a job requeued after the given number of claims with a
// fresh due time, so a wait cannot read the seeded row.
func retriedWith(attempts int, seedAt time.Time) func(jobState) bool {
	return func(s jobState) bool { return s.attempts == attempts && !s.runAt.Equal(seedAt) }
}

// attemptsAt matches a job that has had exactly n claims.
func attemptsAt(n int) func(jobState) bool {
	return func(s jobState) bool { return s.attempts == n }
}

// deadWith matches a dead-lettered job with its reason kept.
func deadWith(n int) func(jobState) bool {
	return func(s jobState) bool { return s.attempts == n && s.lastError != "" }
}

// assertNotClaimable pins that nothing is due at the given instant.
func assertNotClaimable(ctx context.Context, t *testing.T, db *postgres.DB, at time.Time) {
	t.Helper()
	if _, err := db.ClaimJob(ctx, "probe", at, 60); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("claiming at %s = %v, want domain.ErrNotFound", at, err)
	}
}

// TestWorkerRetriesWithBackoffUntilDeadLetter drives the whole retry policy
// against the real queue: each failed attempt requeues the job exactly one
// backoff interval out, and the third failure dead-letters it with the reason
// kept.
func TestWorkerRetriesWithBackoffUntilDeadLetter(t *testing.T) {
	t.Parallel()
	f := newRetryFixture(t, "backoff")
	job := f.seedDueJob(t)
	t0 := f.clock.Now()
	seedAt := t0.Add(-time.Second)

	sender := &failingSender{}
	w := newTestWorker(t, f.clock, "backoff-worker", f.db, sender)

	// Attempt 1 fails: ready again at now + base, with the claim counted.
	state := driveAttempt(t, w, f.owner, job, "ready", retriedWith(1, seedAt))
	if state.attempts != 1 {
		t.Errorf("attempts after one failure = %d, want 1", state.attempts)
	}
	if want := t0.Add(30 * time.Second); !state.runAt.Equal(want) {
		t.Errorf("run_at after the first failure = %v, want %v", state.runAt, want)
	}
	if state.lastError == "" {
		t.Error("the failed attempt kept no reason")
	}

	// The job is not claimable until the backoff has passed.
	assertNotClaimable(t.Context(), t, f.db, t0.Add(30*time.Second-time.Millisecond))

	// Attempt 2 fails at now + base: the next backoff doubles.
	f.clock.Advance(30 * time.Second)
	state = driveAttempt(t, w, f.owner, job, "ready", attemptsAt(2))
	if state.attempts != 2 {
		t.Errorf("attempts after two failures = %d, want 2", state.attempts)
	}
	if want := t0.Add(30*time.Second + 60*time.Second); !state.runAt.Equal(want) {
		t.Errorf("run_at after the second failure = %v, want %v", state.runAt, want)
	}

	// Attempt 3 fails: the budget is spent and the job is dead.
	f.clock.Advance(60 * time.Second)
	state = driveAttempt(t, w, f.owner, job, "dead", deadWith(3))
	if state.attempts != 3 || state.lastError == "" {
		t.Errorf("dead job = %+v, want attempts 3 with the reason kept", state)
	}

	// A dead job is terminal: nothing claims it again.
	assertNotClaimable(t.Context(), t, f.db, f.clock.Now())
	if sender.calls != 3 {
		t.Errorf("the sender was called %d times, want 3", sender.calls)
	}
}

// TestWorkerShutdownReleasesTheInFlightJob cancels the worker while its handler
// is blocked and proves the graceful path against the real queue: the job goes
// back to ready with its attempt refunded, and the release write survives the
// canceled worker context.
func TestWorkerShutdownReleasesTheInFlightJob(t *testing.T) {
	t.Parallel()
	f := newRetryFixture(t, "shutdown")
	job := f.seedDueJob(t)
	seed := readJobState(t, f.owner, job)

	sender := &blockingSender{started: make(chan struct{}, 1)}
	w := newTestWorker(t, f.clock, "shutdown-worker", f.db, sender)

	stop := runUntil(t, w)
	select {
	case <-sender.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler never started")
	}

	// The job is leased while the handler runs.
	if state := readJobState(t, f.owner, job); state.status != "running" || state.attempts != 1 {
		t.Fatalf("in-flight job = %+v, want running attempts=1", state)
	}

	stop()

	state := readJobState(t, f.owner, job)
	if state.status != "ready" || state.attempts != 0 {
		t.Errorf("released job = %+v, want ready with the attempt refunded", state)
	}
	if state.lockedBy != "" || !state.lockedUntil.IsZero() {
		t.Errorf("released job still carries a lease: %q %v", state.lockedBy, state.lockedUntil)
	}
	if !state.runAt.Equal(seed.runAt) {
		t.Errorf("released run_at = %v, want its due time %v unchanged", state.runAt, seed.runAt)
	}
}

// countingSender records how many messages were delivered.
type countingSender struct {
	mu    sync.Mutex
	calls int
}

func (s *countingSender) Send(context.Context, domain.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return nil
}

func (s *countingSender) delivered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestTwoWorkersClaimDisjointJobs runs two live worker loops over one queue:
// SKIP LOCKED hands each due job to exactly one of them, every job is
// delivered, and no row is claimed twice.
func TestTwoWorkersClaimDisjointJobs(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newRetryFixture(t, "two-workers")

	const jobs = 10
	starts := []string{
		"2026-11-03T09:00:00Z", "2026-11-04T09:00:00Z", "2026-11-05T09:00:00Z",
		"2026-11-06T09:00:00Z", "2026-11-07T09:00:00Z", "2026-11-08T09:00:00Z",
		"2026-11-09T09:00:00Z", "2026-11-10T09:00:00Z", "2026-11-11T09:00:00Z",
		"2026-11-12T09:00:00Z",
	}
	for _, startsAt := range starts {
		booking := pgtest.SeedBooking(t, f.owner, f.fixture, startsAt)
		pgtest.SeedJob(t, f.owner, f.tenant, booking, "booking_confirmation", f.clock.Now().Add(-time.Second))
	}

	sender := &countingSender{}
	stopFirst := runUntil(t, newTestWorker(t, f.clock, "worker-1", f.db, sender))
	stopSecond := runUntil(t, newTestWorker(t, f.clock, "worker-2", f.db, sender))
	waitForDeliveries(t, sender, jobs)
	stopFirst()
	stopSecond()

	var done, doubleClaimed int
	err := f.owner.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'done'),
		       count(*) FILTER (WHERE attempts > 1)
		  FROM jobs WHERE tenant_id = $1`, f.tenant).Scan(&done, &doubleClaimed)
	if err != nil {
		t.Fatalf("reading the queue: %v", err)
	}
	if done != jobs {
		t.Errorf("%d jobs done, want %d", done, jobs)
	}
	if doubleClaimed != 0 {
		t.Errorf("%d jobs were claimed twice", doubleClaimed)
	}
	if got := sender.delivered(); got != jobs {
		t.Errorf("%d deliveries, want %d", got, jobs)
	}
}

func waitForDeliveries(t *testing.T, sender *countingSender, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for sender.delivered() < want {
		if time.Now().After(deadline) {
			t.Fatalf("%d deliveries after 15s, want %d", sender.delivered(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// jobState is the queue row shape the worker tests assert.
type jobState struct {
	status, lockedBy, lastError string
	attempts                    int
	runAt, lockedUntil          time.Time
}

func readJobState(t *testing.T, owner *pgxpool.Pool, jobID uuid.UUID) jobState {
	t.Helper()
	var (
		state       jobState
		lockedUntil *time.Time
	)
	err := owner.QueryRow(t.Context(), `
		SELECT status, attempts, run_at, COALESCE(locked_by, ''), locked_until, COALESCE(last_error, '')
		  FROM jobs WHERE id = $1`, jobID).
		Scan(&state.status, &state.attempts, &state.runAt, &state.lockedBy, &lockedUntil, &state.lastError)
	if err != nil {
		t.Fatalf("reading job %s: %v", jobID, err)
	}
	if lockedUntil != nil {
		state.lockedUntil = *lockedUntil
	}
	return state
}
