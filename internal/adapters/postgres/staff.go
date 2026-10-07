package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ctru0009/slotwise/internal/adapters/postgres/dbgen"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

// ListStaff returns the tenant's staff ordered by name. The result is never
// nil, so callers can range over it without a length check.
func (db *DB) ListStaff(ctx context.Context, tenantID uuid.UUID) ([]domain.Staff, error) {
	staff := []domain.Staff{}
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := dbgen.New(tx).ListStaff(ctx)
		if err != nil {
			return fmt.Errorf("listing staff: %w", err)
		}
		for _, row := range rows {
			staff = append(staff, domain.Staff{
				ID:       row.ID,
				TenantID: row.TenantID,
				Name:     row.Name,
				Email:    row.Email,
				Active:   row.Active,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return staff, nil
}

// CreateStaff inserts a validated staff member into the tenant and records who
// added it, in one transaction.
func (db *DB) CreateStaff(ctx context.Context, tenantID, actor uuid.UUID, in app.StaffInput) error {
	return db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := dbgen.New(tx)
		id, err := q.InsertStaff(ctx, dbgen.InsertStaffParams{
			TenantID: tenantID,
			Name:     in.Name,
			Email:    in.Email,
		})
		if err != nil {
			return fmt.Errorf("inserting staff: %w", err)
		}
		return recordAudit(ctx, q, tenantID, actor, domain.AuditStaffCreated, auditSubject{staff: id})
	})
}

// UpdateStaff replaces a staff member's name and email. An id that does not
// exist, including another tenant's row, reports domain.ErrNotFound.
func (db *DB) UpdateStaff(ctx context.Context, tenantID, actor, id uuid.UUID, in app.StaffInput) error {
	return db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := dbgen.New(tx)
		affected, err := q.UpdateStaff(ctx, dbgen.UpdateStaffParams{
			Name:  in.Name,
			Email: in.Email,
			ID:    id,
		})
		if err != nil {
			return fmt.Errorf("updating staff: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("staff %s: %w", id, domain.ErrNotFound)
		}
		return recordAudit(ctx, q, tenantID, actor, domain.AuditStaffUpdated, auditSubject{staff: id})
	})
}

// SetStaffActive hides or unhides a staff member without deleting it, so
// bookings that reference it keep their history. A row the tenant cannot see
// reports domain.ErrNotFound, the same as UpdateStaff.
func (db *DB) SetStaffActive(ctx context.Context, tenantID, actor, id uuid.UUID, active bool) error {
	return db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := dbgen.New(tx)
		affected, err := q.SetStaffActive(ctx, dbgen.SetStaffActiveParams{
			Active: active,
			ID:     id,
		})
		if err != nil {
			return fmt.Errorf("setting staff active: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("staff %s: %w", id, domain.ErrNotFound)
		}
		return recordAudit(ctx, q, tenantID, actor, activeAction(active, domain.AuditStaffActivated, domain.AuditStaffDeactivated), auditSubject{staff: id})
	})
}

var _ app.StaffStore = (*DB)(nil)
