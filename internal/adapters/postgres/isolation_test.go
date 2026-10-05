//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/adapters/postgres"
)

func TestTenantIsolation(t *testing.T) {
	t.Parallel()
	appDSN, ownerDSN := startPostgres(t)
	owner := newOwnerPool(t, ownerDSN)

	tenantA := seed(t, owner, "a")
	tenantB := seed(t, owner, "b")
	bookingA := seedBooking(t, owner, tenantA, "2026-11-02T09:00:00Z")
	bookingB := seedBooking(t, owner, tenantB, "2026-11-02T09:00:00Z")

	db := newAppDB(t, appDSN)

	assertScopedReads(t, db, tenantA.tenant, bookingA, bookingB)
	assertCrossTenantInsertRejected(t, db, tenantA.tenant, tenantB)
	assertCrossTenantUpdateTouchesNothing(t, db, tenantA.tenant, bookingB)
	assertTenantlessConnectionSeesNothing(t, appDSN)
}

func assertScopedReads(t *testing.T, db *postgres.DB, tenant, own, foreign uuid.UUID) {
	t.Helper()
	ctx := t.Context()

	err := db.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		var ownCount, foreignCount int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE id = $1::uuid", own.String()).Scan(&ownCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM bookings WHERE id = $1::uuid", foreign.String()).Scan(&foreignCount); err != nil {
			return err
		}
		if ownCount != 1 {
			t.Errorf("tenant sees %d of its own bookings, want 1", ownCount)
		}
		if foreignCount != 0 {
			t.Errorf("tenant sees %d of another tenant's bookings, want 0", foreignCount)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading as tenant: %v", err)
	}
}

func assertCrossTenantInsertRejected(t *testing.T, db *postgres.DB, tenant uuid.UUID, victim fixture) {
	t.Helper()
	ctx := t.Context()

	err := db.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			INSERT INTO bookings (tenant_id, staff_id, service_id, customer_name, customer_email,
			                      starts_at, ends_at, status, idempotency_key)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'Mallory', 'mallory@example.com',
			        '2026-11-03T09:00:00Z', '2026-11-03T09:30:00Z', 'confirmed', 'cross-tenant')`,
			victim.tenant.String(), victim.staff.String(), victim.service.String())
		return execErr
	})

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Errorf("cross-tenant insert returned %v, want SQLSTATE 42501", err)
	}
}

func assertCrossTenantUpdateTouchesNothing(t *testing.T, db *postgres.DB, tenant, foreign uuid.UUID) {
	t.Helper()
	ctx := t.Context()

	err := db.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE bookings SET status = 'cancelled' WHERE id = $1::uuid", foreign.String())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 0 {
			t.Errorf("tenant updated %d of another tenant's bookings, want 0", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("updating as tenant: %v", err)
	}
}

func assertTenantlessConnectionSeesNothing(t *testing.T, appDSN string) {
	t.Helper()
	ctx := t.Context()

	free, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening tenantless connection: %v", err)
	}
	t.Cleanup(free.Close)

	var visible int
	if err := free.QueryRow(ctx, "SELECT count(*) FROM bookings").Scan(&visible); err != nil {
		t.Fatalf("counting without a tenant: %v", err)
	}
	if visible != 0 {
		t.Errorf("tenantless connection sees %d bookings, want 0", visible)
	}
}
