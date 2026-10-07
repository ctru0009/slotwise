//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/internal/domain"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// jobClaimSQL is the statement the adapter runs; the test keeps its own copy so
// a signature drift fails here as well as in the adapter.
const jobClaimSQL = `SELECT id, tenant_id, booking_id, kind, attempts FROM public.job_claim($1, $2, $3)`

func TestCreateBookingQueuesItsMail(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "jobs")
	in := bookingInput(t, f.fixture, "key-mail")

	if _, err := f.useCase.Create(ctx, f.fixture.Tenant, in); err != nil {
		t.Fatalf("Create: %v", err)
	}

	confirmation := readJob(t, f.owner, f.fixture.Tenant, "booking_confirmation")
	if confirmation.status != "ready" || confirmation.attempts != 0 {
		t.Errorf("confirmation = %s attempts=%d, want ready attempts=0", confirmation.status, confirmation.attempts)
	}
	if !confirmation.runAt.Equal(f.clock.Now()) {
		t.Errorf("confirmation run_at = %v, want the injected clock's now %v", confirmation.runAt, f.clock.Now())
	}
	if confirmation.lockedBy != "" || !confirmation.lockedUntil.IsZero() {
		t.Errorf("confirmation carries a lease before any claim: %q %v", confirmation.lockedBy, confirmation.lockedUntil)
	}

	// 09:00 Berlin the day after the parked clock, so the reminder is 24 hours
	// before the start, not now.
	reminder := readJob(t, f.owner, f.fixture.Tenant, "booking_reminder")
	wantReminderAt := in.StartsAt.Add(-24 * time.Hour)
	if !reminder.runAt.Equal(wantReminderAt) {
		t.Errorf("reminder run_at = %v, want %v", reminder.runAt, wantReminderAt)
	}
	if got := countJobs(t, f.owner, f.fixture.Tenant); got != 2 {
		t.Errorf("%d jobs, want 2 (confirmation and reminder)", got)
	}

	// The replay returns the stored booking and queues nothing: the row set is
	// already there, and the unique key backs the rule up.
	if _, err := f.useCase.Create(ctx, f.fixture.Tenant, in); err != nil {
		t.Fatalf("replayed Create: %v", err)
	}
	if got := countJobs(t, f.owner, f.fixture.Tenant); got != 2 {
		t.Errorf("%d jobs after the replay, want 2", got)
	}
	if got := jobKindCount(t, f.owner, f.fixture.Tenant, "booking_confirmation"); got != 1 {
		t.Errorf("%d confirmation jobs after the replay, want 1", got)
	}
}

// TestBookingInsideTheWindowQueuesNoReminder pins the scheduling policy: a
// booking made less than 24 hours before its start gets its confirmation only,
// because the confirmation already carries the cancel link and an immediate
// reminder could only repeat it.
func TestBookingInsideTheWindowQueuesNoReminder(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "window")

	// One hour before the fixture's 09:00 slot: inside the lead window.
	f.clock.Set(f.clock.Now().Add(31 * time.Hour))

	booking, err := f.useCase.Create(ctx, f.fixture.Tenant, bookingInput(t, f.fixture, "key-window"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if booking.StartsAt.Sub(f.clock.Now()) >= 24*time.Hour {
		t.Fatalf("the fixture no longer books inside the window: %v before the start", booking.StartsAt.Sub(f.clock.Now()))
	}

	if got := countJobs(t, f.owner, f.fixture.Tenant); got != 1 {
		t.Errorf("%d jobs, want only the confirmation", got)
	}
	if got := jobKindCount(t, f.owner, f.fixture.Tenant, "booking_reminder"); got != 0 {
		t.Errorf("%d reminder jobs, want 0 inside the window", got)
	}
}

// TestBookingAndJobsCommitTogether proves the enqueue rides the booking's own
// transaction: a poisoned job insert must take the booking down with it. If
// someone ever moves the enqueue out of the transaction (or swallows its
// error), the booking commits without its mail and this test goes red.
func TestBookingAndJobsCommitTogether(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "atomic")
	poisonJobInserts(t, f.owner)

	_, err := f.useCase.Create(ctx, f.fixture.Tenant, bookingInput(t, f.fixture, "key-atomic"))
	if err == nil {
		t.Fatal("Create succeeded while every job insert raised")
	}
	if got := countBookings(t, f.owner, f.fixture.Tenant); got != 0 {
		t.Errorf("%d booking rows after the failed transaction, want 0", got)
	}
	if got := countJobs(t, f.owner, f.fixture.Tenant); got != 0 {
		t.Errorf("%d job rows after the failed transaction, want 0", got)
	}
}

// TestJobsUniquePerBookingKind pins the enqueue's idempotence at the schema
// level: the unique key is what makes a second enqueue a no-op and a dead job
// requeueable only by UPDATE.
func TestJobsUniquePerBookingKind(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, "unique")
	booking := pgtest.SeedBooking(t, owner, fixture, "2026-11-02T09:00:00Z")
	pgtest.SeedJob(t, owner, fixture.Tenant, booking, "booking_confirmation", time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))

	err := pgtest.AppDB(t, appDSN).WithTenant(ctx, fixture.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO jobs (tenant_id, booking_id, kind, run_at)
			VALUES ($1, $2, 'booking_confirmation', now())`, fixture.Tenant, booking)
		return err
	})
	if !hasSQLState(err, "23505") {
		t.Errorf("a second confirmation job returned %v, want SQLSTATE 23505", err)
	}
}

func TestClaimJobPrefersReadyOverExpiredLeases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, "claim")
	db := pgtest.AppDB(t, appDSN)
	booking := pgtest.SeedBooking(t, owner, fixture, "2026-11-02T09:00:00Z")

	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	expired := pgtest.SeedJob(t, owner, fixture.Tenant, booking, "booking_confirmation", now.Add(-time.Hour))
	setJobLease(ctx, t, owner, expired, "dead-worker", now.Add(-time.Minute))
	ready := pgtest.SeedJob(t, owner, fixture.Tenant, booking, "booking_reminder", now.Add(-time.Minute))

	job, err := db.ClaimJob(ctx, "claimer", now, 120)
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if job.ID != ready {
		t.Errorf("claimed %s, want the ready job %s before the expired lease %s", job.ID, ready, expired)
	}
	if job.Kind != domain.JobBookingReminder || job.Attempts != 1 || job.TenantID != fixture.Tenant {
		t.Errorf("claimed job = %+v, want kind reminder attempts 1 tenant %s", job, fixture.Tenant)
	}
	assertJobLease(ctx, t, owner, job.ID, "claimer", now.Add(120*time.Second), 1)

	// With the ready job leased, the next claim reclaims the expired lease and
	// counts the attempt.
	reclaimed, err := db.ClaimJob(ctx, "claimer", now, 120)
	if err != nil {
		t.Fatalf("ClaimJob after the ready job: %v", err)
	}
	if reclaimed.ID != expired || reclaimed.Attempts != 2 {
		t.Errorf("reclaimed = %s attempts=%d, want %s attempts=2", reclaimed.ID, reclaimed.Attempts, expired)
	}
	assertJobLease(ctx, t, owner, expired, "claimer", now.Add(120*time.Second), 2)
}

func TestClaimJobSkipsRowsLockedByAnOpenClaim(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, "skiplock")
	db := pgtest.AppDB(t, appDSN)
	booking := pgtest.SeedBooking(t, owner, fixture, "2026-11-02T09:00:00Z")

	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	first := pgtest.SeedJob(t, owner, fixture.Tenant, booking, "booking_confirmation", now.Add(-time.Minute))
	second := pgtest.SeedJob(t, owner, fixture.Tenant, booking, "booking_reminder", now)

	// Hold an uncommitted claim on the first job. From another session's
	// snapshot it is still ready, so only SKIP LOCKED keeps the second claimer
	// from blocking on it — and from claiming the same row twice.
	holder, heldID := holdClaim(ctx, t, appDSN, "holder", now, 3600)
	if heldID != first {
		t.Fatalf("the holder claimed %s, want the earliest job %s", heldID, first)
	}

	job, err := claimWithin(ctx, t, db, "claimer", now, 3600)
	if err != nil {
		t.Fatalf("ClaimJob: %v", err)
	}
	if job.ID != second {
		t.Errorf("claimed %s, want the unlocked job %s", job.ID, second)
	}

	var doubleClaimed int
	if err := owner.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE attempts > 1").Scan(&doubleClaimed); err != nil {
		t.Fatalf("counting attempts: %v", err)
	}
	if doubleClaimed != 0 {
		t.Errorf("%d jobs were claimed twice", doubleClaimed)
	}

	// The holder's claim belongs to its transaction: rolling back un-leases the
	// row instead of leaving a phantom attempt behind.
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("rolling back the holder: %v", err)
	}
	var attempts int
	if err := owner.QueryRow(ctx, "SELECT attempts FROM jobs WHERE id = $1", first).Scan(&attempts); err != nil {
		t.Fatalf("reading the rolled-back job: %v", err)
	}
	if attempts != 0 {
		t.Errorf("rolled-back job attempts = %d, want 0", attempts)
	}
}

// holdClaim opens a transaction and claims the next job inside it without
// committing, so the row's lock outlives the claim statement.
func holdClaim(ctx context.Context, t *testing.T, dsn, worker string, at time.Time, leaseSeconds int) (pgx.Tx, uuid.UUID) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("opening the holder pool: %v", err)
	}
	t.Cleanup(pool.Close)
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("opening the holder transaction: %v", err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.WithoutCancel(ctx)) })

	var id uuid.UUID
	if err := holder.QueryRow(ctx, jobClaimSQL, worker, at, leaseSeconds).
		Scan(&id, new(uuid.UUID), new(uuid.UUID), new(string), new(int)); err != nil {
		t.Fatalf("holding the first claim: %v", err)
	}
	return holder, id
}

// claimWithin claims in a goroutine so a blocked claim fails the test instead
// of hanging it: waiting on a row another transaction holds is the bug SKIP
// LOCKED exists to prevent.
func claimWithin(ctx context.Context, t *testing.T, db *postgres.DB, worker string, at time.Time, leaseSeconds int) (domain.Job, error) {
	t.Helper()
	claimed := make(chan domain.Job, 1)
	errs := make(chan error, 1)
	go func() {
		job, err := db.ClaimJob(ctx, worker, at, leaseSeconds)
		if err != nil {
			errs <- err
			return
		}
		claimed <- job
	}()
	select {
	case err := <-errs:
		return domain.Job{}, err
	case job := <-claimed:
		return job, nil
	case <-time.After(5 * time.Second):
		t.Fatal("ClaimJob blocked on a row an uncommitted claim holds; SKIP LOCKED is not in effect")
		return domain.Job{}, nil
	}
}

// TestClaimJobUsesThePassedClaimTime pins the one scheduling authority: the
// claim's ready predicate compares run_at against the claim_time argument, not
// against the database's now(). A job seeded 72 hours into the real future is
// invisible to a claim "now" and due the moment claim_time reaches it, which a
// predicate over the database clock could not do.
func TestClaimJobUsesThePassedClaimTime(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, "claim-clock")
	db := pgtest.AppDB(t, appDSN)
	booking := pgtest.SeedBooking(t, owner, fixture, "2026-11-02T09:00:00Z")

	runAt := time.Now().Add(72 * time.Hour).UTC()
	job := pgtest.SeedJob(t, owner, fixture.Tenant, booking, "booking_confirmation", runAt)

	if _, err := db.ClaimJob(ctx, "clock-worker", time.Now().Add(-time.Minute), 60); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("claiming before run_at = %v, want domain.ErrNotFound", err)
	}

	claimed, err := db.ClaimJob(ctx, "clock-worker", runAt.Add(time.Second), 60)
	if err != nil {
		t.Fatalf("ClaimJob once claim_time reaches run_at: %v", err)
	}
	if claimed.ID != job {
		t.Errorf("claimed %s, want %s", claimed.ID, job)
	}
	assertJobLease(ctx, t, owner, job, "clock-worker", runAt.Add(time.Minute+time.Second), 1)
}

// TestStaleTransitionsFromAReclaimedLeaseAreRefused pins the fencing token: a
// claim whose lease expired and was reclaimed must not be able to write, even
// when the reclaimer carries the same worker id. The attempt count is what
// separates the two claims; without it a stale CompleteJob marks a row done
// while the live claimant is still inside its handler.
func TestStaleTransitionsFromAReclaimedLeaseAreRefused(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, "stale")
	db := pgtest.AppDB(t, appDSN)

	starts := []string{
		"2026-11-02T09:00:00Z",
		"2026-11-03T09:00:00Z",
		"2026-11-04T09:00:00Z",
		"2026-11-05T09:00:00Z",
	}
	for i, name := range []string{"complete", "retry", "dead-letter", "release"} {
		booking := pgtest.SeedBooking(t, owner, fixture, starts[i])
		jobID := pgtest.SeedJob(t, owner, fixture.Tenant, booking, "booking_confirmation", staleClaimAt())
		assertStaleClaimIsRefused(ctx, t, db, owner, name, jobID)
	}
}

// staleClaimAt is when the tests' first claims happen.
func staleClaimAt() time.Time {
	return time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
}

// assertStaleClaimIsRefused claims a job twice under the same worker id: the
// second claim reclaims the first's expired lease, so the first's guarded write
// must be refused and the second's must apply.
func assertStaleClaimIsRefused(ctx context.Context, t *testing.T, db *postgres.DB, owner *pgxpool.Pool, name string, jobID uuid.UUID) {
	t.Helper()
	first, second := reclaimedTwice(ctx, t, db, name, jobID)

	applied, err := applyTransition(ctx, db, name, "recycled", first)
	if err != nil || applied {
		t.Errorf("%s of a stale claim = %v, %v, want false, nil", name, applied, err)
	}
	row := readJobByID(ctx, t, owner, first.ID)
	if row.status != "running" || row.attempts != 2 || row.lockedBy != "recycled" {
		t.Errorf("%s: job after the stale write = %+v, want running attempts=2 held by the reclaim", name, row)
	}

	applied, err = applyTransition(ctx, db, name, "recycled", second)
	if err != nil || !applied {
		t.Errorf("%s of the live claim = %v, %v, want true, nil", name, applied, err)
	}
}

// reclaimedTwice claims a job at t0 and reclaims it once the first lease has
// expired, both times under the same worker id, and returns both claims.
func reclaimedTwice(ctx context.Context, t *testing.T, db *postgres.DB, name string, jobID uuid.UUID) (first, second domain.Job) {
	t.Helper()
	t0 := staleClaimAt()

	first, err := db.ClaimJob(ctx, "recycled", t0, 60)
	if err != nil {
		t.Fatalf("%s: first claim: %v", name, err)
	}
	if first.ID != jobID {
		t.Fatalf("%s: first claim returned %s, want the seeded job %s", name, first.ID, jobID)
	}
	second, err = db.ClaimJob(ctx, "recycled", t0.Add(61*time.Second), 60)
	if err != nil {
		t.Fatalf("%s: reclaim: %v", name, err)
	}
	if second.ID != first.ID || second.Attempts != 2 {
		t.Fatalf("%s: reclaim = %s attempts=%d, want %s attempts=2", name, second.ID, second.Attempts, first.ID)
	}
	return first, second
}

// applyTransition runs one of the four guarded writes by name.
func applyTransition(ctx context.Context, db *postgres.DB, name, worker string, job domain.Job) (bool, error) {
	switch name {
	case "complete":
		return db.CompleteJob(ctx, worker, job)
	case "retry":
		return db.RetryJob(ctx, worker, job, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), "stale claim")
	case "dead-letter":
		return db.DeadLetterJob(ctx, worker, job, "stale claim")
	default:
		return db.ReleaseJob(ctx, worker, job)
	}
}

func TestClaimJobEmptyQueueIsNotFound(t *testing.T) {
	t.Parallel()
	appDSN, _ := pgtest.Start(t)
	db := pgtest.AppDB(t, appDSN)

	_, err := db.ClaimJob(t.Context(), "claimer", time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), 60)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("ClaimJob on an empty queue = %v, want domain.ErrNotFound", err)
	}
}

// transitionsFixture is one throwaway Postgres with a tenant, and seeding a
// claimed job inside it: each test gets its own container, so a bare claim
// always finds the job the test just queued.
type transitionsFixture struct {
	owner   *pgxpool.Pool
	db      *postgres.DB
	fixture pgtest.Fixture
	now     time.Time
}

func newTransitionsFixture(t *testing.T, slug string) transitionsFixture {
	t.Helper()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, slug)
	return transitionsFixture{
		owner:   owner,
		db:      pgtest.AppDB(t, appDSN),
		fixture: fixture,
		now:     time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
	}
}

// seedClaimedJob queues a confirmation job for a fresh booking and claims it.
func (f transitionsFixture) seedClaimedJob(t *testing.T, worker string) domain.Job {
	t.Helper()
	// A fresh booking per call: the enqueue's unique key allows one job of a
	// kind per booking, and SeedBooking keys itself on the start instant.
	booking := pgtest.SeedBooking(t, f.owner, f.fixture, "2026-11-02T09:00:00Z")
	pgtest.SeedJob(t, f.owner, f.fixture.Tenant, booking, "booking_confirmation", f.now)

	job, err := f.db.ClaimJob(t.Context(), worker, f.now, 60)
	if err != nil {
		t.Fatalf("ClaimJob for %s: %v", worker, err)
	}
	return job
}

func TestCompleteJobAppliesOnlyWhileTheLeaseIsHeld(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTransitionsFixture(t, "complete")
	job := f.seedClaimedJob(t, "w-complete")

	if applied, err := f.db.CompleteJob(ctx, "w-complete", job); err != nil || !applied {
		t.Fatalf("CompleteJob = %v, %v, want true, nil", applied, err)
	}
	assertJobFields(ctx, t, f.owner, job.ID, "done", 1, "", time.Time{})
	if applied, err := f.db.CompleteJob(ctx, "w-complete", job); err != nil || applied {
		t.Errorf("second CompleteJob = %v, %v, want false, nil", applied, err)
	}
}

func TestRetryJobAppliesOnlyWhileTheLeaseIsHeld(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTransitionsFixture(t, "retry")
	job := f.seedClaimedJob(t, "w-retry")

	runAt := f.now.Add(30 * time.Second)
	if applied, err := f.db.RetryJob(ctx, "w-retry", job, runAt, "smtp refused"); err != nil || !applied {
		t.Fatalf("RetryJob = %v, %v, want true, nil", applied, err)
	}
	row := readJobByID(ctx, t, f.owner, job.ID)
	if row.status != "ready" || !row.runAt.Equal(runAt) || row.lastError != "smtp refused" {
		t.Errorf("retried job = %+v, want ready at %v with the failure kept", row, runAt)
	}
	if applied, err := f.db.CompleteJob(ctx, "w-retry", job); err != nil {
		t.Fatalf("CompleteJob on a moved lease: %v", err)
	} else if applied {
		t.Error("a worker whose lease moved on completed the job")
	}
}

func TestDeadLetterJobKeepsTheReasonAndStaysTerminal(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTransitionsFixture(t, "dead")
	job := f.seedClaimedJob(t, "w-dead")

	if applied, err := f.db.DeadLetterJob(ctx, "w-dead", job, "attempt budget spent"); err != nil || !applied {
		t.Fatalf("DeadLetterJob = %v, %v, want true, nil", applied, err)
	}
	row := readJobByID(ctx, t, f.owner, job.ID)
	if row.status != "dead" || row.lastError != "attempt budget spent" {
		t.Errorf("dead job = %+v, want dead with the reason kept", row)
	}
	if _, err := f.db.ClaimJob(ctx, "later-worker", f.now, 60); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("claiming after the dead letter = %v, want domain.ErrNotFound", err)
	}
}

// TestReleaseJobRefundsTheAttempt pins the shutdown path's queue contract: the
// job returns to ready with its run_at untouched and the claim's attempt
// refunded, so a deploy does not spend the budget.
func TestReleaseJobRefundsTheAttempt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTransitionsFixture(t, "release")
	job := f.seedClaimedJob(t, "w-release")
	if job.Attempts != 1 {
		t.Fatalf("claimed attempts = %d, want 1", job.Attempts)
	}

	if applied, err := f.db.ReleaseJob(ctx, "w-release", job); err != nil || !applied {
		t.Fatalf("ReleaseJob = %v, %v, want true, nil", applied, err)
	}
	row := readJobByID(ctx, t, f.owner, job.ID)
	if row.status != "ready" || row.attempts != 0 {
		t.Errorf("released job = %+v, want ready with the attempt refunded", row)
	}
	if !row.runAt.Equal(f.now) {
		t.Errorf("released run_at = %v, want its due time %v unchanged", row.runAt, f.now)
	}

	reclaimed, err := f.db.ClaimJob(ctx, "w-release", f.now, 60)
	if err != nil {
		t.Fatalf("ClaimJob after the release: %v", err)
	}
	if reclaimed.ID != job.ID || reclaimed.Attempts != 1 {
		t.Errorf("reclaim after release = %s attempts=%d, want %s attempts=1", reclaimed.ID, reclaimed.Attempts, job.ID)
	}
}

// TestClaimJobCrossesTenantsByDesign pins the one cross-tenant operation the
// application role can perform: the worker has no tenant, so job_claim returns
// the next due row for every business. Direct reads stay tenant-scoped, which
// the isolation test proves.
func TestClaimJobCrossesTenantsByDesign(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	tenantA := pgtest.Seed(t, owner, "a")
	tenantB := pgtest.Seed(t, owner, "b")
	bookingA := pgtest.SeedBooking(t, owner, tenantA, "2026-11-02T09:00:00Z")
	bookingB := pgtest.SeedBooking(t, owner, tenantB, "2026-11-02T09:00:00Z")
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	jobA := pgtest.SeedJob(t, owner, tenantA.Tenant, bookingA, "booking_confirmation", now)
	jobB := pgtest.SeedJob(t, owner, tenantB.Tenant, bookingB, "booking_confirmation", now)

	db := pgtest.AppDB(t, appDSN)
	claimed := map[uuid.UUID]uuid.UUID{}
	for range 2 {
		job, err := db.ClaimJob(ctx, "cross-tenant", now, 60)
		if err != nil {
			t.Fatalf("ClaimJob: %v", err)
		}
		claimed[job.ID] = job.TenantID
	}

	if claimed[jobA] != tenantA.Tenant || claimed[jobB] != tenantB.Tenant {
		t.Errorf("claim returned tenant map %v, want %s for %s and %s for %s",
			claimed, tenantA.Tenant, jobA, tenantB.Tenant, jobB)
	}
}

// poisonJobInserts makes every insert into jobs fail until the test ends, which
// is how the atomicity test proves the enqueue shares the booking transaction.
func poisonJobInserts(t *testing.T, owner *pgxpool.Pool) {
	t.Helper()
	_, err := owner.Exec(t.Context(), `
		CREATE FUNCTION test_poison_jobs() RETURNS trigger LANGUAGE plpgsql
		AS $$ BEGIN RAISE EXCEPTION 'poisoned job insert'; END $$;
		CREATE TRIGGER test_poison_jobs BEFORE INSERT ON jobs
		    FOR EACH ROW EXECUTE FUNCTION test_poison_jobs();`)
	if err != nil {
		t.Fatalf("installing the poison trigger: %v", err)
	}
	t.Cleanup(func() {
		_, err := owner.Exec(context.WithoutCancel(t.Context()), `
			DROP TRIGGER IF EXISTS test_poison_jobs ON jobs;
			DROP FUNCTION IF EXISTS test_poison_jobs();`)
		if err != nil {
			t.Errorf("removing the poison trigger: %v", err)
		}
	})
}

// setJobLease turns a seeded job into an expired lease as the owner, which
// bypasses RLS.
func setJobLease(ctx context.Context, t *testing.T, owner *pgxpool.Pool, jobID uuid.UUID, worker string, until time.Time) {
	t.Helper()
	_, err := owner.Exec(ctx, `
		UPDATE jobs SET status = 'running', locked_by = $2, locked_until = $3, attempts = 1
		 WHERE id = $1`, jobID, worker, until)
	if err != nil {
		t.Fatalf("setting job lease: %v", err)
	}
}

// assertJobLease pins the lease a claim wrote.
func assertJobLease(ctx context.Context, t *testing.T, owner *pgxpool.Pool, jobID uuid.UUID, worker string, until time.Time, attempts int) {
	t.Helper()
	row := readJobByID(ctx, t, owner, jobID)
	if row.status != "running" || row.lockedBy != worker || row.attempts != attempts {
		t.Errorf("job %s = %+v, want running by %s attempts=%d", jobID, row, worker, attempts)
	}
	if !row.lockedUntil.Equal(until) {
		t.Errorf("job %s locked_until = %v, want %v", jobID, row.lockedUntil, until)
	}
}

// assertJobFields pins a terminal or requeued row's shape.
func assertJobFields(ctx context.Context, t *testing.T, owner *pgxpool.Pool, jobID uuid.UUID, status string, attempts int, lockedBy string, lockedUntil time.Time) {
	t.Helper()
	row := readJobByID(ctx, t, owner, jobID)
	if row.status != status || row.attempts != attempts || row.lockedBy != lockedBy {
		t.Errorf("job %s = %+v, want status %s attempts %d locked_by %q", jobID, row, status, attempts, lockedBy)
	}
	if !row.lockedUntil.Equal(lockedUntil) {
		t.Errorf("job %s locked_until = %v, want %v", jobID, row.lockedUntil, lockedUntil)
	}
}

// jobRow is one queue row as the tests assert it, read as the owner.
type jobRow struct {
	status, lockedBy, lastError string
	attempts                    int
	runAt, lockedUntil          time.Time
}

func readJob(t *testing.T, owner *pgxpool.Pool, tenantID uuid.UUID, kind string) jobRow {
	t.Helper()
	var (
		row         jobRow
		lockedUntil *time.Time
	)
	err := owner.QueryRow(t.Context(), `
		SELECT status, attempts, run_at, COALESCE(locked_by, ''), locked_until, COALESCE(last_error, '')
		  FROM jobs WHERE tenant_id = $1 AND kind = $2`, tenantID, kind).
		Scan(&row.status, &row.attempts, &row.runAt, &row.lockedBy, &lockedUntil, &row.lastError)
	if err != nil {
		t.Fatalf("reading %s job: %v", kind, err)
	}
	if lockedUntil != nil {
		row.lockedUntil = *lockedUntil
	}
	return row
}

func readJobByID(ctx context.Context, t *testing.T, owner *pgxpool.Pool, id uuid.UUID) jobRow {
	t.Helper()
	var (
		row         jobRow
		lockedUntil *time.Time
	)
	err := owner.QueryRow(ctx, `
		SELECT status, attempts, run_at, COALESCE(locked_by, ''), locked_until, COALESCE(last_error, '')
		  FROM jobs WHERE id = $1`, id).
		Scan(&row.status, &row.attempts, &row.runAt, &row.lockedBy, &lockedUntil, &row.lastError)
	if err != nil {
		t.Fatalf("reading job %s: %v", id, err)
	}
	if lockedUntil != nil {
		row.lockedUntil = *lockedUntil
	}
	return row
}

func countJobs(t *testing.T, owner *pgxpool.Pool, tenantID uuid.UUID) int {
	t.Helper()
	var count int
	if err := owner.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE tenant_id = $1`, tenantID).Scan(&count); err != nil {
		t.Fatalf("counting jobs: %v", err)
	}
	return count
}

func jobKindCount(t *testing.T, owner *pgxpool.Pool, tenantID uuid.UUID, kind string) int {
	t.Helper()
	var count int
	if err := owner.QueryRow(t.Context(), `SELECT count(*) FROM jobs WHERE tenant_id = $1 AND kind = $2`, tenantID, kind).Scan(&count); err != nil {
		t.Fatalf("counting %s jobs: %v", kind, err)
	}
	return count
}
