package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// testWorkerID names the worker in every test.
const testWorkerID = "test-worker-1"

// testNow is the fake clock's fixed instant, so retry run_at assertions have
// an exact expected value.
var testNow = time.Date(2026, time.October, 7, 9, 0, 0, 0, time.UTC)

// testJob is the queue row a claim hands over unless a test builds its own.
func testJob(attempts int) domain.Job {
	return domain.Job{
		ID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		TenantID:  uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		BookingID: uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		Kind:      domain.JobBookingConfirmation,
		Attempts:  attempts,
	}
}

// testConfig is the production policy with a poll interval short enough for
// tests to wait out.
func testConfig() Config {
	cfg := DefaultConfig(testWorkerID)
	cfg.PollInterval = time.Millisecond
	return cfg
}

// call is one transition the worker asked for, recorded with the state the
// store saw.
type call struct {
	workerID string
	job      domain.Job
	runAt    time.Time
	reason   string
	ctxErr   error
}

// recorded is a race-free copy of everything the worker asked of the store.
type recorded struct {
	claims     int
	workerIDs  []string
	claimTimes []time.Time
	leaseSeen  []int
	completed  []call
	retried    []call
	dead       []call
	released   []call
}

// fakeStore records every call the worker makes. It hands out queued jobs in
// order and reports claimErr once the queue is empty, and every transition
// answers with applied. stopAfterClaims cancels the worker's context once that
// many claims have run, which ends Run deterministically right after the
// transition under test has been applied.
type fakeStore struct {
	mu              sync.Mutex
	queue           []domain.Job
	claimErr        error
	applied         bool
	stopAfterClaims int
	cancel          context.CancelFunc

	workerIDs  []string
	claimTimes []time.Time
	leaseSeen  []int
	completed  []call
	retried    []call
	dead       []call
	released   []call
}

var _ app.JobStore = (*fakeStore)(nil)

// newFakeStore returns a store handing out queue in order and applying every
// transition. Once the queue is empty it reports domain.ErrNotFound — a claim
// error of nil with a zero job would make the worker handle a phantom row.
func newFakeStore(queue ...domain.Job) *fakeStore {
	return &fakeStore{queue: queue, claimErr: domain.ErrNotFound, applied: true}
}

func (s *fakeStore) ClaimJob(_ context.Context, workerID string, claimTime time.Time, leaseSeconds int) (domain.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workerIDs = append(s.workerIDs, workerID)
	s.claimTimes = append(s.claimTimes, claimTime)
	s.leaseSeen = append(s.leaseSeen, leaseSeconds)
	if s.stopAfterClaims > 0 && len(s.claimTimes) >= s.stopAfterClaims && s.cancel != nil {
		s.cancel()
	}
	if len(s.queue) == 0 {
		return domain.Job{}, s.claimErr
	}
	job := s.queue[0]
	s.queue = s.queue[1:]
	return job, nil
}

func (s *fakeStore) CompleteJob(ctx context.Context, workerID string, job domain.Job) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, call{workerID: workerID, job: job, ctxErr: ctx.Err()})
	return s.applied, nil
}

func (s *fakeStore) RetryJob(ctx context.Context, workerID string, job domain.Job, runAt time.Time, reason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried = append(s.retried, call{workerID: workerID, job: job, runAt: runAt, reason: reason, ctxErr: ctx.Err()})
	return s.applied, nil
}

func (s *fakeStore) DeadLetterJob(ctx context.Context, workerID string, job domain.Job, reason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dead = append(s.dead, call{workerID: workerID, job: job, reason: reason, ctxErr: ctx.Err()})
	return s.applied, nil
}

func (s *fakeStore) ReleaseJob(ctx context.Context, workerID string, job domain.Job) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, call{workerID: workerID, job: job, ctxErr: ctx.Err()})
	return s.applied, nil
}

// recorded copies everything recorded so far, safe to assert on after Run has
// returned.
func (s *fakeStore) recorded() recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return recorded{
		claims:     len(s.claimTimes),
		workerIDs:  append([]string(nil), s.workerIDs...),
		claimTimes: append([]time.Time(nil), s.claimTimes...),
		leaseSeen:  append([]int(nil), s.leaseSeen...),
		completed:  append([]call(nil), s.completed...),
		retried:    append([]call(nil), s.retried...),
		dead:       append([]call(nil), s.dead...),
		released:   append([]call(nil), s.released...),
	}
}

// newTestWorker builds a worker over the fake store, a fake clock and a
// discard logger.
func newTestWorker(t *testing.T, cfg Config, store *fakeStore, handler Handler) *Worker {
	t.Helper()
	w, err := New(cfg, Deps{
		Jobs:    store,
		Handler: handler,
		Clock:   clock.NewFake(testNow),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w
}

// runWorker runs the worker until the store cancels its context, which it does
// once claim number stopAfterClaims has been recorded.
func runWorker(t *testing.T, w *Worker, store *fakeStore, stopAfterClaims int) recorded {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.mu.Lock()
	store.stopAfterClaims = stopAfterClaims
	store.cancel = cancel
	store.mu.Unlock()

	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return store.recorded()
}

func TestDefaultConfigValidates(t *testing.T) {
	t.Parallel()

	if err := DefaultConfig("worker-1").validate(); err != nil {
		t.Errorf("DefaultConfig does not validate: %v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string // the offending setting and value the error must name
	}{
		{
			name:   "missing worker id",
			mutate: func(c *Config) { c.WorkerID = "" },
			want:   "worker id",
		},
		{
			name:   "lease shorter than a second",
			mutate: func(c *Config) { c.Lease = 500 * time.Millisecond },
			want:   "lease 500ms",
		},
		{
			name:   "lease not a whole number of seconds",
			mutate: func(c *Config) { c.Lease = 1500 * time.Millisecond },
			want:   "lease 1.5s",
		},
		{
			name:   "zero handler timeout",
			mutate: func(c *Config) { c.HandlerTimeout = 0 },
			want:   "handler timeout 0s",
		},
		{
			name: "lease without room for two handler runs",
			mutate: func(c *Config) {
				c.Lease = time.Minute
				c.HandlerTimeout = 31 * time.Second
			},
			want: "lease 1m0s",
		},
		{
			name:   "zero poll interval",
			mutate: func(c *Config) { c.PollInterval = 0 },
			want:   "poll interval 0s",
		},
		{
			name:   "no attempts",
			mutate: func(c *Config) { c.MaxAttempts = 0 },
			want:   "max attempts 0",
		},
		{
			name:   "zero backoff base",
			mutate: func(c *Config) { c.BackoffBase = 0 },
			want:   "backoff base 0s",
		},
		{
			name: "backoff cap below the base",
			mutate: func(c *Config) {
				c.BackoffBase = time.Minute
				c.BackoffCap = 30 * time.Second
			},
			want: "backoff cap 30s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig("worker-1")
			tt.mutate(&cfg)

			err := cfg.validate()
			if err == nil {
				t.Fatalf("validate accepted %+v", cfg)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not name %q", err, tt.want)
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      func(*Config)
		attempts int
		want     time.Duration
	}{
		{name: "attempt 1 waits the base", attempts: 1, want: defaultBackoffBase},
		{name: "attempt 2 doubles", attempts: 2, want: 2 * defaultBackoffBase},
		{name: "attempt 3", attempts: 3, want: 4 * defaultBackoffBase},
		{name: "attempt 4", attempts: 4, want: 8 * defaultBackoffBase},
		{name: "attempt 5", attempts: 5, want: 16 * defaultBackoffBase},
		{name: "attempt 6 is still below the cap", attempts: 6, want: 32 * defaultBackoffBase},
		{name: "attempt 7 reaches the cap", attempts: 7, want: defaultBackoffCap},
		{name: "attempt 8 stays at the cap", attempts: 8, want: defaultBackoffCap},
		{name: "attempt 9 stays at the cap", attempts: 9, want: defaultBackoffCap},
		{name: "a thousand attempts do not overflow", attempts: 1000, want: defaultBackoffCap},
		{
			name:     "a base equal to the cap stays at the cap",
			cfg:      func(c *Config) { c.BackoffBase, c.BackoffCap = defaultBackoffCap, defaultBackoffCap },
			attempts: 3,
			want:     defaultBackoffCap,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig("worker-1")
			if tt.cfg != nil {
				tt.cfg(&cfg)
			}
			w := &Worker{cfg: cfg}
			if got := w.backoff(tt.attempts); got != tt.want {
				t.Errorf("backoff(%d) = %s, want %s", tt.attempts, got, tt.want)
			}
		})
	}
}

func TestErrorText(t *testing.T) {
	t.Parallel()

	if got := errorText(errors.New("boom")); got != "boom" {
		t.Errorf("errorText = %q, want the message unchanged", got)
	}

	atLimit := errors.New(strings.Repeat("b", errorTextLimit))
	if got := errorText(atLimit); got != atLimit.Error() {
		t.Errorf("errorText cut a message of exactly %d bytes to %d", errorTextLimit, len(got))
	}

	long := errors.New(strings.Repeat("a", 600))
	if got := errorText(long); got != strings.Repeat("a", errorTextLimit) {
		t.Errorf("errorText = %d bytes, want the first %d", len(got), errorTextLimit)
	}

	// The cut lands inside the 250th 'é', so it must back up to the rune
	// start rather than store half a rune.
	runic := errors.New("a" + strings.Repeat("é", 400))
	got := errorText(runic)
	want := "a" + strings.Repeat("é", 249)
	if got != want {
		t.Errorf("errorText = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Error("errorText returned invalid UTF-8")
	}
}

func TestRunCompletesASuccessfulJob(t *testing.T) {
	t.Parallel()

	job := testJob(1)
	store := newFakeStore(job)

	type handled struct {
		job         domain.Job
		deadline    time.Time
		hasDeadline bool
	}
	handledCh := make(chan handled, 1)
	handler := func(ctx context.Context, job domain.Job) error {
		deadline, ok := ctx.Deadline()
		handledCh <- handled{job: job, deadline: deadline, hasDeadline: ok}
		return nil
	}
	w := newTestWorker(t, testConfig(), store, handler)

	got := runWorker(t, w, store, 2)

	select {
	case ran := <-handledCh:
		if ran.job != job {
			t.Errorf("handler got job %+v, want %+v", ran.job, job)
		}
		// The handler must run under the configured timeout. Its deadline is
		// wall-clock however — context deadlines read the real clock, not the
		// injected one — so assert the distance, not an instant.
		if !ran.hasDeadline {
			t.Error("the handler ran without a deadline")
		} else if left := time.Until(ran.deadline); left <= 0 || left > defaultHandlerTimeout {
			t.Errorf("the handler deadline is %s away, want (0, %s]", left, defaultHandlerTimeout)
		}
	default:
		t.Fatal("the handler was never called")
	}

	assertCompletedSuccessfully(t, got, job)
	assertClaimShape(t, got)
}

// assertCompletedSuccessfully pins the store calls one successful job leaves:
// exactly one CompleteJob with the claimed row and the worker's id, and no
// other transition.
func assertCompletedSuccessfully(t *testing.T, got recorded, job domain.Job) {
	t.Helper()
	if len(got.completed) != 1 {
		t.Fatalf("CompleteJob calls = %d, want 1", len(got.completed))
	}
	if got.completed[0].job != job {
		t.Errorf("CompleteJob got job %+v, want the claimed %+v", got.completed[0].job, job)
	}
	if got.completed[0].workerID != testWorkerID {
		t.Errorf("CompleteJob worker = %q, want %q", got.completed[0].workerID, testWorkerID)
	}
	if got.completed[0].ctxErr != nil {
		t.Errorf("CompleteJob ran with a canceled context: %v", got.completed[0].ctxErr)
	}
	if len(got.retried)+len(got.dead)+len(got.released) != 0 {
		t.Errorf("unexpected transitions: retried=%d dead=%d released=%d", len(got.retried), len(got.dead), len(got.released))
	}
}

// assertClaimShape pins what the worker asked the store to claim with: its id,
// the configured lease in whole seconds, and the injected clock's instant.
func assertClaimShape(t *testing.T, got recorded) {
	t.Helper()
	if got.workerIDs[0] != testWorkerID {
		t.Errorf("ClaimJob worker = %q, want %q", got.workerIDs[0], testWorkerID)
	}
	if want := int(defaultLease / time.Second); got.leaseSeen[0] != want {
		t.Errorf("ClaimJob lease = %ds, want %ds", got.leaseSeen[0], want)
	}
	if !got.claimTimes[0].Equal(testNow) {
		t.Errorf("ClaimJob claim time = %v, want %v", got.claimTimes[0], testNow)
	}
}

func TestRunCompletesASkippedJob(t *testing.T) {
	t.Parallel()

	store := newFakeStore(testJob(1))
	w := newTestWorker(t, testConfig(), store, func(context.Context, domain.Job) error {
		return fmt.Errorf("the booking was cancelled: %w", domain.ErrJobSkipped)
	})

	got := runWorker(t, w, store, 2)

	if len(got.completed) != 1 {
		t.Fatalf("CompleteJob calls = %d, want 1", len(got.completed))
	}
	if len(got.retried)+len(got.dead)+len(got.released) != 0 {
		t.Errorf("unexpected transitions: retried=%d dead=%d released=%d", len(got.retried), len(got.dead), len(got.released))
	}
}

func TestRunRetriesAFailedJob(t *testing.T) {
	t.Parallel()

	job := testJob(1)
	store := newFakeStore(job)
	cfg := testConfig()
	cfg.MaxAttempts = 3
	errBoom := errors.New("sending the mail: boom")
	w := newTestWorker(t, cfg, store, func(context.Context, domain.Job) error { return errBoom })

	got := runWorker(t, w, store, 2)

	if len(got.retried) != 1 {
		t.Fatalf("RetryJob calls = %d, want 1", len(got.retried))
	}
	retry := got.retried[0]
	if retry.job != job {
		t.Errorf("RetryJob got job %+v, want the claimed %+v", retry.job, job)
	}
	if retry.workerID != testWorkerID {
		t.Errorf("RetryJob worker = %q, want %q", retry.workerID, testWorkerID)
	}
	if want := testNow.Add(defaultBackoffBase); !retry.runAt.Equal(want) {
		t.Errorf("RetryJob run at = %v, want %v", retry.runAt, want)
	}
	if retry.reason != errBoom.Error() {
		t.Errorf("RetryJob reason = %q, want %q", retry.reason, errBoom.Error())
	}
	if len(got.completed)+len(got.dead)+len(got.released) != 0 {
		t.Errorf("unexpected transitions: completed=%d dead=%d released=%d", len(got.completed), len(got.dead), len(got.released))
	}
}

func TestRunDeadLettersAFailedJobAtTheAttemptLimit(t *testing.T) {
	t.Parallel()

	job := testJob(3)
	store := newFakeStore(job)
	cfg := testConfig()
	cfg.MaxAttempts = 3
	w := newTestWorker(t, cfg, store, func(context.Context, domain.Job) error { return errors.New("boom") })

	got := runWorker(t, w, store, 2)

	if len(got.dead) != 1 {
		t.Fatalf("DeadLetterJob calls = %d, want 1", len(got.dead))
	}
	if got.dead[0].job != job {
		t.Errorf("DeadLetterJob got job %+v, want the claimed %+v", got.dead[0].job, job)
	}
	if got.dead[0].reason != "boom" {
		t.Errorf("DeadLetterJob reason = %q, want the handler error text", got.dead[0].reason)
	}
	if len(got.retried)+len(got.completed)+len(got.released) != 0 {
		t.Errorf("unexpected transitions: retried=%d completed=%d released=%d", len(got.retried), len(got.completed), len(got.released))
	}
}

func TestRunDeadLettersAnOverBudgetClaim(t *testing.T) {
	t.Parallel()

	// The claim that produced this row spent the last attempt: an earlier
	// execution died before it could report its outcome.
	job := testJob(4)
	store := newFakeStore(job)
	cfg := testConfig()
	cfg.MaxAttempts = 3
	var calls atomic.Int32
	w := newTestWorker(t, cfg, store, func(context.Context, domain.Job) error {
		calls.Add(1)
		return nil
	})

	got := runWorker(t, w, store, 2)

	if calls.Load() != 0 {
		t.Error("the handler ran for a job whose budget was already spent")
	}
	if len(got.dead) != 1 {
		t.Fatalf("DeadLetterJob calls = %d, want 1", len(got.dead))
	}
	if want := "attempt budget of 3 spent before this claim"; got.dead[0].reason != want {
		t.Errorf("DeadLetterJob reason = %q, want %q", got.dead[0].reason, want)
	}
	if len(got.retried)+len(got.completed)+len(got.released) != 0 {
		t.Errorf("unexpected transitions: retried=%d completed=%d released=%d", len(got.retried), len(got.completed), len(got.released))
	}
}

func TestRunReleasesAJobInterruptedByShutdown(t *testing.T) {
	t.Parallel()

	job := testJob(1)
	store := newFakeStore(job)
	started := make(chan struct{})
	handler := func(ctx context.Context, _ domain.Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	w := newTestWorker(t, testConfig(), store, handler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never started")
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was canceled")
	}

	got := store.recorded()
	if len(got.released) != 1 {
		t.Fatalf("ReleaseJob calls = %d, want 1", len(got.released))
	}
	if got.released[0].job != job {
		t.Errorf("ReleaseJob got job %+v, want the claimed %+v", got.released[0].job, job)
	}
	// The release write must not run on the canceled worker context: it would
	// be dropped and the job would stay leased until expiry.
	if got.released[0].ctxErr != nil {
		t.Errorf("ReleaseJob ran with a canceled context: %v", got.released[0].ctxErr)
	}
	if len(got.retried)+len(got.completed)+len(got.dead) != 0 {
		t.Errorf("unexpected transitions: retried=%d completed=%d dead=%d", len(got.retried), len(got.completed), len(got.dead))
	}
}

func TestRunReturnsWithoutClaimingWhenAlreadyCanceled(t *testing.T) {
	t.Parallel()

	store := newFakeStore(testJob(1))
	w := newTestWorker(t, testConfig(), store, func(context.Context, domain.Job) error {
		t.Error("the handler ran for an already-canceled worker")
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := store.recorded(); got.claims != 0 {
		t.Errorf("claims = %d, want none for an already-canceled worker", got.claims)
	}
}

func TestRunPollsAnEmptyQueue(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.claimErr = domain.ErrNotFound
	w := newTestWorker(t, testConfig(), store, func(context.Context, domain.Job) error {
		t.Error("the handler ran though nothing was due")
		return nil
	})

	got := runWorker(t, w, store, 3)

	if got.claims != 3 {
		t.Errorf("claims = %d, want 3: an empty queue must loop, not return", got.claims)
	}
	if len(got.completed)+len(got.retried)+len(got.dead)+len(got.released) != 0 {
		t.Error("an empty queue produced a transition")
	}
}

func TestRunSurvivesAClaimError(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	store.claimErr = errors.New("connection refused")
	w := newTestWorker(t, testConfig(), store, func(context.Context, domain.Job) error {
		t.Error("the handler ran though the claim failed")
		return nil
	})

	got := runWorker(t, w, store, 3)

	if got.claims != 3 {
		t.Errorf("claims = %d, want 3: a database blip must not kill the worker", got.claims)
	}
	if len(got.completed)+len(got.retried)+len(got.dead)+len(got.released) != 0 {
		t.Error("a failed claim produced a transition")
	}
}

func TestRunToleratesALostLease(t *testing.T) {
	t.Parallel()

	store := newFakeStore(testJob(1))
	store.applied = false
	w := newTestWorker(t, testConfig(), store, func(context.Context, domain.Job) error { return nil })

	got := runWorker(t, w, store, 2)

	// The call was made; the store just reported that the lease had moved on,
	// which the worker warns about and moves past.
	if len(got.completed) != 1 {
		t.Errorf("CompleteJob calls = %d, want 1", len(got.completed))
	}
	if len(got.retried)+len(got.dead)+len(got.released) != 0 {
		t.Errorf("unexpected transitions: retried=%d dead=%d released=%d", len(got.retried), len(got.dead), len(got.released))
	}
}
