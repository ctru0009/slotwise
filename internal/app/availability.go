package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// Search and write limits. The range caps keep one request a bounded amount of
// work; the rule cap keeps a week from being described by an unbounded list.
const (
	maxSearchDays  = 62
	maxWeeklyRules = 50
	maxTimeOffDays = 366
)

// WeeklyRuleInput is one row of a replace-week request.
type WeeklyRuleInput struct {
	Weekday     time.Weekday
	StartMinute int
	EndMinute   int
}

// Availability implements the weekly schedule, time off and slot search use
// cases. A search is public: it takes no actor and resolves the tenant directly.
type Availability struct {
	store   AvailabilityStore
	tenants TenantStore
	clock   clock.Clock
}

// NewAvailability returns an Availability backed by store, resolving tenant
// timezones through tenants and the current instant through clk.
func NewAvailability(store AvailabilityStore, tenants TenantStore, clk clock.Clock) *Availability {
	return &Availability{store: store, tenants: tenants, clock: clk}
}

// Search returns the bookable starts for the tenant's active service and staff
// over the inclusive local date range, ordered by start and then staff id. The
// result is never nil.
func (a *Availability) Search(ctx context.Context, tenantID, serviceID uuid.UUID, from, to domain.LocalDate) ([]domain.Slot, error) {
	loc, err := tenantLocation(ctx, a.tenants, tenantID)
	if err != nil {
		return nil, err
	}
	if err := validateDateRange(from, to, maxSearchDays, "must span at most 62 days"); err != nil {
		return nil, err
	}

	lo, hi := domain.DayStart(from, loc), domain.DayStart(to.Next(), loc)
	snapshot, err := a.store.SlotSnapshot(ctx, tenantID, serviceID, lo, hi)
	if err != nil {
		return nil, fmt.Errorf("reading slot snapshot: %w", err)
	}

	notBefore := a.clock.Now()
	slots := []domain.Slot{}
	for _, schedule := range snapshot.Staff {
		starts := domain.SlotTimes(slotQuery(loc, snapshot.Service, schedule, from, to, notBefore))
		for _, start := range starts {
			slots = append(slots, domain.Slot{StaffID: schedule.Staff.ID, StaffName: schedule.Staff.Name, Start: start})
		}
	}
	slices.SortFunc(slots, compareSlots)
	return slots, nil
}

// ListWeeklyRules returns the staff member's weekly windows. Both roles may
// read; the result is never nil.
func (a *Availability) ListWeeklyRules(ctx context.Context, actor domain.User, staffID uuid.UUID) ([]domain.WeeklyRule, error) {
	rules, err := a.store.ListWeeklyRules(ctx, actor.TenantID, staffID)
	if err != nil {
		return nil, fmt.Errorf("listing weekly rules: %w", err)
	}
	if rules == nil {
		rules = []domain.WeeklyRule{}
	}
	return rules, nil
}

// SetWeeklyRules replaces the staff member's whole week with the validated,
// deduplicated rules, sorted by (weekday, start, end). Only owners may write;
// an empty list is a valid week with no availability.
func (a *Availability) SetWeeklyRules(ctx context.Context, actor domain.User, staffID uuid.UUID, rules []WeeklyRuleInput) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	normalised, err := normaliseRules(rules)
	if err != nil {
		return err
	}
	if err := a.store.ReplaceWeeklyRules(ctx, actor.TenantID, staffID, normalised); err != nil {
		return fmt.Errorf("replacing weekly rules: %w", err)
	}
	return nil
}

// ListTimeOff returns the staff member's absences. Both roles may read; the
// result is never nil.
func (a *Availability) ListTimeOff(ctx context.Context, actor domain.User, staffID uuid.UUID) ([]domain.TimeOff, error) {
	entries, err := a.store.ListTimeOff(ctx, actor.TenantID, staffID)
	if err != nil {
		return nil, fmt.Errorf("listing time off: %w", err)
	}
	if entries == nil {
		entries = []domain.TimeOff{}
	}
	return entries, nil
}

// AddTimeOff stores one absence covering the inclusive local date range, using
// the tenant's timezone so the range is the tenant's own day boundaries. Only
// owners may write.
func (a *Availability) AddTimeOff(ctx context.Context, actor domain.User, staffID uuid.UUID, from, to domain.LocalDate) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	if err := validateDateRange(from, to, maxTimeOffDays, "must span at most 366 days"); err != nil {
		return err
	}
	loc, err := tenantLocation(ctx, a.tenants, actor.TenantID)
	if err != nil {
		return err
	}
	lo, hi := domain.DayStart(from, loc), domain.DayStart(to.Next(), loc)
	if !hi.After(lo) {
		return domain.ValidationError{Field: "from", Message: "must be a date in the tenant's timezone"}
	}
	if err := a.store.InsertTimeOff(ctx, actor.TenantID, staffID, lo, hi); err != nil {
		return fmt.Errorf("inserting time off: %w", err)
	}
	return nil
}

// RemoveTimeOff deletes one absence. Only owners may write; an id outside the
// staff member's rows stays domain.ErrNotFound.
func (a *Availability) RemoveTimeOff(ctx context.Context, actor domain.User, staffID, id uuid.UUID) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	if err := a.store.DeleteTimeOff(ctx, actor.TenantID, staffID, id); err != nil {
		return fmt.Errorf("deleting time off: %w", err)
	}
	return nil
}

// tenantLocation loads a tenant's IANA timezone. Both the search and the
// booking path resolve the tenant's own clock through it.
func tenantLocation(ctx context.Context, tenants TenantStore, tenantID uuid.UUID) (*time.Location, error) {
	tenant, err := tenants.TenantByID(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("resolving tenant: %w", err)
	}
	loc, err := time.LoadLocation(tenant.Timezone)
	if err != nil {
		return nil, fmt.Errorf("loading tenant timezone %q: %w", tenant.Timezone, err)
	}
	return loc, nil
}

// slotQuery assembles the engine input for one staff member, so the search and
// the booking path cannot drift apart in what they consider bookable.
func slotQuery(loc *time.Location, service domain.Service, schedule domain.StaffSchedule, from, to domain.LocalDate, notBefore time.Time) domain.SlotQuery {
	return domain.SlotQuery{
		Location:  loc,
		Rules:     schedule.Rules,
		Busy:      schedule.Busy,
		TimeOff:   schedule.TimeOff,
		From:      from,
		To:        to,
		Block:     service.Block(),
		NotBefore: notBefore,
	}
}

// normaliseRules validates every rule, drops exact duplicates and sorts by
// (weekday, start, end), so the store only ever sees one canonical week.
func normaliseRules(rules []WeeklyRuleInput) ([]WeeklyRuleInput, error) {
	if len(rules) > maxWeeklyRules {
		return nil, domain.ValidationError{Field: "rules", Message: fmt.Sprintf("must contain at most %d rules", maxWeeklyRules)}
	}
	normalised := make([]WeeklyRuleInput, 0, len(rules))
	for i, rule := range rules {
		if err := rule.validate(i); err != nil {
			return nil, err
		}
		normalised = append(normalised, rule)
	}
	slices.SortFunc(normalised, compareRuleInputs)
	return slices.Compact(normalised), nil
}

// validate reports the first out-of-bounds field of rule i.
func (r WeeklyRuleInput) validate(i int) error {
	switch {
	case r.Weekday < time.Sunday || r.Weekday > time.Saturday:
		return domain.ValidationError{Field: fmt.Sprintf("rules[%d].weekday", i), Message: "must be between 0 and 6"}
	case r.StartMinute < 0 || r.StartMinute > 1439:
		return domain.ValidationError{Field: fmt.Sprintf("rules[%d].start_minute", i), Message: "must be between 0 and 1439"}
	case r.EndMinute < 1 || r.EndMinute > 1440:
		return domain.ValidationError{Field: fmt.Sprintf("rules[%d].end_minute", i), Message: "must be between 1 and 1440"}
	case r.EndMinute <= r.StartMinute:
		return domain.ValidationError{Field: fmt.Sprintf("rules[%d].end_minute", i), Message: "must be after start_minute"}
	}
	return nil
}

// validateDateRange checks that an inclusive local range is real, ascending and
// no longer than maxDays, naming the field the caller should fix.
func validateDateRange(from, to domain.LocalDate, maxDays int, spanMessage string) error {
	if !from.Valid() {
		return domain.ValidationError{Field: "from", Message: "must be a real calendar date"}
	}
	if !to.Valid() {
		return domain.ValidationError{Field: "to", Message: "must be a real calendar date"}
	}
	if from.After(to) {
		return domain.ValidationError{Field: "to", Message: "must not be before from"}
	}
	if daysInclusive(from, to) > maxDays {
		return domain.ValidationError{Field: "to", Message: spanMessage}
	}
	return nil
}

// daysInclusive counts the local dates from through to. Both are real calendar
// dates, so the UTC instants differ by whole days.
func daysInclusive(from, to domain.LocalDate) int {
	lo := time.Date(from.Year, from.Month, from.Day, 0, 0, 0, 0, time.UTC)
	hi := time.Date(to.Year, to.Month, to.Day, 0, 0, 0, 0, time.UTC)
	return int(hi.Sub(lo)/(24*time.Hour)) + 1
}

// compareSlots orders slots by start, then staff id, so one search has one
// stable order even when several staff members share a start.
func compareSlots(a, b domain.Slot) int {
	if c := a.Start.Compare(b.Start); c != 0 {
		return c
	}
	return slices.Compare(a.StaffID[:], b.StaffID[:])
}

// compareRuleInputs orders rules by weekday, then start, then end.
func compareRuleInputs(a, b WeeklyRuleInput) int {
	if a.Weekday != b.Weekday {
		return int(a.Weekday) - int(b.Weekday)
	}
	if a.StartMinute != b.StartMinute {
		return a.StartMinute - b.StartMinute
	}
	return a.EndMinute - b.EndMinute
}
