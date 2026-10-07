package app

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// The dashboard fake set: one in-memory store per interface Dashboard reads
// through, each counting the calls Load makes and recording the tenant it was
// scoped to. Only the read Load uses is implemented; the rest of each
// interface exists to satisfy it.

type dashboardTenantStore struct {
	tenant    domain.Tenant
	byIDCalls int
}

func (f *dashboardTenantStore) TenantByID(_ context.Context, tenantID uuid.UUID) (domain.Tenant, error) {
	f.byIDCalls++
	if tenantID != f.tenant.ID {
		return domain.Tenant{}, domain.ErrNotFound
	}
	return f.tenant, nil
}

func (f *dashboardTenantStore) TenantBySlug(_ context.Context, slug string) (domain.Tenant, error) {
	if slug != f.tenant.Slug {
		return domain.Tenant{}, domain.ErrNotFound
	}
	return f.tenant, nil
}

func (f *dashboardTenantStore) InsertTenant(context.Context, domain.Tenant) (bool, error) {
	return false, nil
}

type dashboardBookingStore struct {
	bookings  []BookingInRange
	listCalls int
	tenantID  uuid.UUID
	from, to  time.Time
}

func (f *dashboardBookingStore) ListBookingsInRange(_ context.Context, tenantID uuid.UUID, from, to time.Time) ([]BookingInRange, error) {
	f.listCalls++
	f.tenantID = tenantID
	f.from, f.to = from, to
	return f.bookings, nil
}

func (f *dashboardBookingStore) BookingByIdempotencyKey(context.Context, uuid.UUID, string) (domain.Booking, error) {
	return domain.Booking{}, domain.ErrNotFound
}

func (f *dashboardBookingStore) CreateBooking(context.Context, uuid.UUID, BookingWrite) (domain.Booking, error) {
	return domain.Booking{}, nil
}

func (f *dashboardBookingStore) BookingByID(context.Context, uuid.UUID, uuid.UUID) (domain.Booking, error) {
	return domain.Booking{}, domain.ErrNotFound
}

func (f *dashboardBookingStore) BookingMessage(context.Context, uuid.UUID, uuid.UUID) (BookingMessage, error) {
	return BookingMessage{}, domain.ErrNotFound
}

func (f *dashboardBookingStore) CancelBooking(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

type dashboardStaffStore struct {
	staff     []domain.Staff
	listCalls int
	tenantID  uuid.UUID
}

func (f *dashboardStaffStore) ListStaff(_ context.Context, tenantID uuid.UUID) ([]domain.Staff, error) {
	f.listCalls++
	f.tenantID = tenantID
	return f.staff, nil
}

func (f *dashboardStaffStore) CreateStaff(context.Context, uuid.UUID, uuid.UUID, StaffInput) error {
	return nil
}

func (f *dashboardStaffStore) UpdateStaff(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, StaffInput) error {
	return nil
}

func (f *dashboardStaffStore) SetStaffActive(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, bool) error {
	return nil
}

type dashboardServiceStore struct {
	services  []domain.Service
	listCalls int
	tenantID  uuid.UUID
}

func (f *dashboardServiceStore) ListServices(_ context.Context, tenantID uuid.UUID) ([]domain.Service, error) {
	f.listCalls++
	f.tenantID = tenantID
	return f.services, nil
}

func (f *dashboardServiceStore) CreateService(context.Context, uuid.UUID, uuid.UUID, ServiceInput) error {
	return nil
}

func (f *dashboardServiceStore) UpdateService(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ServiceInput) error {
	return nil
}

func (f *dashboardServiceStore) SetServiceActive(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, bool) error {
	return nil
}

type dashboardAuditStore struct {
	entries   []AuditEntry
	listCalls int
	tenantID  uuid.UUID
	limit     int
}

func (f *dashboardAuditStore) ListAuditLog(_ context.Context, tenantID uuid.UUID, limit int) ([]AuditEntry, error) {
	f.listCalls++
	f.tenantID = tenantID
	f.limit = limit
	return f.entries, nil
}

type dashboardJobStore struct {
	jobs      []DeadJob
	listCalls int
	tenantID  uuid.UUID
	limit     int
}

func (f *dashboardJobStore) ListDeadJobs(_ context.Context, tenantID uuid.UUID, limit int) ([]DeadJob, error) {
	f.listCalls++
	f.tenantID = tenantID
	f.limit = limit
	return f.jobs, nil
}

func (f *dashboardJobStore) ClaimJob(context.Context, string, time.Time, int) (domain.Job, error) {
	return domain.Job{}, domain.ErrNotFound
}

func (f *dashboardJobStore) CompleteJob(context.Context, string, domain.Job) (bool, error) {
	return false, nil
}

func (f *dashboardJobStore) RetryJob(context.Context, string, domain.Job, time.Time, string) (bool, error) {
	return false, nil
}

func (f *dashboardJobStore) DeadLetterJob(context.Context, string, domain.Job, string) (bool, error) {
	return false, nil
}

func (f *dashboardJobStore) ReleaseJob(context.Context, string, domain.Job) (bool, error) {
	return false, nil
}

// dashboardFixture is one Berlin tenant wired to Dashboard through the fake
// set, with a clock the test drives. The clock starts on Thursday 2026-03-26.
type dashboardFixture struct {
	dashboard *Dashboard
	tenants   *dashboardTenantStore
	bookings  *dashboardBookingStore
	staff     *dashboardStaffStore
	services  *dashboardServiceStore
	audit     *dashboardAuditStore
	jobs      *dashboardJobStore
	tenant    domain.Tenant
	loc       *time.Location
	clk       *clock.Fake
	actor     domain.User
}

func newDashboardFixture(t *testing.T) dashboardFixture {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("loading Europe/Berlin: %v", err)
	}
	tenant := domain.Tenant{ID: uuid.New(), Slug: "acme", Name: "Acme", Timezone: "Europe/Berlin"}
	f := dashboardFixture{
		tenants:  &dashboardTenantStore{tenant: tenant},
		bookings: &dashboardBookingStore{},
		staff:    &dashboardStaffStore{},
		services: &dashboardServiceStore{},
		audit:    &dashboardAuditStore{},
		jobs:     &dashboardJobStore{},
		tenant:   tenant,
		loc:      loc,
		clk:      clock.NewFake(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)),
	}
	f.actor = domain.User{ID: uuid.New(), TenantID: tenant.ID, Email: "owner@example.com", Role: domain.RoleOwner}
	f.dashboard = NewDashboard(f.tenants, f.bookings, f.staff, f.services, f.audit, f.jobs, f.clk)
	return f
}

// load runs one Load and fails the test when it errors.
func (f dashboardFixture) load(t *testing.T, mode CalendarMode, anchor *domain.LocalDate) Overview {
	t.Helper()
	overview, err := f.dashboard.Load(t.Context(), f.actor, mode, anchor)
	if err != nil {
		t.Fatalf("Load(%s): %v", mode, err)
	}
	return overview
}

// wall builds the instant for a Berlin wall time, which is how the tests state
// bookings and expected midnights.
func (f dashboardFixture) wall(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, f.loc)
}

// TestDashboardWeekStartsMonday pins the week window: seven consecutive local
// days from the Monday of the week holding the clock's Thursday, with the
// instant range running from the first local midnight to the one after the
// last day.
func TestDashboardWeekStartsMonday(t *testing.T) {
	t.Parallel()
	f := newDashboardFixture(t)
	got := f.load(t, CalendarWeek, nil)

	if got.Window.Mode != CalendarWeek {
		t.Errorf("Window.Mode = %q, want %q", got.Window.Mode, CalendarWeek)
	}
	if len(got.Window.Days) != 7 {
		t.Fatalf("len(Window.Days) = %d, want 7", len(got.Window.Days))
	}
	if got.Window.Days[0].Weekday() != time.Monday {
		t.Errorf("Window.Days[0] = %s (%s), want a Monday", got.Window.Days[0], got.Window.Days[0].Weekday())
	}
	for i, day := range got.Window.Days {
		want := domain.LocalDate{Year: 2026, Month: time.March, Day: 23 + i}
		if !day.Equal(want) {
			t.Errorf("Window.Days[%d] = %s, want %s", i, day, want)
		}
	}
	if want := f.wall(2026, time.March, 23, 0, 0); !got.Window.From.Equal(want) {
		t.Errorf("Window.From = %s, want the local midnight %s", got.Window.From, want)
	}
	if want := f.wall(2026, time.March, 30, 0, 0); !got.Window.To.Equal(want) {
		t.Errorf("Window.To = %s, want the local midnight after the last day %s", got.Window.To, want)
	}
}

// TestDashboardDayWindowHoldsOneDay pins the day view: the one local day the
// anchor names, bounded by its own midnights.
func TestDashboardDayWindowHoldsOneDay(t *testing.T) {
	t.Parallel()
	f := newDashboardFixture(t)
	anchor := domain.LocalDate{Year: 2026, Month: time.March, Day: 24}
	got := f.load(t, CalendarDay, &anchor)

	if got.Window.Mode != CalendarDay {
		t.Errorf("Window.Mode = %q, want %q", got.Window.Mode, CalendarDay)
	}
	if len(got.Window.Days) != 1 || !got.Window.Days[0].Equal(anchor) {
		t.Fatalf("Window.Days = %v, want just %s", got.Window.Days, anchor)
	}
	if want := f.wall(2026, time.March, 24, 0, 0); !got.Window.From.Equal(want) {
		t.Errorf("Window.From = %s, want the local midnight %s", got.Window.From, want)
	}
	if want := f.wall(2026, time.March, 25, 0, 0); !got.Window.To.Equal(want) {
		t.Errorf("Window.To = %s, want the local midnight after the day %s", got.Window.To, want)
	}
}

// TestDashboardDSTWeeksAreNot168Hours is the assertion the timezone story
// rests on: a week is seven local days, not a fixed 168 hours, so the week
// holding the Berlin spring-forward is 167 hours and the one holding the
// fall-back is 169.
func TestDashboardDSTWeeksAreNot168Hours(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		anchor domain.LocalDate
		from   domain.LocalDate
		to     domain.LocalDate
		want   time.Duration
	}{
		{
			name:   "spring forward week is 167 hours",
			anchor: domain.LocalDate{Year: 2026, Month: time.March, Day: 25},
			from:   domain.LocalDate{Year: 2026, Month: time.March, Day: 23},
			to:     domain.LocalDate{Year: 2026, Month: time.March, Day: 30},
			want:   167 * time.Hour,
		},
		{
			name:   "fall back week is 169 hours",
			anchor: domain.LocalDate{Year: 2026, Month: time.October, Day: 21},
			from:   domain.LocalDate{Year: 2026, Month: time.October, Day: 19},
			to:     domain.LocalDate{Year: 2026, Month: time.October, Day: 26},
			want:   169 * time.Hour,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newDashboardFixture(t)
			got := f.load(t, CalendarWeek, &tt.anchor)

			if len(got.Window.Days) != 7 {
				t.Fatalf("len(Window.Days) = %d, want 7", len(got.Window.Days))
			}
			if !got.Window.Days[0].Equal(tt.from) || !got.Window.Days[6].Equal(tt.to.AddDays(-1)) {
				t.Errorf("Window.Days = %s..%s, want %s..%s",
					got.Window.Days[0], got.Window.Days[6], tt.from, tt.to.AddDays(-1))
			}
			if want := f.wall(tt.from.Year, tt.from.Month, tt.from.Day, 0, 0); !got.Window.From.Equal(want) {
				t.Errorf("Window.From = %s, want the local midnight %s", got.Window.From, want)
			}
			if want := f.wall(tt.to.Year, tt.to.Month, tt.to.Day, 0, 0); !got.Window.To.Equal(want) {
				t.Errorf("Window.To = %s, want the local midnight %s", got.Window.To, want)
			}
			if span := got.Window.To.Sub(got.Window.From); span != tt.want {
				t.Errorf("Window.To - Window.From = %s, want %s", span, tt.want)
			}
		})
	}
}

// TestDashboardEmptyAnchorUsesTenantNow pins that a request naming no date
// renders the window holding the injected clock's now, read in the tenant's
// timezone: the clock reads 2026-03-29 in UTC but 2026-03-30 in Berlin.
func TestDashboardEmptyAnchorUsesTenantNow(t *testing.T) {
	t.Parallel()
	f := newDashboardFixture(t)
	f.clk.Set(time.Date(2026, time.March, 29, 22, 30, 0, 0, time.UTC))
	today := domain.LocalDate{Year: 2026, Month: time.March, Day: 30}

	dayView := f.load(t, CalendarDay, nil)
	if len(dayView.Window.Days) != 1 || !dayView.Window.Days[0].Equal(today) {
		t.Errorf("day view Window.Days = %v, want just the tenant-local %s", dayView.Window.Days, today)
	}
	if !dayView.Today().Equal(today) {
		t.Errorf("Overview.Today() = %s, want %s", dayView.Today(), today)
	}

	weekView := f.load(t, CalendarWeek, nil)
	if len(weekView.Window.Days) != 7 || !weekView.Window.Days[0].Equal(today) {
		t.Errorf("week view Window.Days = %v, want the week starting %s (a Monday)", weekView.Window.Days, today)
	}

	anchor := domain.LocalDate{Year: 2026, Month: time.April, Day: 2}
	anchored := f.load(t, CalendarDay, &anchor)
	if len(anchored.Window.Days) != 1 || !anchored.Window.Days[0].Equal(anchor) {
		t.Errorf("anchored Window.Days = %v, want just %s", anchored.Window.Days, anchor)
	}
}

// TestDashboardBookingsAreAttributedToDays pins the calendar's column rules: a
// booking that started before the window is shown in the window's first day
// rather than dropped, and one staff member's column holds only their own
// bookings.
func TestDashboardBookingsAreAttributedToDays(t *testing.T) {
	t.Parallel()
	f := newDashboardFixture(t)
	monday := domain.LocalDate{Year: 2026, Month: time.March, Day: 23}
	tuesday := domain.LocalDate{Year: 2026, Month: time.March, Day: 24}
	ada := domain.Staff{ID: uuid.New(), TenantID: f.tenant.ID, Name: "Ada", Active: true}
	bo := domain.Staff{ID: uuid.New(), TenantID: f.tenant.ID, Name: "Bo", Active: true}
	f.staff.staff = []domain.Staff{ada, bo}

	// Overnight is a booking the store returns because it overlaps the window
	// even though it started the day before the window opens.
	overnight := BookingInRange{
		ID: uuid.New(), StaffID: ada.ID,
		StartsAt: f.wall(2026, time.March, 22, 23, 30),
		EndsAt:   f.wall(2026, time.March, 23, 0, 10),
	}
	adaMorning := BookingInRange{
		ID: uuid.New(), StaffID: ada.ID,
		StartsAt: f.wall(2026, time.March, 24, 9, 0),
		EndsAt:   f.wall(2026, time.March, 24, 9, 30),
	}
	adaNoon := BookingInRange{
		ID: uuid.New(), StaffID: ada.ID,
		StartsAt: f.wall(2026, time.March, 24, 11, 0),
		EndsAt:   f.wall(2026, time.March, 24, 11, 30),
	}
	boMorning := BookingInRange{
		ID: uuid.New(), StaffID: bo.ID,
		StartsAt: f.wall(2026, time.March, 24, 10, 0),
		EndsAt:   f.wall(2026, time.March, 24, 10, 30),
	}
	f.bookings.bookings = []BookingInRange{overnight, adaMorning, adaNoon, boMorning}

	overview := f.load(t, CalendarWeek, nil)

	if got := overview.BookingsOn(monday); len(got) != 1 || got[0].ID != overnight.ID {
		t.Errorf("BookingsOn(Monday) = %#v, want the booking that started before the window", got)
	}
	if got := overview.BookingsOn(tuesday); len(got) != 3 {
		t.Errorf("BookingsOn(Tuesday) = %d bookings, want 3", len(got))
	}
	if got := overview.BookingsFor(ada.ID, tuesday); len(got) != 2 {
		t.Errorf("BookingsFor(Ada, Tuesday) = %d bookings, want 2", len(got))
	} else if got[0].ID != adaMorning.ID || got[1].ID != adaNoon.ID {
		t.Errorf("BookingsFor(Ada, Tuesday) = %#v, want Ada's bookings in start order", got)
	}
	if got := overview.BookingsFor(bo.ID, tuesday); len(got) != 1 || got[0].ID != boMorning.ID {
		t.Errorf("BookingsFor(Bo, Tuesday) = %#v, want just Bo's booking", got)
	}
	if got := overview.BookingsFor(ada.ID, monday); len(got) != 1 || got[0].ID != overnight.ID {
		t.Errorf("BookingsFor(Ada, Monday) = %#v, want the overnight booking", got)
	}
}

// assertOneCallPerSection is the no-N+1 assertion: four bookings in the window
// must not add a single round trip.
func assertOneCallPerSection(t *testing.T, f dashboardFixture) {
	t.Helper()

	calls := []struct {
		name  string
		count int
	}{
		{name: "tenant", count: f.tenants.byIDCalls},
		{name: "bookings", count: f.bookings.listCalls},
		{name: "staff", count: f.staff.listCalls},
		{name: "services", count: f.services.listCalls},
		{name: "audit", count: f.audit.listCalls},
		{name: "dead jobs", count: f.jobs.listCalls},
	}
	for _, c := range calls {
		if c.count != 1 {
			t.Errorf("the %s store was called %d times in one Load, want 1", c.name, c.count)
		}
	}
}

// assertTenantScopedReads checks every section was asked for the actor's tenant.
func assertTenantScopedReads(t *testing.T, f dashboardFixture) {
	t.Helper()

	if f.bookings.tenantID != f.tenant.ID || f.staff.tenantID != f.tenant.ID ||
		f.services.tenantID != f.tenant.ID || f.audit.tenantID != f.tenant.ID || f.jobs.tenantID != f.tenant.ID {
		t.Errorf("a section was read outside the actor's tenant %v: bookings %v, staff %v, services %v, audit %v, dead jobs %v",
			f.tenant.ID, f.bookings.tenantID, f.staff.tenantID, f.services.tenantID, f.audit.tenantID, f.jobs.tenantID)
	}
}

// TestDashboardLoadReadsEachSectionOnce pins that Load is one query per
// section: several bookings in the window must not grow the round trips, and
// every read is scoped to the actor's tenant and the window.
func TestDashboardLoadReadsEachSectionOnce(t *testing.T) {
	t.Parallel()
	f := newDashboardFixture(t)
	f.bookings.bookings = []BookingInRange{
		{ID: uuid.New(), StartsAt: f.wall(2026, time.March, 23, 9, 0), EndsAt: f.wall(2026, time.March, 23, 9, 30)},
		{ID: uuid.New(), StartsAt: f.wall(2026, time.March, 24, 9, 0), EndsAt: f.wall(2026, time.March, 24, 9, 30)},
		{ID: uuid.New(), StartsAt: f.wall(2026, time.March, 25, 9, 0), EndsAt: f.wall(2026, time.March, 25, 9, 30)},
		{ID: uuid.New(), StartsAt: f.wall(2026, time.March, 27, 9, 0), EndsAt: f.wall(2026, time.March, 27, 9, 30)},
	}
	f.staff.staff = []domain.Staff{{ID: uuid.New(), TenantID: f.tenant.ID, Name: "Ada", Active: true}}
	f.services.services = []domain.Service{{ID: uuid.New(), TenantID: f.tenant.ID, Name: "Cut", Active: true}}
	f.audit.entries = []AuditEntry{{ID: uuid.New(), Action: domain.AuditServiceCreated}}
	f.jobs.jobs = []DeadJob{{ID: uuid.New(), Kind: domain.JobBookingReminder, Attempts: 5}}

	got := f.load(t, CalendarWeek, nil)

	assertOneCallPerSection(t, f)
	assertTenantScopedReads(t, f)
	if !f.bookings.from.Equal(got.Window.From) || !f.bookings.to.Equal(got.Window.To) {
		t.Errorf("bookings read [%s, %s), want the window [%s, %s)", f.bookings.from, f.bookings.to, got.Window.From, got.Window.To)
	}
	if f.audit.limit != auditLogLimit {
		t.Errorf("audit limit = %d, want %d", f.audit.limit, auditLogLimit)
	}
	if f.jobs.limit != deadJobLimit {
		t.Errorf("dead jobs limit = %d, want %d", f.jobs.limit, deadJobLimit)
	}
	if len(got.Bookings) != len(f.bookings.bookings) {
		t.Errorf("Overview.Bookings = %d bookings, want the store's %d", len(got.Bookings), len(f.bookings.bookings))
	}
}
