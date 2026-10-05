package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ctru0009/slotwise/internal/adapters/postgres/dbgen"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

var _ app.AvailabilityStore = (*DB)(nil)

// SlotSnapshot reads one search in a single tenant-scoped transaction: the
// active service, the active staff, and per staff member their weekly rules,
// time off and confirmed busy intervals overlapping [from, to). An unknown or
// inactive service reports domain.ErrNotFound; staff the tenant cannot see
// never appear. The result is never nil.
func (db *DB) SlotSnapshot(ctx context.Context, tenantID, serviceID uuid.UUID, from, to time.Time) (domain.SlotSnapshot, error) {
	var snapshot domain.SlotSnapshot
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		read, err := readSlotSnapshot(ctx, dbgen.New(tx), serviceID, from, to)
		if err != nil {
			return err
		}
		snapshot = read
		return nil
	})
	if err != nil {
		return domain.SlotSnapshot{}, err
	}
	return snapshot, nil
}

// readSlotSnapshot runs the five snapshot statements against one transaction
// and groups the rows by staff id. Rules, time off and busy rows for staff the
// snapshot does not include, because they are inactive or invisible, are
// dropped instead of failing.
func readSlotSnapshot(ctx context.Context, q *dbgen.Queries, serviceID uuid.UUID, from, to time.Time) (domain.SlotSnapshot, error) {
	service, err := q.ServiceForSlotSearch(ctx, serviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SlotSnapshot{}, fmt.Errorf("service %s: %w", serviceID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.SlotSnapshot{}, fmt.Errorf("reading active service: %w", err)
	}

	staffRows, err := q.ActiveStaffForSlotSearch(ctx)
	if err != nil {
		return domain.SlotSnapshot{}, fmt.Errorf("reading active staff: %w", err)
	}
	snapshot := domain.SlotSnapshot{
		Service: domain.Service{
			ID:              service.ID,
			TenantID:        service.TenantID,
			Name:            service.Name,
			DurationMinutes: int(service.DurationMinutes),
			BufferMinutes:   int(service.BufferMinutes),
			PriceCents:      int(service.PriceCents),
			Active:          service.Active,
		},
		Staff: make([]domain.StaffSchedule, 0, len(staffRows)),
	}
	for _, row := range staffRows {
		snapshot.Staff = append(snapshot.Staff, domain.StaffSchedule{
			Staff: domain.Staff{
				ID:       row.ID,
				TenantID: row.TenantID,
				Name:     row.Name,
				Email:    row.Email,
				Active:   row.Active,
			},
		})
	}
	at := make(map[uuid.UUID]int, len(snapshot.Staff))
	for i := range snapshot.Staff {
		at[snapshot.Staff[i].Staff.ID] = i
	}

	if err := attachRules(ctx, q, snapshot.Staff, at); err != nil {
		return domain.SlotSnapshot{}, err
	}
	if err := attachTimeOff(ctx, q, snapshot.Staff, at, from, to); err != nil {
		return domain.SlotSnapshot{}, err
	}
	if err := attachBusy(ctx, q, snapshot.Staff, at, from, to); err != nil {
		return domain.SlotSnapshot{}, err
	}
	return snapshot, nil
}

// attachRules adds each weekly rule to its staff member's schedule.
func attachRules(ctx context.Context, q *dbgen.Queries, schedules []domain.StaffSchedule, at map[uuid.UUID]int) error {
	rows, err := q.RulesForSlotSearch(ctx)
	if err != nil {
		return fmt.Errorf("reading weekly rules: %w", err)
	}
	for _, row := range rows {
		i, ok := at[row.StaffID]
		if !ok {
			continue
		}
		schedules[i].Rules = append(schedules[i].Rules, domain.WeeklyRule{
			Weekday:     time.Weekday(row.Weekday),
			StartMinute: int(row.StartMinute),
			EndMinute:   int(row.EndMinute),
		})
	}
	return nil
}

// attachTimeOff adds each absence overlapping the range to its staff member's
// schedule.
func attachTimeOff(ctx context.Context, q *dbgen.Queries, schedules []domain.StaffSchedule, at map[uuid.UUID]int, from, to time.Time) error {
	rows, err := q.TimeOffForSlotSearch(ctx, dbgen.TimeOffForSlotSearchParams{RangeStart: from, RangeEnd: to})
	if err != nil {
		return fmt.Errorf("reading time off: %w", err)
	}
	for _, row := range rows {
		i, ok := at[row.StaffID]
		if !ok {
			continue
		}
		schedules[i].TimeOff = append(schedules[i].TimeOff, domain.Interval{Start: row.StartsAt, End: row.EndsAt})
	}
	return nil
}

// attachBusy adds each confirmed booking overlapping the range to its staff
// member's schedule. The stored ends_at already includes the service's buffer,
// so the interval is used as it is.
func attachBusy(ctx context.Context, q *dbgen.Queries, schedules []domain.StaffSchedule, at map[uuid.UUID]int, from, to time.Time) error {
	rows, err := q.BusyForSlotSearch(ctx, dbgen.BusyForSlotSearchParams{RangeStart: from, RangeEnd: to})
	if err != nil {
		return fmt.Errorf("reading busy intervals: %w", err)
	}
	for _, row := range rows {
		i, ok := at[row.StaffID]
		if !ok {
			continue
		}
		schedules[i].Busy = append(schedules[i].Busy, domain.Interval{Start: row.StartsAt, End: row.EndsAt})
	}
	return nil
}

// ListWeeklyRules returns the staff member's weekly windows ordered by weekday
// and start. The result is never nil.
func (db *DB) ListWeeklyRules(ctx context.Context, tenantID, staffID uuid.UUID) ([]domain.WeeklyRule, error) {
	rules := []domain.WeeklyRule{}
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := dbgen.New(tx).ListWeeklyRulesForStaff(ctx, staffID)
		if err != nil {
			return fmt.Errorf("listing weekly rules: %w", err)
		}
		for _, row := range rows {
			rules = append(rules, domain.WeeklyRule{
				Weekday:     time.Weekday(row.Weekday),
				StartMinute: int(row.StartMinute),
				EndMinute:   int(row.EndMinute),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rules, nil
}

// ReplaceWeeklyRules replaces the staff member's whole week in one transaction:
// the row lock, the delete and the inserts commit together, so a failure never
// leaves a half-replaced week and two concurrent replacements for the same
// staff member serialize instead of interleaving into the union of both weeks.
// The lock also makes an invisible staff id report domain.ErrNotFound instead
// of leaking a foreign key error.
func (db *DB) ReplaceWeeklyRules(ctx context.Context, tenantID, staffID uuid.UUID, rules []app.WeeklyRuleInput) error {
	return db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := dbgen.New(tx)
		if err := lockStaffRow(ctx, q, staffID); err != nil {
			return err
		}
		if err := q.DeleteWeeklyRulesForStaff(ctx, staffID); err != nil {
			return fmt.Errorf("deleting weekly rules: %w", err)
		}
		for _, rule := range rules {
			if err := q.InsertWeeklyRule(ctx, dbgen.InsertWeeklyRuleParams{
				TenantID:    tenantID,
				StaffID:     staffID,
				Weekday:     narrowInt16(int(rule.Weekday)),
				StartMinute: narrowInt32(rule.StartMinute),
				EndMinute:   narrowInt32(rule.EndMinute),
			}); err != nil {
				return fmt.Errorf("inserting weekly rule: %w", err)
			}
		}
		return nil
	})
}

// ListTimeOff returns the staff member's absences ordered by start. The result
// is never nil.
func (db *DB) ListTimeOff(ctx context.Context, tenantID, staffID uuid.UUID) ([]domain.TimeOff, error) {
	entries := []domain.TimeOff{}
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := dbgen.New(tx).ListTimeOffForStaff(ctx, staffID)
		if err != nil {
			return fmt.Errorf("listing time off: %w", err)
		}
		for _, row := range rows {
			entries = append(entries, domain.TimeOff{
				ID:       row.ID,
				StaffID:  row.StaffID,
				StartsAt: row.StartsAt,
				EndsAt:   row.EndsAt,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// InsertTimeOff stores one absence. An invisible staff id reports
// domain.ErrNotFound instead of leaking a foreign key error.
func (db *DB) InsertTimeOff(ctx context.Context, tenantID, staffID uuid.UUID, from, to time.Time) error {
	return db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := dbgen.New(tx)
		if err := requireStaffVisible(ctx, q, staffID); err != nil {
			return err
		}
		if _, err := q.InsertTimeOff(ctx, dbgen.InsertTimeOffParams{
			TenantID: tenantID,
			StaffID:  staffID,
			StartsAt: from,
			EndsAt:   to,
		}); err != nil {
			return fmt.Errorf("inserting time off: %w", err)
		}
		return nil
	})
}

// DeleteTimeOff deletes one absence. An id that does not exist for the staff
// member, including another tenant's row, reports domain.ErrNotFound.
func (db *DB) DeleteTimeOff(ctx context.Context, tenantID, staffID, id uuid.UUID) error {
	return db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		affected, err := dbgen.New(tx).DeleteTimeOffForStaff(ctx, dbgen.DeleteTimeOffForStaffParams{
			StaffID: staffID,
			ID:      id,
		})
		if err != nil {
			return fmt.Errorf("deleting time off: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("time off %s: %w", id, domain.ErrNotFound)
		}
		return nil
	})
}

// requireStaffVisible reports domain.ErrNotFound when the staff member is not
// visible in the tenant's transaction: RLS makes another tenant's row
// indistinguishable from a missing one.
func requireStaffVisible(ctx context.Context, q *dbgen.Queries, staffID uuid.UUID) error {
	_, err := q.StaffVisible(ctx, staffID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("staff %s: %w", staffID, domain.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("checking staff %s: %w", staffID, err)
	}
	return nil
}

// lockStaffRow takes the staff member's row lock inside the tenant's
// transaction, serializing concurrent replacements for the same staff member,
// and reports domain.ErrNotFound when the row is not visible.
func lockStaffRow(ctx context.Context, q *dbgen.Queries, staffID uuid.UUID) error {
	_, err := q.LockStaff(ctx, staffID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("staff %s: %w", staffID, domain.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("locking staff %s: %w", staffID, err)
	}
	return nil
}

// narrowInt16 fits a validated weekday into its column type. The app layer
// validates weekday to 0..6, so the narrowing cannot overflow.
func narrowInt16(v int) int16 {
	return int16(v) //nolint:gosec // the app layer's range cannot overflow int16
}
