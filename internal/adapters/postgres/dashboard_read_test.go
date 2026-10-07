//go:build integration

package postgres_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// TestListBookingsInRangeIsScopedAndOrdered pins the calendar read: one query
// returns the tenant's own bookings in start order with both display names
// joined in, and the interval test is half-open — a booking that ends exactly
// at the range start or starts exactly at the range end is out, while one that
// started before the range and still occupies it is in.
func TestListBookingsInRangeIsScopedAndOrdered(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	tenantA := pgtest.Seed(t, owner, "a")
	tenantB := pgtest.Seed(t, owner, "b")
	extraStaff := pgtest.SeedStaff(t, owner, tenantA, "Staff Extra", "extra@example.com")
	extra := pgtest.Fixture{Tenant: tenantA.Tenant, Staff: extraStaff, Service: tenantA.Service}

	rangeStart := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	rangeEnd := rangeStart.Add(3 * time.Hour)

	// Seeded out of start order, so the result's order can only come from the
	// query.
	late := pgtest.SeedBookingSpan(t, owner, tenantA, "2026-11-02T11:00:00Z", 30)
	early := pgtest.SeedBookingSpan(t, owner, tenantA, "2026-11-02T09:30:00Z", 30)
	foreign := pgtest.SeedBookingSpan(t, owner, tenantB, "2026-11-02T10:00:00Z", 30)

	// The half-open boundaries. The occupying booking sits on a second staff
	// member so the per-staff exclusion constraint does not make the cases
	// fight each other.
	endsAtStart := pgtest.SeedBookingSpan(t, owner, tenantA, "2026-11-02T08:30:00Z", 30)
	overlapping := pgtest.SeedBookingSpan(t, owner, extra, "2026-11-02T08:45:00Z", 30)
	atEnd := pgtest.SeedBookingSpan(t, owner, extra, "2026-11-02T12:00:00Z", 30)

	db := pgtest.AppDB(t, appDSN)
	bookings, err := db.ListBookingsInRange(ctx, tenantA.Tenant, rangeStart, rangeEnd)
	if err != nil {
		t.Fatalf("ListBookingsInRange: %v", err)
	}

	gotIDs := make([]uuid.UUID, 0, len(bookings))
	for _, booking := range bookings {
		gotIDs = append(gotIDs, booking.ID)
	}
	wantIDs := []uuid.UUID{overlapping, early, late}
	assertBookingIDsInStartOrder(t, gotIDs, wantIDs, []uuid.UUID{endsAtStart, atEnd}, rangeStart, rangeEnd)
	assertBookingRows(t, bookings, tenantA, extraStaff)

	foreignRead, err := db.ListBookingsInRange(ctx, tenantB.Tenant, rangeStart, rangeEnd)
	if err != nil {
		t.Fatalf("tenant B's ListBookingsInRange: %v", err)
	}
	if len(foreignRead) != 1 || foreignRead[0].ID != foreign {
		t.Errorf("tenant B reads %d bookings (%v), want only its own %s", len(foreignRead), foreignRead, foreign)
	}
}

// assertBookingIDsInStartOrder checks the returned ids match wantIDs in start
// order and that the half-open boundaries are excluded.
func assertBookingIDsInStartOrder(t *testing.T, gotIDs, wantIDs, excluded []uuid.UUID, rangeStart, rangeEnd time.Time) {
	t.Helper()
	if !slices.Equal(gotIDs, wantIDs) {
		t.Errorf("tenant A reads %v, want %v in start order: the range is half-open and tenant-scoped", gotIDs, wantIDs)
	}
	for _, id := range excluded {
		if slices.Contains(gotIDs, id) {
			t.Errorf("booking %s appears in [%s, %s) although the range excludes it", id, rangeStart, rangeEnd)
		}
	}
}

// assertBookingRows checks every returned booking carries the seeded staff,
// service and status the join is expected to have filled in, in start order.
func assertBookingRows(t *testing.T, bookings []app.BookingInRange, tenantA pgtest.Fixture, extraStaff uuid.UUID) {
	t.Helper()
	wantStaff := []struct {
		id   uuid.UUID
		name string
	}{
		{extraStaff, "Staff Extra"},
		{tenantA.Staff, "Staff a"},
		{tenantA.Staff, "Staff a"},
	}
	if len(bookings) != len(wantStaff) {
		return
	}
	for i, booking := range bookings {
		if booking.StaffID != wantStaff[i].id || booking.ServiceID != tenantA.Service {
			t.Errorf("booking %s refers to staff %s service %s, want staff %s service %s",
				booking.ID, booking.StaffID, booking.ServiceID, wantStaff[i].id, tenantA.Service)
		}
		if booking.StaffName != wantStaff[i].name || booking.ServiceName != "Cut a" {
			t.Errorf("booking %s names %q at %q, want %q at %q",
				booking.ID, booking.StaffName, booking.ServiceName, wantStaff[i].name, "Cut a")
		}
		if booking.Status != domain.BookingConfirmed {
			t.Errorf("booking %s status = %q, want %q", booking.ID, booking.Status, domain.BookingConfirmed)
		}
	}
}

// TestListBookingsInRangeIsNeverNil pins the empty result's shape: a range
// holding no bookings returns an allocated empty slice, so callers may range
// over it without a nil check.
func TestListBookingsInRangeIsNeverNil(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	empty := pgtest.Seed(t, owner, "empty")
	elsewhere := pgtest.Seed(t, owner, "elsewhere")
	pgtest.SeedBooking(t, owner, elsewhere, "2026-11-03T09:00:00Z")

	db := pgtest.AppDB(t, appDSN)
	from := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	to := from.Add(3 * time.Hour)

	for _, tc := range []struct {
		name   string
		tenant uuid.UUID
	}{
		{"tenant with no bookings", empty.Tenant},
		{"booking outside the range", elsewhere.Tenant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bookings, err := db.ListBookingsInRange(ctx, tc.tenant, from, to)
			if err != nil {
				t.Fatalf("ListBookingsInRange: %v", err)
			}
			if bookings == nil {
				t.Fatal("ListBookingsInRange returned nil, want an allocated empty slice")
			}
			if len(bookings) != 0 {
				t.Errorf("%d bookings, want none", len(bookings))
			}
		})
	}
}

// jobDueAt is when every job seeded in this file is due. It is a fixed instant
// so run_at cannot influence the ordering assertion.
func jobDueAt() time.Time {
	return time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
}

// TestListDeadJobsIsScoped pins the dead-letter list: newest first by when the
// job died, capped by the limit, scoped to the tenant, and filtered to dead
// rows only — a ready or done job is not a dead letter.
func TestListDeadJobsIsScoped(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	tenantA := pgtest.Seed(t, owner, "a")
	tenantB := pgtest.Seed(t, owner, "b")

	bookingA1 := pgtest.SeedBooking(t, owner, tenantA, "2026-11-02T09:00:00Z")
	bookingA2 := pgtest.SeedBooking(t, owner, tenantA, "2026-11-02T10:00:00Z")
	bookingA3 := pgtest.SeedBooking(t, owner, tenantA, "2026-11-02T11:00:00Z")
	bookingA4 := pgtest.SeedBooking(t, owner, tenantA, "2026-11-02T12:00:00Z")
	bookingB := pgtest.SeedBooking(t, owner, tenantB, "2026-11-02T09:00:00Z")

	older := pgtest.SeedJob(t, owner, tenantA.Tenant, bookingA1, "booking_confirmation", jobDueAt())
	newer := pgtest.SeedJob(t, owner, tenantA.Tenant, bookingA2, "booking_reminder", jobDueAt())
	ready := pgtest.SeedJob(t, owner, tenantA.Tenant, bookingA3, "booking_confirmation", jobDueAt())
	done := pgtest.SeedJob(t, owner, tenantA.Tenant, bookingA4, "booking_confirmation", jobDueAt())
	foreign := pgtest.SeedJob(t, owner, tenantB.Tenant, bookingB, "booking_confirmation", jobDueAt())

	db := pgtest.AppDB(t, appDSN)

	// Dead-letter through the store: the transition guards want the leasable
	// shape a claim leaves behind.
	for _, job := range []struct {
		id     uuid.UUID
		tenant uuid.UUID
	}{
		{older, tenantA.Tenant},
		{newer, tenantA.Tenant},
		{foreign, tenantB.Tenant},
	} {
		setJobLease(ctx, t, owner, job.id, "w-dead", jobDueAt().Add(time.Hour))
		applied, err := db.DeadLetterJob(ctx, "w-dead", domain.Job{ID: job.id, TenantID: job.tenant, Attempts: 1}, "attempt budget spent")
		if err != nil || !applied {
			t.Fatalf("dead-lettering job %s = %v, %v, want true, nil", job.id, applied, err)
		}
	}
	if _, err := owner.Exec(ctx, `UPDATE jobs SET status = 'done' WHERE id = $1`, done); err != nil {
		t.Fatalf("marking job %s done: %v", done, err)
	}

	// updated_at is the column "newest first" orders by, so stamping the dead
	// rows keeps the assertion independent of how fast the test ran. Tenant B's
	// job dies last and must still not appear in A's list.
	for _, death := range []struct {
		id uuid.UUID
		at string
	}{
		{older, "2026-11-01T09:00:00Z"},
		{newer, "2026-11-01T10:00:00Z"},
		{foreign, "2026-11-01T11:00:00Z"},
	} {
		if _, err := owner.Exec(ctx, `UPDATE jobs SET updated_at = $2::timestamptz WHERE id = $1`, death.id, death.at); err != nil {
			t.Fatalf("stamping job %s: %v", death.id, err)
		}
	}

	dead, err := db.ListDeadJobs(ctx, tenantA.Tenant, 10)
	if err != nil {
		t.Fatalf("ListDeadJobs: %v", err)
	}
	wantIDs := []uuid.UUID{newer, older}
	assertDeadJobsListedNewestFirst(t, dead, wantIDs, ready, done, foreign)
	assertNewestDeadJobKeptItsShape(t, dead)

	limited, err := db.ListDeadJobs(ctx, tenantA.Tenant, 1)
	if err != nil {
		t.Fatalf("ListDeadJobs with limit 1: %v", err)
	}
	assertDeadJobsLimitCapsTheList(t, limited, newer)
}

// assertDeadJobsListedNewestFirst checks the returned ids match wantIDs in
// order — newest first — and that no ready or done job leaked into the list.
func assertDeadJobsListedNewestFirst(t *testing.T, dead []app.DeadJob, wantIDs []uuid.UUID, ready, done, foreign uuid.UUID) {
	t.Helper()
	gotIDs := make([]uuid.UUID, 0, len(dead))
	for _, job := range dead {
		gotIDs = append(gotIDs, job.ID)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Errorf("tenant A's dead jobs = %v, want %v: newest first, dead only (ready %s, done %s, tenant B's %s)",
			gotIDs, wantIDs, ready, done, foreign)
	}
	for _, job := range dead {
		if job.ID == ready || job.ID == done {
			t.Errorf("ListDeadJobs returned job %s, which is not dead", job.ID)
		}
	}
}

// assertNewestDeadJobKeptItsShape checks the newest dead letter still carries
// its kind, attempts and failure reason, and died at the stamped instant.
func assertNewestDeadJobKeptItsShape(t *testing.T, dead []app.DeadJob) {
	t.Helper()
	if len(dead) != 2 {
		return
	}
	if dead[0].Kind != domain.JobBookingReminder || dead[0].Attempts != 1 || dead[0].LastError != "attempt budget spent" {
		t.Errorf("dead job = %+v, want the reminder with its attempts and the failure reason kept", dead[0])
	}
	if want := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC); !dead[0].DiedAt.Equal(want) {
		t.Errorf("dead job died at = %v, want the stamped %v", dead[0].DiedAt, want)
	}
}

// assertDeadJobsLimitCapsTheList checks a limit of one returns only the newest
// dead letter.
func assertDeadJobsLimitCapsTheList(t *testing.T, limited []app.DeadJob, newest uuid.UUID) {
	t.Helper()
	if len(limited) != 1 || limited[0].ID != newest {
		t.Errorf("limit 1 returned %d jobs (%v), want only the newest %s", len(limited), limited, newest)
	}
}
