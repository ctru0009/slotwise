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

// ListServices returns the tenant's services ordered by name. The result is
// never nil, so callers can range over it without a length check.
func (db *DB) ListServices(ctx context.Context, tenantID uuid.UUID) ([]domain.Service, error) {
	services := []domain.Service{}
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := dbgen.New(tx).ListServices(ctx)
		if err != nil {
			return fmt.Errorf("listing services: %w", err)
		}
		for _, row := range rows {
			services = append(services, domain.Service{
				ID:              row.ID,
				TenantID:        row.TenantID,
				Name:            row.Name,
				DurationMinutes: int(row.DurationMinutes),
				BufferMinutes:   int(row.BufferMinutes),
				PriceCents:      int(row.PriceCents),
				Active:          row.Active,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return services, nil
}

// CreateService inserts a validated service into the tenant.
func (db *DB) CreateService(ctx context.Context, tenantID uuid.UUID, in app.ServiceInput) error {
	return db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := dbgen.New(tx).InsertService(ctx, dbgen.InsertServiceParams{
			TenantID:        tenantID,
			Name:            in.Name,
			DurationMinutes: narrowInt32(in.DurationMinutes),
			BufferMinutes:   narrowInt32(in.BufferMinutes),
			PriceCents:      narrowInt32(in.PriceCents),
		})
		if err != nil {
			return fmt.Errorf("inserting service: %w", err)
		}
		return nil
	})
}

// UpdateService replaces a service's mutable fields. An id that does not exist,
// including another tenant's row, reports domain.ErrNotFound: row level
// security makes the two indistinguishable, and callers render both as 404.
func (db *DB) UpdateService(ctx context.Context, tenantID, id uuid.UUID, in app.ServiceInput) error {
	return db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		affected, err := dbgen.New(tx).UpdateService(ctx, dbgen.UpdateServiceParams{
			Name:            in.Name,
			DurationMinutes: narrowInt32(in.DurationMinutes),
			BufferMinutes:   narrowInt32(in.BufferMinutes),
			PriceCents:      narrowInt32(in.PriceCents),
			ID:              id,
		})
		if err != nil {
			return fmt.Errorf("updating service: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("service %s: %w", id, domain.ErrNotFound)
		}
		return nil
	})
}

// SetServiceActive hides or unhides a service without deleting it, so bookings
// that reference it keep their history. A row the tenant cannot see reports
// domain.ErrNotFound, the same as UpdateService.
func (db *DB) SetServiceActive(ctx context.Context, tenantID, id uuid.UUID, active bool) error {
	return db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		affected, err := dbgen.New(tx).SetServiceActive(ctx, dbgen.SetServiceActiveParams{
			Active: active,
			ID:     id,
		})
		if err != nil {
			return fmt.Errorf("setting service active: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("service %s: %w", id, domain.ErrNotFound)
		}
		return nil
	})
}

var _ app.ServiceStore = (*DB)(nil)

// narrowInt32 fits a validated input field into its column type. The app layer
// validates duration to 1..1440, buffer to 0..240 and price to 0..10_000_000,
// so every value that reaches this adapter is far inside int32 and the
// narrowing cannot overflow.
func narrowInt32(v int) int32 {
	return int32(v) //nolint:gosec // the app layer's ranges cannot overflow int32
}
