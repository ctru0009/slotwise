package app

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// CalendarMode is the window the owner's calendar renders.
type CalendarMode string

// The windows the dashboard renders. Values are the strings a calendar request
// puts in its mode parameter.
const (
	CalendarDay  CalendarMode = "day"
	CalendarWeek CalendarMode = "week"
)

// Calendar limits. Both lists are capped, so one tenant's backlog cannot grow a
// page without bound.
const (
	auditLogLimit = 50
	deadJobLimit  = 20
	weekLength    = 7
)

// ParseCalendarMode reads the mode a calendar request named. An empty value is
// the week view, which is what a first visit gets.
func ParseCalendarMode(raw string) (CalendarMode, error) {
	if raw == "" {
		return CalendarWeek, nil
	}
	mode := CalendarMode(raw)
	if !mode.Valid() {
		return "", domain.ValidationError{Field: "mode", Message: "must be day or week"}
	}
	return mode, nil
}

// Valid reports whether m is a mode the dashboard renders.
func (m CalendarMode) Valid() bool {
	return m == CalendarDay || m == CalendarWeek
}

// CalendarWindow is the half-open instant range one view renders and the local
// days it covers. The days are tenant-local dates, so a week holding a DST
// transition is 167 or 169 hours long and still reads as seven days.
type CalendarWindow struct {
	Mode CalendarMode
	Days []domain.LocalDate
	From time.Time
	To   time.Time
}

// Anchor returns the window's first local day.
func (w CalendarWindow) Anchor() domain.LocalDate {
	return w.Days[0]
}

// Today returns the tenant's current local date, which is the day a landing
// page starts its search from and the day the toolbar's "today" link jumps to.
func (o Overview) Today() domain.LocalDate {
	return localDateOf(o.Now, o.Location)
}

// BookingsOn returns the bookings the calendar shows in one local day's column,
// in start order. A booking that started before the window is shown in the
// window's first day rather than dropped: it still occupies the calendar.
func (o Overview) BookingsOn(day domain.LocalDate) []BookingInRange {
	on := []BookingInRange{}
	first := o.Window.Days[0]
	for _, booking := range o.Bookings {
		started := localDateOf(booking.StartsAt, o.Location)
		if first.After(started) {
			started = first
		}
		if started.Equal(day) {
			on = append(on, booking)
		}
	}
	return on
}

// BookingsFor returns the bookings of one staff member on one local day, which
// is what one calendar cell holds.
func (o Overview) BookingsFor(staffID uuid.UUID, day domain.LocalDate) []BookingInRange {
	mine := []BookingInRange{}
	for _, booking := range o.BookingsOn(day) {
		if booking.StaffID == staffID {
			mine = append(mine, booking)
		}
	}
	return mine
}

// BookingInRange is one booking as the calendar and the booking list render it.
// Both display names come from the same query, so no row costs a second read.
type BookingInRange struct {
	ID           uuid.UUID
	StaffID      uuid.UUID
	ServiceID    uuid.UUID
	StaffName    string
	ServiceName  string
	CustomerName string
	StartsAt     time.Time
	EndsAt       time.Time
	Status       domain.BookingStatus
}

// AuditEntry is one audit row with the labels it resolves to. Which actor and
// subject field is filled follows from the action.
type AuditEntry struct {
	ID              uuid.UUID
	Action          domain.AuditAction
	CreatedAt       time.Time
	ActorEmail      string
	BookingCustomer string
	BookingStarts   *time.Time
	ServiceName     string
	StaffName       string
}

// DeadJob is one dead-lettered queue row.
type DeadJob struct {
	ID        uuid.UUID
	Kind      domain.JobKind
	Attempts  int
	LastError string
	DiedAt    time.Time
}

// Overview is everything the owner's dashboard renders for one window.
type Overview struct {
	Tenant   domain.Tenant
	Location *time.Location
	Window   CalendarWindow
	Now      time.Time
	Bookings []BookingInRange
	Staff    []domain.Staff
	Services []domain.Service
	Audit    []AuditEntry
	DeadJobs []DeadJob
}

// Dashboard loads the owner's overview, one tenant-scoped query per section.
// Both roles may read it; only the catalogue writes behind it are owner-only.
type Dashboard struct {
	tenants  TenantStore
	bookings BookingStore
	staff    StaffStore
	services ServiceStore
	audit    AuditStore
	jobs     JobStore
	clock    clock.Clock
}

// NewDashboard returns a Dashboard backed by the stores each section reads,
// taking the current instant from clk.
func NewDashboard(tenants TenantStore, bookings BookingStore, staff StaffStore, services ServiceStore, audit AuditStore, jobs JobStore, clk clock.Clock) *Dashboard {
	return &Dashboard{tenants: tenants, bookings: bookings, staff: staff, services: services, audit: audit, jobs: jobs, clock: clk}
}

// Load renders the window containing anchor, or the window containing now when
// the request named no date.
func (d *Dashboard) Load(ctx context.Context, actor domain.User, mode CalendarMode, anchor *domain.LocalDate) (Overview, error) {
	if !mode.Valid() {
		return Overview{}, domain.ValidationError{Field: "mode", Message: "must be day or week"}
	}
	tenant, err := d.tenants.TenantByID(ctx, actor.TenantID)
	if err != nil {
		return Overview{}, fmt.Errorf("resolving tenant: %w", err)
	}
	loc, err := time.LoadLocation(tenant.Timezone)
	if err != nil {
		return Overview{}, fmt.Errorf("loading tenant timezone %q: %w", tenant.Timezone, err)
	}

	now := d.clock.Now()
	window := calendarWindow(mode, anchor, loc, now)
	bookings, err := d.bookings.ListBookingsInRange(ctx, actor.TenantID, window.From, window.To)
	if err != nil {
		return Overview{}, fmt.Errorf("listing bookings in range: %w", err)
	}
	staff, err := d.staff.ListStaff(ctx, actor.TenantID)
	if err != nil {
		return Overview{}, fmt.Errorf("listing staff: %w", err)
	}
	services, err := d.services.ListServices(ctx, actor.TenantID)
	if err != nil {
		return Overview{}, fmt.Errorf("listing services: %w", err)
	}
	entries, err := d.audit.ListAuditLog(ctx, actor.TenantID, auditLogLimit)
	if err != nil {
		return Overview{}, fmt.Errorf("listing audit log: %w", err)
	}
	dead, err := d.jobs.ListDeadJobs(ctx, actor.TenantID, deadJobLimit)
	if err != nil {
		return Overview{}, fmt.Errorf("listing dead jobs: %w", err)
	}
	return Overview{
		Tenant:   tenant,
		Location: loc,
		Window:   window,
		Now:      now,
		Bookings: bookings,
		Staff:    staff,
		Services: services,
		Audit:    entries,
		DeadJobs: dead,
	}, nil
}

// calendarWindow returns the window to render: the day or the Monday-to-Sunday
// week that holds anchor, or the one holding now when the request named no
// date. The instant range runs from the first local midnight to the one after
// the last local day, so a DST transition inside the window shortens or
// lengthens it without dropping or repeating a day.
func calendarWindow(mode CalendarMode, anchor *domain.LocalDate, loc *time.Location, now time.Time) CalendarWindow {
	first := localDateOf(now, loc)
	if anchor != nil {
		first = *anchor
	}
	if mode == CalendarWeek {
		for first.Weekday() != time.Monday {
			first = first.AddDays(-1)
		}
	}
	days := make([]domain.LocalDate, 0, weekLength)
	count := 1
	if mode == CalendarWeek {
		count = weekLength
	}
	for d := first; len(days) < count; d = d.AddDays(1) {
		days = append(days, d)
	}
	return CalendarWindow{
		Mode: mode,
		Days: days,
		From: domain.DayStart(days[0], loc),
		To:   domain.DayStart(days[len(days)-1].AddDays(1), loc),
	}
}
