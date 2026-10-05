//go:build integration

package postgres_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// availEnv is two Berlin tenants whose schedules the M3 contract pins, plus the
// use case wired to the real stores and a clock parked before every fixture
// date so NotBefore filters nothing.
type availEnv struct {
	db      *postgres.DB
	owner   *pgxpool.Pool
	tenantA pgtest.Fixture
	tenantB pgtest.Fixture
	useCase *app.Availability
}

func newAvailEnv(t *testing.T) availEnv {
	t.Helper()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	db := pgtest.AppDB(t, appDSN)
	return availEnv{
		db:      db,
		owner:   owner,
		tenantA: seedScheduledTenant(t, owner, "a"),
		tenantB: seedScheduledTenant(t, owner, "b"),
		useCase: app.NewAvailability(db, db, clock.NewFake(availInstant(2026, time.January, 1, 0, 0))),
	}
}

// seedScheduledTenant seeds the pinned schedule: a 30-minute service with a
// 15-minute buffer (a 45-minute block), Mon-Fri 09:00-12:00 and a Sunday
// 01:30-04:00 rule, one confirmed booking on Monday 2026-11-02 09:15 local
// whose stored ends_at covers the whole 45-minute occupancy, and one absence on
// Wednesday 2026-11-04 11:00-17:00 local.
func seedScheduledTenant(t *testing.T, owner *pgxpool.Pool, slug string) pgtest.Fixture {
	t.Helper()
	tenant := pgtest.Seed(t, owner, slug)
	if _, err := owner.Exec(t.Context(),
		"UPDATE services SET buffer_minutes = 15 WHERE id = $1", tenant.Service); err != nil {
		t.Fatalf("setting %s's service buffer: %v", slug, err)
	}
	for weekday := 1; weekday <= 5; weekday++ {
		pgtest.SeedWeeklyRule(t, owner, tenant, weekday, 540, 720)
	}
	pgtest.SeedWeeklyRule(t, owner, tenant, 0, 90, 240)
	pgtest.SeedBookingSpan(t, owner, tenant, "2026-11-02T08:15:00Z", 45)
	pgtest.SeedTimeOff(t, owner, tenant, "2026-11-04T10:00:00Z", "2026-11-04T16:00:00Z")
	return tenant
}

// TestAvailabilitySlotSearch runs the real search against Postgres at the
// pinned instants: a normal Monday, the Berlin spring-forward Sunday, a day
// with time off, and a closed day. Tenant B's schedule is in the same database
// and must never appear.
func TestAvailabilitySlotSearch(t *testing.T) {
	t.Parallel()
	env := newAvailEnv(t)

	// Monday 2026-11-02 is CET. The booking occupies 09:15-09:45 local, but its
	// stored ends_at is 10:00 local because it carries the 15-minute buffer:
	// that is why the 09:45 start is suppressed too, and only the two free
	// starts of the 45-minute grid remain.
	t.Run("normal Monday", func(t *testing.T) {
		t.Parallel()
		assertAvailSlots(t, availSearch(t, env, "2026-11-02", "2026-11-02"),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 2, 9, 30)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 2, 10, 15)))
	})

	t.Run("Tuesday without blockers", func(t *testing.T) {
		t.Parallel()
		assertAvailSlots(t, availSearch(t, env, "2026-11-03", "2026-11-03"),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 8, 0)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 8, 45)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 9, 30)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 10, 15)))
	})

	// Sunday 2026-03-29 skips 02:00-03:00 local. The 45-minute grid steps
	// 01:30, 02:15, 03:00, and the middle one does not exist, so the two
	// remaining starts sit on different sides of the transition: 01:30 is CET
	// (+1) and 03:00 is CEST (+2).
	t.Run("spring forward Sunday", func(t *testing.T) {
		t.Parallel()
		slots := availSearch(t, env, "2026-03-29", "2026-03-29")
		assertAvailSlots(t, slots,
			availSlot(env.tenantA.Staff, availInstant(2026, time.March, 29, 0, 30)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.March, 29, 1, 0)))
		berlin := availLocation(t, "Europe/Berlin")
		for _, s := range slots {
			if local := s.Start.In(berlin); local.Hour() == 2 {
				t.Errorf("slot %v reads back as %s, inside the skipped hour", s.Start, local)
			}
		}
	})

	// Wednesday 2026-11-04 is blocked from 11:00 local, so only the first two
	// starts of the grid survive.
	t.Run("time off removes the covered starts", func(t *testing.T) {
		t.Parallel()
		assertAvailSlots(t, availSearch(t, env, "2026-11-04", "2026-11-04"),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 4, 8, 0)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 4, 8, 45)))
	})

	t.Run("closed Saturday is an empty slice", func(t *testing.T) {
		t.Parallel()
		slots := availSearch(t, env, "2026-11-07", "2026-11-07")
		if slots == nil || len(slots) != 0 {
			t.Errorf("Search = %#v, want a non-nil empty slice", slots)
		}
	})

	t.Run("range spans three days in order", func(t *testing.T) {
		t.Parallel()
		assertAvailSlots(t, availSearch(t, env, "2026-11-02", "2026-11-04"),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 2, 9, 30)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 2, 10, 15)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 8, 0)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 8, 45)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 9, 30)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 10, 15)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 4, 8, 0)),
			availSlot(env.tenantA.Staff, availInstant(2026, time.November, 4, 8, 45)))
	})
}

// TestAvailabilityTenancyAndExclusions proves tenant B's rules and absences
// never reach tenant A's search, foreign staff ids stay domain.ErrNotFound, and
// deactivating a staff member or the service removes them from the search.
func TestAvailabilityTenancyAndExclusions(t *testing.T) {
	t.Parallel()
	env := newAvailEnv(t)
	second := pgtest.SeedStaff(t, env.owner, env.tenantA, "Bea", "bea@example.com")
	for weekday := 1; weekday <= 5; weekday++ {
		pgtest.SeedWeeklyRule(t, env.owner,
			pgtest.Fixture{Tenant: env.tenantA.Tenant, Staff: second, Service: env.tenantA.Service}, weekday, 540, 720)
	}

	assertAvailForeignRowsHidden(t, env, second)
	assertAvailForeignWritesNotFound(t, env)
	if err := env.db.SetStaffActive(t.Context(), env.tenantA.Tenant, second, false); err != nil {
		t.Fatalf("deactivating the second staff member: %v", err)
	}
	assertAvailInactiveStaffExcluded(t, env)
	if err := env.db.SetServiceActive(t.Context(), env.tenantA.Tenant, env.tenantA.Service, false); err != nil {
		t.Fatalf("deactivating the service: %v", err)
	}
	assertAvailInactiveServiceNotFound(t, env)
}

func assertAvailForeignRowsHidden(t *testing.T, env availEnv, second uuid.UUID) {
	t.Helper()

	slots := availSearch(t, env, "2026-11-03", "2026-11-03")
	if len(slots) != 8 {
		t.Fatalf("Search = %s, want four starts for each of tenant A's two staff members", availSlots(slots))
	}
	counts := map[uuid.UUID]int{}
	for _, s := range slots {
		counts[s.StaffID]++
	}
	if counts[env.tenantA.Staff] != 4 || counts[second] != 4 {
		t.Errorf("staff occurrence counts = %v, want 4 for each of tenant A's staff members", counts)
	}
	if counts[env.tenantB.Staff] != 0 {
		t.Errorf("tenant B's staff %s appears in tenant A's search", env.tenantB.Staff)
	}

	// Tenant B's service is invisible from tenant A's transaction, so the
	// snapshot reports it as missing rather than as someone else's row.
	_, err := env.db.SlotSnapshot(t.Context(), env.tenantA.Tenant, env.tenantB.Service,
		availInstant(2026, time.November, 3, 0, 0), availInstant(2026, time.November, 4, 0, 0))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SlotSnapshot of tenant B's service = %v, want domain.ErrNotFound", err)
	}

	// The snapshot itself carries tenant A's two staff members and none of
	// tenant B's rows: a leak here would be visible as a third schedule.
	snapshot, err := env.db.SlotSnapshot(t.Context(), env.tenantA.Tenant, env.tenantA.Service,
		availInstant(2026, time.November, 3, 0, 0), availInstant(2026, time.November, 4, 0, 0))
	if err != nil {
		t.Fatalf("SlotSnapshot: %v", err)
	}
	if len(snapshot.Staff) != 2 {
		t.Fatalf("SlotSnapshot has %d staff schedules, want tenant A's 2", len(snapshot.Staff))
	}
	for _, schedule := range snapshot.Staff {
		if schedule.Staff.ID == env.tenantB.Staff {
			t.Errorf("tenant B's staff %s appears in tenant A's snapshot", schedule.Staff.ID)
		}
	}
}

func assertAvailForeignWritesNotFound(t *testing.T, env availEnv) {
	t.Helper()
	ctx := t.Context()
	actor := domain.User{ID: uuid.New(), TenantID: env.tenantA.Tenant, Email: "owner@example.com", Role: domain.RoleOwner}

	err := env.useCase.SetWeeklyRules(ctx, actor, env.tenantB.Staff,
		[]app.WeeklyRuleInput{{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SetWeeklyRules with tenant B's staff = %v, want domain.ErrNotFound", err)
	}
	err = env.useCase.AddTimeOff(ctx, actor, env.tenantB.Staff, availDate(t, "2026-12-01"), availDate(t, "2026-12-02"))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("AddTimeOff with tenant B's staff = %v, want domain.ErrNotFound", err)
	}

	foreign, err := env.db.ListWeeklyRules(ctx, env.tenantA.Tenant, env.tenantB.Staff)
	if err != nil {
		t.Fatalf("listing tenant B's rules from tenant A: %v", err)
	}
	if len(foreign) != 0 {
		t.Errorf("tenant A sees %d of tenant B's rules, want 0", len(foreign))
	}
	own, err := env.db.ListWeeklyRules(ctx, env.tenantB.Tenant, env.tenantB.Staff)
	if err != nil {
		t.Fatalf("listing tenant B's rules as tenant B: %v", err)
	}
	if len(own) != 6 {
		t.Errorf("tenant B has %d rules after A's rejected write, want its original 6 (Mon-Fri plus Sunday)", len(own))
	}
}

func assertAvailInactiveStaffExcluded(t *testing.T, env availEnv) {
	t.Helper()

	assertAvailSlots(t, availSearch(t, env, "2026-11-03", "2026-11-03"),
		availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 8, 0)),
		availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 8, 45)),
		availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 9, 30)),
		availSlot(env.tenantA.Staff, availInstant(2026, time.November, 3, 10, 15)))
}

func assertAvailInactiveServiceNotFound(t *testing.T, env availEnv) {
	t.Helper()
	ctx := t.Context()

	_, err := env.useCase.Search(ctx, env.tenantA.Tenant, env.tenantA.Service, availDate(t, "2026-11-03"), availDate(t, "2026-11-03"))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Search of the deactivated service = %v, want domain.ErrNotFound", err)
	}
	_, err = env.db.SlotSnapshot(ctx, env.tenantA.Tenant, env.tenantA.Service,
		availInstant(2026, time.November, 3, 0, 0), availInstant(2026, time.November, 4, 0, 0))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("SlotSnapshot of the deactivated service = %v, want domain.ErrNotFound", err)
	}
}

// TestAvailabilityRoundTrip drives the write path through the use case:
// replacing a week and adding and removing time off both round-trip through
// Postgres.
func TestAvailabilityRoundTrip(t *testing.T) {
	t.Parallel()
	env := newAvailEnv(t)
	actor := domain.User{ID: uuid.New(), TenantID: env.tenantA.Tenant, Email: "owner@example.com", Role: domain.RoleOwner}

	assertAvailWeeklyRulesRoundTrip(t, env, actor)
	assertAvailTimeOffRoundTrip(t, env, actor)
}

func assertAvailWeeklyRulesRoundTrip(t *testing.T, env availEnv, actor domain.User) {
	t.Helper()
	ctx := t.Context()

	duplicates := []app.WeeklyRuleInput{
		{Weekday: time.Friday, StartMinute: 600, EndMinute: 720},
		{Weekday: time.Monday, StartMinute: 540, EndMinute: 600},
		{Weekday: time.Monday, StartMinute: 540, EndMinute: 600},
	}
	if err := env.useCase.SetWeeklyRules(ctx, actor, env.tenantA.Staff, duplicates); err != nil {
		t.Fatalf("SetWeeklyRules: %v", err)
	}
	rules, err := env.useCase.ListWeeklyRules(ctx, actor, env.tenantA.Staff)
	if err != nil {
		t.Fatalf("ListWeeklyRules: %v", err)
	}
	want := []domain.WeeklyRule{
		{Weekday: time.Monday, StartMinute: 540, EndMinute: 600},
		{Weekday: time.Friday, StartMinute: 600, EndMinute: 720},
	}
	if len(rules) != len(want) {
		t.Fatalf("ListWeeklyRules = %+v, want the deduplicated week %+v", rules, want)
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("rule %d = %+v, want %+v", i, rules[i], want[i])
		}
	}

	if err := env.useCase.SetWeeklyRules(ctx, actor, env.tenantA.Staff, nil); err != nil {
		t.Fatalf("clearing the week: %v", err)
	}
	empty, err := env.useCase.ListWeeklyRules(ctx, actor, env.tenantA.Staff)
	if err != nil {
		t.Fatalf("ListWeeklyRules after clearing: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("ListWeeklyRules after clearing = %#v, want a non-nil empty slice", empty)
	}
}

func assertAvailTimeOffRoundTrip(t *testing.T, env availEnv, actor domain.User) {
	t.Helper()
	ctx := t.Context()

	clearAvailTimeOff(t, env, actor)

	// Berlin is CET in December, so the 24th through 27th are those midnights.
	if err := env.useCase.AddTimeOff(ctx, actor, env.tenantA.Staff, availDate(t, "2026-12-24"), availDate(t, "2026-12-27")); err != nil {
		t.Fatalf("AddTimeOff: %v", err)
	}
	entries, err := env.useCase.ListTimeOff(ctx, actor, env.tenantA.Staff)
	if err != nil {
		t.Fatalf("ListTimeOff: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ListTimeOff = %+v, want the one added absence", entries)
	}
	entry := entries[0]
	wantFrom, wantTo := availInstant(2026, time.December, 23, 23, 0), availInstant(2026, time.December, 27, 23, 0)
	if entry.StaffID != env.tenantA.Staff || !entry.StartsAt.Equal(wantFrom) || !entry.EndsAt.Equal(wantTo) {
		t.Errorf("absence = %+v, want staff %s over [%v, %v)", entry, env.tenantA.Staff, wantFrom, wantTo)
	}

	if err := env.useCase.RemoveTimeOff(ctx, actor, env.tenantA.Staff, entry.ID); err != nil {
		t.Fatalf("RemoveTimeOff: %v", err)
	}
	remaining, err := env.useCase.ListTimeOff(ctx, actor, env.tenantA.Staff)
	if err != nil {
		t.Fatalf("ListTimeOff after removing: %v", err)
	}
	if remaining == nil || len(remaining) != 0 {
		t.Errorf("ListTimeOff after removing = %#v, want a non-nil empty slice", remaining)
	}
	if err := env.useCase.RemoveTimeOff(ctx, actor, env.tenantA.Staff, entry.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("RemoveTimeOff of a deleted absence = %v, want domain.ErrNotFound", err)
	}
}

// TestAvailabilityReplaceWeeklyRulesSerializes proves two concurrent
// replacements for one staff member cannot interleave into the union of both
// weeks: the second transaction waits at the staff row lock, and once the first
// commits it sees the first week and replaces it whole.
func TestAvailabilityReplaceWeeklyRulesSerializes(t *testing.T) {
	t.Parallel()
	env := newAvailEnv(t)
	actor := domain.User{ID: uuid.New(), TenantID: env.tenantA.Tenant, Email: "owner@example.com", Role: domain.RoleOwner}
	staff := env.tenantA.Staff
	weekA := []app.WeeklyRuleInput{{Weekday: time.Monday, StartMinute: 480, EndMinute: 540}}
	weekB := []app.WeeklyRuleInput{{Weekday: time.Tuesday, StartMinute: 600, EndMinute: 660}}

	t1 := startWeekReplacement(t, env, staff, weekA)

	done := make(chan error, 1)
	go func() {
		done <- env.useCase.SetWeeklyRules(t.Context(), actor, staff, weekB)
	}()

	waitForLockedBackend(t, env.owner)

	if err := t1.Commit(t.Context()); err != nil {
		t.Fatalf("committing T1: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetWeeklyRules: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SetWeeklyRules did not finish after T1 committed")
	}
	assertAvailWeeklyRules(t, env, actor, staff,
		[]domain.WeeklyRule{{Weekday: time.Tuesday, StartMinute: 600, EndMinute: 660}})
}

// startWeekReplacement opens T1: a transaction holding the staff member's row
// lock with the staff member's week deleted and week inserted, left uncommitted
// so the caller can commit it once the second replacement is blocked.
func startWeekReplacement(t *testing.T, env availEnv, staff uuid.UUID, week []app.WeeklyRuleInput) pgx.Tx {
	t.Helper()
	ctx := t.Context()

	tx, err := env.owner.Begin(ctx)
	if err != nil {
		t.Fatalf("beginning T1: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, "SELECT 1 FROM staff WHERE id = $1 FOR UPDATE", staff); err != nil {
		t.Fatalf("T1 locking the staff row: %v", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM availability_rules WHERE staff_id = $1", staff); err != nil {
		t.Fatalf("T1 deleting the week: %v", err)
	}
	rule := week[0]
	if _, err := tx.Exec(ctx, `
		INSERT INTO availability_rules (tenant_id, staff_id, weekday, start_minute, end_minute)
		VALUES ($1, $2, $3, $4, $5)`,
		env.tenantA.Tenant, staff, int(rule.Weekday), rule.StartMinute, rule.EndMinute); err != nil {
		t.Fatalf("T1 inserting its week: %v", err)
	}
	return tx
}

// assertAvailWeeklyRules checks that the staff member's stored week is exactly
// want.
func assertAvailWeeklyRules(t *testing.T, env availEnv, actor domain.User, staff uuid.UUID, want []domain.WeeklyRule) {
	t.Helper()

	rules, err := env.useCase.ListWeeklyRules(t.Context(), actor, staff)
	if err != nil {
		t.Fatalf("ListWeeklyRules: %v", err)
	}
	if len(rules) != len(want) {
		t.Fatalf("ListWeeklyRules = %+v, want exactly %+v", rules, want)
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("rule %d = %+v, want %+v", i, rules[i], want[i])
		}
	}
}

// waitForLockedBackend polls until at least one backend in this database is
// waiting on a lock: the second replacement blocked behind the held row.
func waitForLockedBackend(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(t.Context(),
			"SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'").Scan(&waiting); err != nil {
			t.Fatalf("counting lock waiters: %v", err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no backend waited on a lock within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// clearAvailTimeOff removes every absence the fixture seeded, so the round trip
// below starts from a known state.
func clearAvailTimeOff(t *testing.T, env availEnv, actor domain.User) {
	t.Helper()
	ctx := t.Context()

	existing, err := env.useCase.ListTimeOff(ctx, actor, env.tenantA.Staff)
	if err != nil {
		t.Fatalf("ListTimeOff: %v", err)
	}
	for _, entry := range existing {
		if err := env.useCase.RemoveTimeOff(ctx, actor, env.tenantA.Staff, entry.ID); err != nil {
			t.Fatalf("clearing an existing absence: %v", err)
		}
	}
	left, err := env.useCase.ListTimeOff(ctx, actor, env.tenantA.Staff)
	if err != nil {
		t.Fatalf("ListTimeOff after clearing: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("ListTimeOff after clearing = %+v, want no absences", left)
	}
}

// availSearch runs the public search for the inclusive local range.
func availSearch(t *testing.T, env availEnv, from, to string) []domain.Slot {
	t.Helper()
	slots, err := env.useCase.Search(t.Context(), env.tenantA.Tenant, env.tenantA.Service, availDate(t, from), availDate(t, to))
	if err != nil {
		t.Fatalf("Search(%s..%s): %v", from, to, err)
	}
	return slots
}

func assertAvailSlots(t *testing.T, got []domain.Slot, want ...domain.Slot) {
	t.Helper()
	if got == nil {
		t.Fatal("Search returned nil, want a non-nil slice")
	}
	if len(got) != len(want) {
		t.Fatalf("Search = %s, want %s", availSlots(got), availSlots(want))
	}
	for i := range want {
		if got[i].StaffID != want[i].StaffID || !got[i].Start.Equal(want[i].Start) {
			t.Errorf("slot %d = (%s, %s), want (%s, %s)",
				i, got[i].StaffID, got[i].Start.UTC(), want[i].StaffID, want[i].Start.UTC())
		}
	}
}

func availSlots(slots []domain.Slot) string {
	parts := make([]string, 0, len(slots))
	for _, s := range slots {
		parts = append(parts, fmt.Sprintf("%s@%s", s.StaffID, s.Start.UTC().Format(time.RFC3339)))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func availSlot(staffID uuid.UUID, start time.Time) domain.Slot {
	return domain.Slot{StaffID: staffID, Start: start}
}

func availInstant(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func availDate(t *testing.T, s string) domain.LocalDate {
	t.Helper()
	parsed, err := domain.ParseLocalDate(s)
	if err != nil {
		t.Fatalf("parsing date %s: %v", s, err)
	}
	return parsed
}

func availLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("loading location %s: %v", name, err)
	}
	return loc
}
