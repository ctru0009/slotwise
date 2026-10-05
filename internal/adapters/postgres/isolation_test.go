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
	assertCrossTenantReferencesRejected(t, db, tenantA.tenant, tenantB, bookingA)
	assertCrossTenantUpdateTouchesNothing(t, db, tenantA.tenant, bookingB)
	assertCrossTenantDeleteTouchesNothing(t, db, tenantA.tenant, bookingB)
	assertMalformedTenantMatchesNothing(t, db, tenantA.tenant)
	assertTenantlessConnectionSeesNothing(t, appDSN)
}

// hasSQLState reports whether err carries a specific Postgres SQLSTATE.
func hasSQLState(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
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

	if !hasSQLState(err, "42501") {
		t.Errorf("cross-tenant insert returned %v, want SQLSTATE 42501", err)
	}
}

// assertCrossTenantReferencesRejected proves a booking cannot name another
// tenant's staff or service. Referential checks run as the table owner and
// bypass row security, so only tenant-consistent foreign keys stop this — the
// policy alone accepts it, because tenant_id matches the caller.
func assertCrossTenantReferencesRejected(t *testing.T, db *postgres.DB, tenant uuid.UUID, victim fixture, ownBooking uuid.UUID) {
	t.Helper()
	ctx := t.Context()

	insertErr := db.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			INSERT INTO bookings (tenant_id, staff_id, service_id, customer_name, customer_email,
			                      starts_at, ends_at, status, idempotency_key)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'Mallory', 'mallory@example.com',
			        '2026-12-01T09:00:00Z', '2026-12-01T09:30:00Z', 'confirmed', 'foreign-staff')`,
			tenant.String(), victim.staff.String(), victim.service.String())
		return execErr
	})
	if !hasSQLState(insertErr, "23503") {
		t.Errorf("booking pointed at another tenant's staff returned %v, want SQLSTATE 23503", insertErr)
	}

	// A free window on the victim's calendar, so only the tenant-consistent
	// foreign key can reject this: the exclusion constraint has nothing to
	// collide with, which is exactly the squat the exploit used.
	updateErr := db.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			UPDATE bookings
			   SET staff_id = $1::uuid,
			       starts_at = '2026-12-02T09:00:00Z',
			       ends_at = '2026-12-02T09:30:00Z'
			 WHERE id = $2::uuid`,
			victim.staff.String(), ownBooking.String())
		return execErr
	})
	if !hasSQLState(updateErr, "23503") {
		t.Errorf("moving a booking onto another tenant's staff returned %v, want SQLSTATE 23503", updateErr)
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

func assertCrossTenantDeleteTouchesNothing(t *testing.T, db *postgres.DB, tenant, foreign uuid.UUID) {
	t.Helper()
	ctx := t.Context()

	err := db.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM bookings WHERE id = $1::uuid", foreign.String())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 0 {
			t.Errorf("tenant deleted %d of another tenant's bookings, want 0", tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("deleting as tenant: %v", err)
	}
}

// assertMalformedTenantMatchesNothing covers the two ways the tenant setting can
// be wrong: empty, which the policy helper turns into NULL, and not a uuid, which
// must fail instead of matching anything.
func assertMalformedTenantMatchesNothing(t *testing.T, db *postgres.DB, tenant uuid.UUID) {
	t.Helper()
	ctx := t.Context()

	emptyErr := db.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', '', true)"); err != nil {
			return err
		}
		var visible int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM bookings").Scan(&visible); err != nil {
			return err
		}
		if visible != 0 {
			t.Errorf("empty tenant id saw %d bookings, want 0", visible)
		}
		return nil
	})
	if emptyErr != nil {
		t.Fatalf("reading with an empty tenant id: %v", emptyErr)
	}

	garbageErr := db.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', 'not-a-uuid', true)"); err != nil {
			return err
		}
		var visible int
		return tx.QueryRow(ctx, "SELECT count(*) FROM bookings").Scan(&visible)
	})
	if garbageErr == nil {
		t.Error("a non-uuid tenant id was accepted, want an error")
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
