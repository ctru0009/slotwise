package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// fakeAvailabilityStore is an in-memory AvailabilityStore that records what the
// use case asked it to do.
type fakeAvailabilityStore struct {
	snapshot      domain.SlotSnapshot
	snapshotErr   error
	snapshotCalls int
	snapshotFrom  time.Time
	snapshotTo    time.Time

	rules    []domain.WeeklyRule
	listErr  error
	stored   []WeeklyRuleInput
	replace  error
	replaces int

	timeOff      []domain.TimeOff
	insertedFrom time.Time
	insertedTo   time.Time
	insertErr    error
	inserts      int

	deletedID uuid.UUID
	deleteErr error
	deletes   int
}

func (f *fakeAvailabilityStore) SlotSnapshot(_ context.Context, _ uuid.UUID, _ uuid.UUID, from, to time.Time) (domain.SlotSnapshot, error) {
	f.snapshotCalls++
	f.snapshotFrom = from
	f.snapshotTo = to
	if f.snapshotErr != nil {
		return domain.SlotSnapshot{}, f.snapshotErr
	}
	return f.snapshot, nil
}

func (f *fakeAvailabilityStore) ListWeeklyRules(_ context.Context, _ uuid.UUID, _ uuid.UUID) ([]domain.WeeklyRule, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.rules, nil
}

func (f *fakeAvailabilityStore) ReplaceWeeklyRules(_ context.Context, _ uuid.UUID, _ uuid.UUID, rules []WeeklyRuleInput) error {
	f.replaces++
	f.stored = rules
	return f.replace
}

func (f *fakeAvailabilityStore) ListTimeOff(_ context.Context, _ uuid.UUID, _ uuid.UUID) ([]domain.TimeOff, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.timeOff, nil
}

func (f *fakeAvailabilityStore) InsertTimeOff(_ context.Context, _ uuid.UUID, _ uuid.UUID, from, to time.Time) error {
	f.inserts++
	f.insertedFrom = from
	f.insertedTo = to
	return f.insertErr
}

func (f *fakeAvailabilityStore) DeleteTimeOff(_ context.Context, _ uuid.UUID, _ uuid.UUID, id uuid.UUID) error {
	f.deletes++
	f.deletedID = id
	return f.deleteErr
}

// availabilityFixture is one Berlin tenant wired to the use case and the fakes,
// with the clock parked before the fixture dates so NotBefore filters nothing.
type availabilityFixture struct {
	useCase  *Availability
	store    *fakeAvailabilityStore
	clock    *clock.Fake
	tenants  *fakeTenantStore
	tenantID uuid.UUID
}

func newAvailabilityFixture(t *testing.T) availabilityFixture {
	t.Helper()
	tenantID := uuid.New()
	store := &fakeAvailabilityStore{}
	tenants := &fakeTenantStore{bySlug: map[string]domain.Tenant{
		"acme": {ID: tenantID, Slug: "acme", Name: "Acme", Timezone: "Europe/Berlin"},
	}}
	fakeClock := clock.NewFake(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	return availabilityFixture{
		useCase:  NewAvailability(store, tenants, fakeClock),
		store:    store,
		clock:    fakeClock,
		tenants:  tenants,
		tenantID: tenantID,
	}
}

// staffSchedule is one active staff member with the given rules.
func staffSchedule(t *testing.T, rules ...domain.WeeklyRule) domain.StaffSchedule {
	t.Helper()
	return domain.StaffSchedule{
		Staff: domain.Staff{ID: uuid.New(), TenantID: uuid.New(), Name: "Ada", Email: "ada@example.com", Active: true},
		Rules: rules,
	}
}

func utcInst(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func date(t *testing.T, s string) domain.LocalDate {
	t.Helper()
	parsed, err := domain.ParseLocalDate(s)
	if err != nil {
		t.Fatalf("parsing date %s: %v", s, err)
	}
	return parsed
}

func assertValidationField(t *testing.T, err error, field string) {
	t.Helper()
	var invalid domain.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want a domain.ValidationError", err)
	}
	if invalid.Field != field {
		t.Errorf("error field = %q, want %q", invalid.Field, field)
	}
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("error = %v, want it to match ErrInvalidInput", err)
	}
}

// TestAvailabilitySearchSearchesTheTenantTimezone drives one Monday search
// through the wall-clock pipeline: the range reaches the store as Berlin
// midnight bounds, and the 30+15 block lands on a 45-minute grid.
func TestAvailabilitySearchSearchesTheTenantTimezone(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	schedule := staffSchedule(t, domain.WeeklyRule{Weekday: time.Monday, StartMinute: 540, EndMinute: 660})
	f.store.snapshot = domain.SlotSnapshot{
		Service: domain.Service{DurationMinutes: 30, BufferMinutes: 15, Active: true},
		Staff:   []domain.StaffSchedule{schedule},
	}

	monday := date(t, "2026-01-05")
	slots, err := f.useCase.Search(t.Context(), f.tenantID, uuid.New(), monday, monday)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	wantLo, wantHi := utcInst(2026, time.January, 4, 23, 0), utcInst(2026, time.January, 5, 23, 0)
	if !f.store.snapshotFrom.Equal(wantLo) || !f.store.snapshotTo.Equal(wantHi) {
		t.Errorf("SlotSnapshot range = [%v, %v), want [%v, %v)", f.store.snapshotFrom, f.store.snapshotTo, wantLo, wantHi)
	}

	// Berlin is CET in January, so the 09:00 and 09:45 starts are 08:00Z and
	// 08:45Z; a 30-minute block would have emitted 09:30 local too.
	want := []domain.Slot{
		{StaffID: schedule.Staff.ID, Start: utcInst(2026, time.January, 5, 8, 0)},
		{StaffID: schedule.Staff.ID, Start: utcInst(2026, time.January, 5, 8, 45)},
	}
	if len(slots) != len(want) {
		t.Fatalf("Search = %v, want %v", slots, want)
	}
	for i := range want {
		if slots[i].StaffID != want[i].StaffID || !slots[i].Start.Equal(want[i].Start) {
			t.Errorf("slot %d = %+v, want %v starting %v", i, slots[i], want[i].StaffID, want[i].Start)
		}
	}
}

// TestAvailabilitySearchNotBeforeComesFromTheClock proves the candidate filter
// uses the injected clock: the same search returns one slot with the clock
// parked at 11:00 Berlin and three with it parked earlier.
func TestAvailabilitySearchNotBeforeComesFromTheClock(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	schedule := staffSchedule(t, domain.WeeklyRule{Weekday: time.Monday, StartMinute: 540, EndMinute: 720})
	f.store.snapshot = domain.SlotSnapshot{
		Service: domain.Service{DurationMinutes: 60, Active: true},
		Staff:   []domain.StaffSchedule{schedule},
	}

	monday := date(t, "2026-01-05")
	f.clock.Set(utcInst(2026, time.January, 5, 10, 0)) // 11:00 Berlin
	late, err := f.useCase.Search(t.Context(), f.tenantID, uuid.New(), monday, monday)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(late) != 1 || !late[0].Start.Equal(utcInst(2026, time.January, 5, 10, 0)) {
		t.Fatalf("Search with the clock at 10:00Z = %v, want only the 11:00 Berlin start", late)
	}

	f.clock.Set(utcInst(2026, time.January, 5, 6, 0))
	early, err := f.useCase.Search(t.Context(), f.tenantID, uuid.New(), monday, monday)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(early) != 3 {
		t.Fatalf("Search with the clock at 06:00Z = %v, want three starts", early)
	}
}

// TestAvailabilitySearchOrdersByStartThenStaff pins the order of a search with
// two staff members: starts first, then staff id.
func TestAvailabilitySearchOrdersByStartThenStaff(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	rule := domain.WeeklyRule{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}
	first := staffSchedule(t, rule)
	second := staffSchedule(t, rule)
	if slices.Compare(first.Staff.ID[:], second.Staff.ID[:]) > 0 {
		first, second = second, first
	}
	f.store.snapshot = domain.SlotSnapshot{
		Service: domain.Service{DurationMinutes: 30, Active: true},
		Staff:   []domain.StaffSchedule{second, first},
	}

	monday := date(t, "2026-01-05")
	slots, err := f.useCase.Search(t.Context(), f.tenantID, uuid.New(), monday, monday)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := []domain.Slot{
		{StaffID: first.Staff.ID, Start: utcInst(2026, time.January, 5, 8, 0)},
		{StaffID: second.Staff.ID, Start: utcInst(2026, time.January, 5, 8, 0)},
		{StaffID: first.Staff.ID, Start: utcInst(2026, time.January, 5, 8, 30)},
		{StaffID: second.Staff.ID, Start: utcInst(2026, time.January, 5, 8, 30)},
	}
	if len(slots) != len(want) {
		t.Fatalf("Search = %v, want %v", slots, want)
	}
	for i := range want {
		if slots[i].StaffID != want[i].StaffID || !slots[i].Start.Equal(want[i].Start) {
			t.Errorf("slot %d = %+v, want %+v", i, slots[i], want[i])
		}
	}
}

func TestAvailabilitySearchValidatesTheRange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		from  domain.LocalDate
		to    domain.LocalDate
		field string
	}{
		{"from after to", date(t, "2026-01-06"), date(t, "2026-01-05"), "to"},
		{"span over 62 days", date(t, "2026-01-01"), date(t, "2026-03-04"), "to"},
		{"not a real from date", domain.LocalDate{Year: 2026, Month: time.February, Day: 30}, date(t, "2026-03-01"), "from"},
		{"not a real to date", date(t, "2026-03-01"), domain.LocalDate{Year: 2026, Month: time.February, Day: 30}, "to"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newAvailabilityFixture(t)
			_, err := f.useCase.Search(t.Context(), f.tenantID, uuid.New(), tt.from, tt.to)
			assertValidationField(t, err, tt.field)
			if f.store.snapshotCalls != 0 {
				t.Errorf("the store was read %d times, want 0", f.store.snapshotCalls)
			}
		})
	}
}

func TestAvailabilitySearchAcceptsTheSpanCap(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	_, err := f.useCase.Search(t.Context(), f.tenantID, uuid.New(), date(t, "2026-01-01"), date(t, "2026-03-03"))
	if err != nil {
		t.Fatalf("Search over exactly 62 days: %v", err)
	}
}

func TestAvailabilitySearchPassesErrorsThrough(t *testing.T) {
	t.Parallel()
	t.Run("unknown service", func(t *testing.T) {
		t.Parallel()
		f := newAvailabilityFixture(t)
		f.store.snapshotErr = fmt.Errorf("service: %w", domain.ErrNotFound)
		_, err := f.useCase.Search(t.Context(), f.tenantID, uuid.New(), date(t, "2026-01-05"), date(t, "2026-01-05"))
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Search = %v, want domain.ErrNotFound", err)
		}
	})
	t.Run("unknown tenant", func(t *testing.T) {
		t.Parallel()
		f := newAvailabilityFixture(t)
		f.tenants.lookupErr = domain.ErrNotFound
		_, err := f.useCase.Search(t.Context(), f.tenantID, uuid.New(), date(t, "2026-01-05"), date(t, "2026-01-05"))
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Search = %v, want domain.ErrNotFound", err)
		}
	})
	t.Run("bad timezone", func(t *testing.T) {
		t.Parallel()
		f := newAvailabilityFixture(t)
		f.tenants.bySlug["acme"] = domain.Tenant{ID: f.tenantID, Slug: "acme", Timezone: "Not/AZone"}
		_, err := f.useCase.Search(t.Context(), f.tenantID, uuid.New(), date(t, "2026-01-05"), date(t, "2026-01-05"))
		if err == nil {
			t.Fatal("Search with an unknown timezone succeeded, want an error")
		}
	})
}

// TestAvailabilitySearchEmptyResultIsNonNil covers the empty week and the empty
// roster: a search must hand back an empty slice, not nil.
func TestAvailabilitySearchEmptyResultIsNonNil(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	f.store.snapshot = domain.SlotSnapshot{Service: domain.Service{DurationMinutes: 30, Active: true}}
	slots, err := f.useCase.Search(t.Context(), f.tenantID, uuid.New(), date(t, "2026-01-05"), date(t, "2026-01-05"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if slots == nil || len(slots) != 0 {
		t.Errorf("Search = %#v, want a non-nil empty slice", slots)
	}
}

func TestAvailabilityWritesRequireOwner(t *testing.T) {
	t.Parallel()
	from, to := date(t, "2026-01-05"), date(t, "2026-01-06")
	calls := []struct {
		name string
		call func(context.Context, *Availability, domain.User) error
	}{
		{"set weekly rules", func(ctx context.Context, useCase *Availability, actor domain.User) error {
			return useCase.SetWeeklyRules(ctx, actor, uuid.New(), []WeeklyRuleInput{{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}})
		}},
		{"add time off", func(ctx context.Context, useCase *Availability, actor domain.User) error {
			return useCase.AddTimeOff(ctx, actor, uuid.New(), from, to)
		}},
		{"remove time off", func(ctx context.Context, useCase *Availability, actor domain.User) error {
			return useCase.RemoveTimeOff(ctx, actor, uuid.New(), uuid.New())
		}},
	}
	for _, tt := range calls {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newAvailabilityFixture(t)
			err := tt.call(t.Context(), f.useCase, staffActor(f.tenantID))
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("error = %v, want domain.ErrForbidden", err)
			}
			if f.store.replaces+f.store.inserts+f.store.deletes != 0 {
				t.Errorf("the store was written %d times, want 0", f.store.replaces+f.store.inserts+f.store.deletes)
			}
		})
	}
}

// TestAvailabilitySetWeeklyRulesNormalises checks that duplicates collapse and
// the store receives one sorted week.
func TestAvailabilitySetWeeklyRulesNormalises(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	rules := []WeeklyRuleInput{
		{Weekday: time.Friday, StartMinute: 600, EndMinute: 720},
		{Weekday: time.Monday, StartMinute: 540, EndMinute: 600},
		{Weekday: time.Monday, StartMinute: 540, EndMinute: 600},
		{Weekday: time.Monday, StartMinute: 540, EndMinute: 660},
	}
	if err := f.useCase.SetWeeklyRules(t.Context(), ownerActor(f.tenantID), uuid.New(), rules); err != nil {
		t.Fatalf("SetWeeklyRules: %v", err)
	}
	want := []WeeklyRuleInput{
		{Weekday: time.Monday, StartMinute: 540, EndMinute: 600},
		{Weekday: time.Monday, StartMinute: 540, EndMinute: 660},
		{Weekday: time.Friday, StartMinute: 600, EndMinute: 720},
	}
	if len(f.store.stored) != len(want) {
		t.Fatalf("stored %+v, want %+v", f.store.stored, want)
	}
	for i := range want {
		if f.store.stored[i] != want[i] {
			t.Errorf("stored[%d] = %+v, want %+v", i, f.store.stored[i], want[i])
		}
	}
}

func TestAvailabilitySetWeeklyRulesEmptyIsValid(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	if err := f.useCase.SetWeeklyRules(t.Context(), ownerActor(f.tenantID), uuid.New(), nil); err != nil {
		t.Fatalf("SetWeeklyRules(nil): %v", err)
	}
	if f.store.replaces != 1 || len(f.store.stored) != 0 {
		t.Errorf("stored %+v after %d calls, want one call with an empty week", f.store.stored, f.store.replaces)
	}
}

func TestAvailabilitySetWeeklyRulesValidates(t *testing.T) {
	t.Parallel()
	valid := WeeklyRuleInput{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}
	tooMany := make([]WeeklyRuleInput, maxWeeklyRules+1)
	for i := range tooMany {
		tooMany[i] = valid
	}
	tests := []struct {
		name  string
		rules []WeeklyRuleInput
		field string
	}{
		{"weekday out of range", []WeeklyRuleInput{{Weekday: 7, StartMinute: 0, EndMinute: 60}}, "rules[0].weekday"},
		{"start past the day", []WeeklyRuleInput{{Weekday: time.Monday, StartMinute: 1440, EndMinute: 1440}}, "rules[0].start_minute"},
		{"negative start", []WeeklyRuleInput{{Weekday: time.Monday, StartMinute: -1, EndMinute: 60}}, "rules[0].start_minute"},
		{"zero length", []WeeklyRuleInput{{Weekday: time.Monday, StartMinute: 600, EndMinute: 600}}, "rules[0].end_minute"},
		{"end past midnight", []WeeklyRuleInput{{Weekday: time.Monday, StartMinute: 0, EndMinute: 1441}}, "rules[0].end_minute"},
		{"second rule names its index", []WeeklyRuleInput{valid, {Weekday: time.Tuesday, StartMinute: 0, EndMinute: 0}}, "rules[1].end_minute"},
		{"too many rules", tooMany, "rules"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newAvailabilityFixture(t)
			err := f.useCase.SetWeeklyRules(t.Context(), ownerActor(f.tenantID), uuid.New(), tt.rules)
			assertValidationField(t, err, tt.field)
			if f.store.replaces != 0 {
				t.Errorf("the store was written %d times, want 0", f.store.replaces)
			}
		})
	}
}

func TestAvailabilitySetWeeklyRulesPassesNotFoundThrough(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	f.store.replace = fmt.Errorf("staff: %w", domain.ErrNotFound)
	err := f.useCase.SetWeeklyRules(t.Context(), ownerActor(f.tenantID), uuid.New(), nil)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("SetWeeklyRules = %v, want domain.ErrNotFound", err)
	}
}

// TestAvailabilityAddTimeOffUsesTenantMidnight checks the exact bounds the
// store receives: local inclusive dates become Berlin midnights.
func TestAvailabilityAddTimeOffUsesTenantMidnight(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	staffID := uuid.New()
	if err := f.useCase.AddTimeOff(t.Context(), ownerActor(f.tenantID), staffID, date(t, "2026-01-05"), date(t, "2026-01-07")); err != nil {
		t.Fatalf("AddTimeOff: %v", err)
	}
	wantFrom, wantTo := utcInst(2026, time.January, 4, 23, 0), utcInst(2026, time.January, 7, 23, 0)
	if !f.store.insertedFrom.Equal(wantFrom) || !f.store.insertedTo.Equal(wantTo) {
		t.Errorf("InsertTimeOff bounds = [%v, %v), want [%v, %v)", f.store.insertedFrom, f.store.insertedTo, wantFrom, wantTo)
	}
}

func TestAvailabilityAddTimeOffValidates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		from  domain.LocalDate
		to    domain.LocalDate
		field string
	}{
		{"from after to", date(t, "2026-01-06"), date(t, "2026-01-05"), "to"},
		{"span over 366 days", date(t, "2026-01-01"), date(t, "2027-01-02"), "to"},
		{"not a real from date", domain.LocalDate{Year: 2026, Month: time.February, Day: 30}, date(t, "2026-03-01"), "from"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newAvailabilityFixture(t)
			err := f.useCase.AddTimeOff(t.Context(), ownerActor(f.tenantID), uuid.New(), tt.from, tt.to)
			assertValidationField(t, err, tt.field)
			if f.store.inserts != 0 {
				t.Errorf("the store was written %d times, want 0", f.store.inserts)
			}
		})
	}

	spanned := newAvailabilityFixture(t)
	if err := spanned.useCase.AddTimeOff(t.Context(), ownerActor(spanned.tenantID), uuid.New(), date(t, "2026-01-01"), date(t, "2027-01-01")); err != nil {
		t.Fatalf("AddTimeOff over exactly 366 days: %v", err)
	}
}

// TestAvailabilityAddTimeOffRejectsASkippedLocalDay covers Samoa's 2011-12-30,
// a local day the calendar has but the timezone does not: its whole range
// collapses to a point, so there is nothing to store.
func TestAvailabilityAddTimeOffRejectsASkippedLocalDay(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	f.tenants.bySlug["acme"] = domain.Tenant{ID: f.tenantID, Slug: "acme", Name: "Acme", Timezone: "Pacific/Apia"}
	actor := ownerActor(f.tenantID)

	err := f.useCase.AddTimeOff(t.Context(), actor, uuid.New(), date(t, "2011-12-30"), date(t, "2011-12-30"))
	assertValidationField(t, err, "from")
	if f.store.inserts != 0 {
		t.Errorf("the store was written %d times, want 0", f.store.inserts)
	}

	// A range that merely contains the skipped day is still a real span.
	if err := f.useCase.AddTimeOff(t.Context(), actor, uuid.New(), date(t, "2011-12-29"), date(t, "2011-12-31")); err != nil {
		t.Fatalf("AddTimeOff across the skipped day: %v", err)
	}
	if f.store.inserts != 1 {
		t.Errorf("the store was written %d times, want 1", f.store.inserts)
	}
	if !f.store.insertedTo.After(f.store.insertedFrom) {
		t.Errorf("InsertTimeOff bounds = [%v, %v), want a positive span", f.store.insertedFrom, f.store.insertedTo)
	}
}

func TestAvailabilityRemoveTimeOffReachesTheStore(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	staffID, id := uuid.New(), uuid.New()
	if err := f.useCase.RemoveTimeOff(t.Context(), ownerActor(f.tenantID), staffID, id); err != nil {
		t.Fatalf("RemoveTimeOff: %v", err)
	}
	if f.store.deletedID != id || f.store.deletes != 1 {
		t.Errorf("DeleteTimeOff(%v) called %d times, want (%v) once", f.store.deletedID, f.store.deletes, id)
	}

	f.store.deleteErr = fmt.Errorf("time off: %w", domain.ErrNotFound)
	err := f.useCase.RemoveTimeOff(t.Context(), ownerActor(f.tenantID), staffID, uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RemoveTimeOff = %v, want domain.ErrNotFound", err)
	}
}

func TestAvailabilityReadsAllowBothRolesAndNeverReturnNil(t *testing.T) {
	t.Parallel()
	f := newAvailabilityFixture(t)
	for _, actor := range []domain.User{ownerActor(f.tenantID), staffActor(f.tenantID)} {
		rules, err := f.useCase.ListWeeklyRules(t.Context(), actor, uuid.New())
		if err != nil {
			t.Fatalf("ListWeeklyRules as %s: %v", actor.Role, err)
		}
		if rules == nil {
			t.Errorf("ListWeeklyRules as %s = nil, want a non-nil slice", actor.Role)
		}
		entries, err := f.useCase.ListTimeOff(t.Context(), actor, uuid.New())
		if err != nil {
			t.Fatalf("ListTimeOff as %s: %v", actor.Role, err)
		}
		if entries == nil {
			t.Errorf("ListTimeOff as %s = nil, want a non-nil slice", actor.Role)
		}
	}
}
