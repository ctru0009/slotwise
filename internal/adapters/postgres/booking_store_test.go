//go:build integration

package postgres_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// testCancelSecret is long enough for app.NewCancelSigner.
const testCancelSecret = "integration-cancel-secret-32-bytes-plus"

// bookingFixture is one Berlin tenant whose staff member works Monday
// 09:00–12:00, with a service that blocks 40 minutes: 30 of appointment and 10
// of buffer.
type bookingFixture struct {
	fixture pgtest.Fixture
	owner   *pgxpool.Pool
	appPool *pgxpool.Pool
	db      *postgres.DB
	useCase *app.Bookings
}

func newBookingFixture(t *testing.T, slug string) bookingFixture {
	t.Helper()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, slug)
	setServiceBuffer(t, owner, fixture, 10)
	pgtest.SeedWeeklyRule(t, owner, fixture, int(time.Monday), 9*60, 12*60)

	db := pgtest.AppDB(t, appDSN)
	signer, err := app.NewCancelSigner(testCancelSecret)
	if err != nil {
		t.Fatalf("building the signer: %v", err)
	}
	// The clock is parked before the fixture date so NotBefore filters nothing.
	clk := clock.NewFake(time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC))
	return bookingFixture{
		fixture: fixture,
		owner:   owner,
		appPool: pgtest.AppPool(t, appDSN),
		db:      db,
		useCase: app.NewBookings(db, db, db, clk, signer),
	}
}

// bookingStart is 09:00 Berlin on Monday 2026-11-02, the start every test in
// this file books.
func bookingStart(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("loading Berlin: %v", err)
	}
	return time.Date(2026, time.November, 2, 9, 0, 0, 0, loc)
}

func bookingInput(t *testing.T, f pgtest.Fixture, key string) app.BookingInput {
	t.Helper()
	return app.BookingInput{
		ServiceID:      f.Service,
		StaffID:        f.Staff,
		StartsAt:       bookingStart(t),
		CustomerName:   "Ada Lovelace",
		CustomerEmail:  "ada@example.com",
		IdempotencyKey: key,
	}
}

// TestConcurrentBookingCreatesHaveOneWinner drives 100 goroutines through the
// use case, all for one slot with distinct keys: the lock plus the exclusion
// constraint must pick exactly one winner and report the rest as taken, not as
// a deadlock or an infrastructure error.
func TestConcurrentBookingCreatesHaveOneWinner(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "race")

	const attempts = 100
	in := bookingInput(t, f.fixture, "")

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     int
		taken    int
		winner   uuid.UUID
		failures []error
	)
	for i := range attempts {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			attempt := in
			attempt.IdempotencyKey = fmt.Sprintf("race-%d", n)
			booking, err := f.useCase.Create(ctx, f.fixture.Tenant, attempt)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
				winner = booking.ID
			case errors.Is(err, domain.ErrSlotTaken):
				taken++
			default:
				failures = append(failures, err)
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Errorf("%d attempts booked the slot, want exactly 1", wins)
	}
	if taken != attempts-1 {
		t.Errorf("%d attempts returned ErrSlotTaken, want %d", taken, attempts-1)
	}
	if len(failures) > 0 {
		t.Errorf("unexpected failures (%d), first: %v", len(failures), failures[0])
	}
	if got := countBookings(t, f.owner, f.fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows, want 1", got)
	}
	// The stored span is the occupied end, buffer included: the constraint
	// enforces the buffer only if the writer stored it.
	if got := bookingSpan(t, f.owner, winner); got != 40*time.Minute {
		t.Errorf("stored span = %v, want 40m (duration plus buffer)", got)
	}
}

func TestCreateBookingIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "idem")
	in := bookingInput(t, f.fixture, "key-1")

	first, err := f.useCase.Create(ctx, f.fixture.Tenant, in)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := f.useCase.Create(ctx, f.fixture.Tenant, in)
	if err != nil {
		t.Fatalf("replayed Create: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("the replay returned booking %s, want the stored %s", second.ID, first.ID)
	}
	if got := countBookings(t, f.owner, f.fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows after the replay, want 1", got)
	}

	// Cancelling does not reopen the key: the replay returns the cancelled
	// booking instead of booking the slot again.
	if err := f.useCase.Cancel(ctx, f.fixture.Tenant, first.ID, f.useCase.CancelToken(first.ID)); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	replayed, err := f.useCase.Create(ctx, f.fixture.Tenant, in)
	if err != nil {
		t.Fatalf("Create after cancelling: %v", err)
	}
	if replayed.ID != first.ID {
		t.Errorf("the replay after cancelling returned %s, want %s", replayed.ID, first.ID)
	}
	if replayed.Status != domain.BookingCancelled {
		t.Errorf("the replay returned status %q, want %q", replayed.Status, domain.BookingCancelled)
	}
	if got := countBookings(t, f.owner, f.fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows after the replay, want 1", got)
	}
}

// TestConcurrentSameKeyCreatesConverge is the other half of the race: one key
// asked for twenty times must converge on one booking, not report nineteen
// conflicts. Without the ON CONFLICT path the losers would hit the exclusion
// constraint and surface as ErrSlotTaken.
func TestConcurrentSameKeyCreatesConverge(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "same-key")

	const callers = 20
	in := bookingInput(t, f.fixture, "shared-key")

	var wg sync.WaitGroup
	ids := make([]uuid.UUID, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			booking, err := f.useCase.Create(ctx, f.fixture.Tenant, in)
			ids[n], errs[n] = booking.ID, err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	for i, id := range ids {
		if id != ids[0] {
			t.Errorf("caller %d booked %s, want the first caller's %s", i, id, ids[0])
		}
	}
	if got := countBookings(t, f.owner, f.fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows, want 1", got)
	}
}

func TestCreateBookingOnATakenSlotIsSlotTaken(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "taken")
	pgtest.SeedBookingSpan(t, f.owner, f.fixture, "2026-11-02T08:00:00Z", 40)

	_, err := f.useCase.Create(ctx, f.fixture.Tenant, bookingInput(t, f.fixture, "key-1"))
	if !errors.Is(err, domain.ErrSlotTaken) {
		t.Fatalf("Create on a taken slot = %v, want domain.ErrSlotTaken", err)
	}
	if got := countBookings(t, f.owner, f.fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows, want only the seeded one", got)
	}
}

func TestCreateBookingForInvisibleOrInactiveStaffIsNotFound(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "invisible")
	other := pgtest.Seed(t, f.owner, "other")

	// Another tenant's staff member is invisible: the roster the snapshot
	// returns never contains it.
	in := bookingInput(t, f.fixture, "cross-tenant")
	in.StaffID = other.Staff
	if _, err := f.useCase.Create(ctx, f.fixture.Tenant, in); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Create for another tenant's staff = %v, want domain.ErrNotFound", err)
	}

	// Deactivating the staff member takes them out of the roster too.
	if _, err := f.owner.Exec(ctx, `UPDATE staff SET active = false WHERE id = $1::uuid`, f.fixture.Staff.String()); err != nil {
		t.Fatalf("deactivating the staff member: %v", err)
	}
	if _, err := f.useCase.Create(ctx, f.fixture.Tenant, bookingInput(t, f.fixture, "inactive")); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Create for a deactivated staff member = %v, want domain.ErrNotFound", err)
	}

	// The store's own lock is the last line: a caller that reaches it directly
	// cannot book a deactivated staff member either.
	start := bookingStart(t)
	_, err := f.db.CreateBooking(ctx, f.fixture.Tenant, app.BookingWrite{
		StaffID:        f.fixture.Staff,
		ServiceID:      f.fixture.Service,
		CustomerName:   "Ada Lovelace",
		CustomerEmail:  "ada@example.com",
		StartsAt:       start,
		EndsAt:         start.Add(40 * time.Minute),
		IdempotencyKey: "direct",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("CreateBooking for a deactivated staff member = %v, want domain.ErrNotFound", err)
	}
	if got := countBookings(t, f.owner, f.fixture.Tenant); got != 0 {
		t.Errorf("%d booking rows, want 0", got)
	}
}

func TestBookingByIdempotencyKeyIsTenantScoped(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "key-a")
	other := pgtest.Seed(t, f.owner, "key-b")

	booking, err := f.useCase.Create(ctx, f.fixture.Tenant, bookingInput(t, f.fixture, "shared-key"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored, err := f.db.BookingByIdempotencyKey(ctx, f.fixture.Tenant, "shared-key")
	if err != nil {
		t.Fatalf("BookingByIdempotencyKey in its own tenant: %v", err)
	}
	if stored.ID != booking.ID {
		t.Errorf("read booking %s, want %s", stored.ID, booking.ID)
	}
	if _, err := f.db.BookingByIdempotencyKey(ctx, other.Tenant, "shared-key"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("BookingByIdempotencyKey for another tenant = %v, want domain.ErrNotFound", err)
	}
}

func TestBookingByIDIsTenantScoped(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "id-a")
	other := pgtest.Seed(t, f.owner, "id-b")

	booking, err := f.useCase.Create(ctx, f.fixture.Tenant, bookingInput(t, f.fixture, "key-1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored, err := f.db.BookingByID(ctx, f.fixture.Tenant, booking.ID)
	if err != nil {
		t.Fatalf("BookingByID in its own tenant: %v", err)
	}
	if stored.ID != booking.ID || stored.Status != domain.BookingConfirmed {
		t.Errorf("read %s (%s), want %s (confirmed)", stored.ID, stored.Status, booking.ID)
	}
	if _, err := f.db.BookingByID(ctx, other.Tenant, booking.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("BookingByID for another tenant = %v, want domain.ErrNotFound", err)
	}
}

func TestCancelBookingIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "cancel")
	other := pgtest.Seed(t, f.owner, "cancel-other")

	booking, err := f.useCase.Create(ctx, f.fixture.Tenant, bookingInput(t, f.fixture, "key-1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.db.CancelBooking(ctx, f.fixture.Tenant, booking.ID); err != nil {
		t.Fatalf("CancelBooking: %v", err)
	}
	if err := f.db.CancelBooking(ctx, f.fixture.Tenant, booking.ID); err != nil {
		t.Fatalf("repeated CancelBooking: %v", err)
	}
	if got := bookingStatus(t, f.owner, booking.ID); got != domain.BookingCancelled {
		t.Errorf("stored status = %q, want %q", got, domain.BookingCancelled)
	}

	if err := f.db.CancelBooking(ctx, other.Tenant, booking.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("CancelBooking for another tenant = %v, want domain.ErrNotFound", err)
	}
	if err := f.db.CancelBooking(ctx, f.fixture.Tenant, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("CancelBooking of an unknown booking = %v, want domain.ErrNotFound", err)
	}
	if got := bookingStatus(t, f.owner, booking.ID); got != domain.BookingCancelled {
		t.Errorf("stored status after the refusals = %q, want %q", got, domain.BookingCancelled)
	}

	// The constraint only covers confirmed rows, so a cancelled window is
	// bookable again — with a new key, since the old one still replays.
	again, err := f.useCase.Create(ctx, f.fixture.Tenant, bookingInput(t, f.fixture, "key-2"))
	if err != nil {
		t.Fatalf("Create on the cancelled window: %v", err)
	}
	if again.ID == booking.ID {
		t.Errorf("the rebooking returned the cancelled booking %s", again.ID)
	}
	if again.Status != domain.BookingConfirmed {
		t.Errorf("the rebooking has status %q, want %q", again.Status, domain.BookingConfirmed)
	}
	if got := countBookings(t, f.owner, f.fixture.Tenant); got != 2 {
		t.Errorf("%d booking rows, want 2", got)
	}
}

// TestCreateBookingTakesTheStaffRowLock pins the write path's first statement:
// with the staff row held elsewhere, a booking waits on that row instead of
// inserting, so two writers for one slot queue rather than deadlocking on the
// exclusion constraint's index. The waiter is identified by its statement, not
// by any lock wait: an insert would also wait, on the foreign key's key-share
// lock, which is exactly the queueing this lock exists to replace.
func TestCreateBookingTakesTheStaffRowLock(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "staff-lock")

	holder, err := f.appPool.Begin(ctx)
	if err != nil {
		t.Fatalf("beginning the holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Rollback(ctx) })
	if _, err := holder.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", f.fixture.Tenant.String()); err != nil {
		t.Fatalf("setting the holder's tenant: %v", err)
	}
	if _, err := holder.Exec(ctx, "SELECT 1 FROM staff WHERE id = $1 FOR UPDATE", f.fixture.Staff); err != nil {
		t.Fatalf("locking the staff row: %v", err)
	}

	start := bookingStart(t)
	done := make(chan error, 1)
	go func() {
		_, err := f.db.CreateBooking(ctx, f.fixture.Tenant, app.BookingWrite{
			StaffID:        f.fixture.Staff,
			ServiceID:      f.fixture.Service,
			CustomerName:   "Ada Lovelace",
			CustomerEmail:  "ada@example.com",
			StartsAt:       start,
			EndsAt:         start.Add(40 * time.Minute),
			IdempotencyKey: "locked",
		})
		done <- err
	}()

	waitForStaffLockWaiter(t, f.owner)
	if got := countBookings(t, f.owner, f.fixture.Tenant); got != 0 {
		t.Errorf("%d booking rows while the lock is held, want 0", got)
	}

	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("releasing the lock: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CreateBooking after the lock was released: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CreateBooking did not finish after the lock was released")
	}
	if got := countBookings(t, f.owner, f.fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows after the lock was released, want 1", got)
	}
}

// waitForStaffLockWaiter polls until a backend is waiting inside a FOR UPDATE
// statement, which is the booking write queued behind the held staff row.
func waitForStaffLockWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		err := pool.QueryRow(t.Context(), `
			SELECT count(*)
			  FROM pg_stat_activity
			 WHERE wait_event_type = 'Lock' AND query ILIKE '%FOR UPDATE%'`).Scan(&waiting)
		if err != nil {
			t.Fatalf("counting lock waiters: %v", err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no backend waited inside a FOR UPDATE statement within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// setServiceBuffer gives the fixture's service a buffer, so a stored ends_at
// that ignored it would fail the span assertions.
func setServiceBuffer(t *testing.T, owner *pgxpool.Pool, f pgtest.Fixture, minutes int) {
	t.Helper()
	_, err := owner.Exec(t.Context(), `UPDATE services SET buffer_minutes = $2 WHERE id = $1::uuid`, f.Service.String(), minutes)
	if err != nil {
		t.Fatalf("setting the service buffer: %v", err)
	}
}

// countBookings counts a tenant's bookings as the owner pool, bypassing RLS.
func countBookings(t *testing.T, owner *pgxpool.Pool, tenantID uuid.UUID) int {
	t.Helper()
	var count int
	err := owner.QueryRow(t.Context(), `SELECT count(*) FROM bookings WHERE tenant_id = $1::uuid`, tenantID.String()).Scan(&count)
	if err != nil {
		t.Fatalf("counting bookings: %v", err)
	}
	return count
}

// bookingSpan reads one booking's occupied length as the owner pool.
func bookingSpan(t *testing.T, owner *pgxpool.Pool, id uuid.UUID) time.Duration {
	t.Helper()
	var span time.Duration
	err := owner.QueryRow(t.Context(), `SELECT ends_at - starts_at FROM bookings WHERE id = $1::uuid`, id.String()).Scan(&span)
	if err != nil {
		t.Fatalf("reading the booking span: %v", err)
	}
	return span
}

// bookingStatus reads one booking's status as the owner pool.
func bookingStatus(t *testing.T, owner *pgxpool.Pool, id uuid.UUID) domain.BookingStatus {
	t.Helper()
	var status string
	err := owner.QueryRow(t.Context(), `SELECT status FROM bookings WHERE id = $1::uuid`, id.String()).Scan(&status)
	if err != nil {
		t.Fatalf("reading the booking status: %v", err)
	}
	return domain.BookingStatus(status)
}
