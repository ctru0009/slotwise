//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/internal/domain"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

func TestTenantIsolation(t *testing.T) {
	t.Parallel()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)

	tenantA := pgtest.Seed(t, owner, "a")
	tenantB := pgtest.Seed(t, owner, "b")
	bookingA := pgtest.SeedBooking(t, owner, tenantA, "2026-11-02T09:00:00Z")
	bookingB := pgtest.SeedBooking(t, owner, tenantB, "2026-11-02T09:00:00Z")

	db := pgtest.AppDB(t, appDSN)

	assertScopedReads(t, db, tenantA.Tenant, bookingA, bookingB)
	assertCrossTenantInsertRejected(t, db, tenantA.Tenant, tenantB)
	assertCrossTenantReferencesRejected(t, db, tenantA.Tenant, tenantB, bookingA)
	assertCrossTenantUpdateTouchesNothing(t, db, tenantA.Tenant, bookingB)
	assertCrossTenantDeleteTouchesNothing(t, db, tenantA.Tenant, bookingB)
	assertAuthTablesIsolated(t, db, appDSN, owner, tenantA, tenantB)
	assertAvailabilityTablesIsolated(t, db, owner, tenantA, tenantB)
	assertJobsIsolated(t, db, appDSN, owner, tenantA, tenantB)
	assertMalformedTenantMatchesNothing(t, db, tenantA.Tenant)
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

func assertCrossTenantInsertRejected(t *testing.T, db *postgres.DB, tenant uuid.UUID, victim pgtest.Fixture) {
	t.Helper()
	ctx := t.Context()

	err := db.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			INSERT INTO bookings (tenant_id, staff_id, service_id, customer_name, customer_email,
			                      starts_at, ends_at, status, idempotency_key)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'Mallory', 'mallory@example.com',
			        '2026-11-03T09:00:00Z', '2026-11-03T09:30:00Z', 'confirmed', 'cross-tenant')`,
			victim.Tenant.String(), victim.Staff.String(), victim.Service.String())
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
func assertCrossTenantReferencesRejected(t *testing.T, db *postgres.DB, tenant uuid.UUID, victim pgtest.Fixture, ownBooking uuid.UUID) {
	t.Helper()
	ctx := t.Context()

	insertErr := db.WithTenant(ctx, tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			INSERT INTO bookings (tenant_id, staff_id, service_id, customer_name, customer_email,
			                      starts_at, ends_at, status, idempotency_key)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'Mallory', 'mallory@example.com',
			        '2026-12-01T09:00:00Z', '2026-12-01T09:30:00Z', 'confirmed', 'foreign-staff')`,
			tenant.String(), victim.Staff.String(), victim.Service.String())
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
			victim.Staff.String(), ownBooking.String())
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

// assertTenantlessConnectionSeesNothing checks that a connection with no tenant
// set reads no bookings and no jobs, so a missing set_config cannot leak another
// tenant's data.
func assertTenantlessConnectionSeesNothing(t *testing.T, appDSN string) {
	t.Helper()
	ctx := t.Context()

	free, err := pgxpool.New(ctx, appDSN)
	if err != nil {
		t.Fatalf("opening tenantless connection: %v", err)
	}
	t.Cleanup(free.Close)

	var bookings, jobs int
	if err := free.QueryRow(ctx, "SELECT count(*) FROM bookings").Scan(&bookings); err != nil {
		t.Fatalf("counting bookings without a tenant: %v", err)
	}
	if err := free.QueryRow(ctx, "SELECT count(*) FROM jobs").Scan(&jobs); err != nil {
		t.Fatalf("counting jobs without a tenant: %v", err)
	}
	if bookings != 0 || jobs != 0 {
		t.Errorf("a tenantless connection sees %d bookings and %d jobs, want 0 and 0", bookings, jobs)
	}
}

// assertAuthTablesIsolated proves the 0003 tables sit behind the same tenant
// policy as the M1 tables. users and password_reset_tokens are invisible to
// another tenant, writable only under their own tenant_id, and a reset token
// can only be spent inside the tenant that issued it.
func assertAuthTablesIsolated(t *testing.T, db *postgres.DB, appDSN string, owner *pgxpool.Pool, tenantA, tenantB pgtest.Fixture) {
	t.Helper()

	userA, userB, tokenB := seedAuthRows(t, owner, tenantA, tenantB)
	assertAuthReadsScoped(t, db, tenantA, userA, userB, tokenB)
	assertAuthRowsUntouchable(t, db, owner, tenantA, tenantB, userB, tokenB)
	assertCrossTenantUserInsertRejected(t, db, tenantA, tenantB)
	assertResetTokenScoped(t, db, appDSN, owner, tenantA, tenantB, userB, tokenB)
}

func seedAuthRows(t *testing.T, owner *pgxpool.Pool, tenantA, tenantB pgtest.Fixture) (uuid.UUID, uuid.UUID, []byte) {
	t.Helper()

	userA := pgtest.SeedUser(t, owner, tenantA, "a-owner@example.com", "hash-a", "owner")
	userB := pgtest.SeedUser(t, owner, tenantB, "b-owner@example.com", "hash-b", "owner")
	tokenB := tokenHash("tenant-b-reset")
	_, err := owner.Exec(t.Context(), `
		INSERT INTO password_reset_tokens (token_hash, tenant_id, user_id, expires_at)
		VALUES ($1, $2, $3, now() + interval '1 hour')`, tokenB, tenantB.Tenant, userB)
	if err != nil {
		t.Fatalf("seeding tenant B's reset token: %v", err)
	}
	return userA, userB, tokenB
}

// assertAuthReadsScoped checks that tenant A reads its own auth rows and sees
// none of B's.
func assertAuthReadsScoped(t *testing.T, db *postgres.DB, tenantA pgtest.Fixture, userA, userB uuid.UUID, tokenB []byte) {
	t.Helper()
	ctx := t.Context()

	err := db.WithTenant(ctx, tenantA.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		var ownUser, foreignUser, foreignToken int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM users WHERE id = $1", userA).Scan(&ownUser); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM users WHERE id = $1", userB).Scan(&foreignUser); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM password_reset_tokens WHERE token_hash = $1", tokenB).Scan(&foreignToken); err != nil {
			return err
		}
		if ownUser != 1 {
			t.Errorf("tenant A sees %d of its own users, want 1", ownUser)
		}
		if foreignUser != 0 {
			t.Errorf("tenant A sees %d of tenant B's users, want 0", foreignUser)
		}
		if foreignToken != 0 {
			t.Errorf("tenant A sees %d of tenant B's reset tokens, want 0", foreignToken)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading auth tables as tenant A: %v", err)
	}
}

// assertAuthRowsUntouchable checks that A's updates and deletes reach none of
// B's rows, and that B's rows are intact afterwards.
func assertAuthRowsUntouchable(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenantA, tenantB pgtest.Fixture, userB uuid.UUID, tokenB []byte) {
	t.Helper()
	ctx := t.Context()

	err := db.WithTenant(ctx, tenantA.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		updateUser, err := tx.Exec(ctx, "UPDATE users SET password_hash = 'hijacked' WHERE id = $1", userB)
		if err != nil {
			return err
		}
		if updateUser.RowsAffected() != 0 {
			t.Errorf("tenant A updated %d of tenant B's users, want 0", updateUser.RowsAffected())
		}
		updateToken, err := tx.Exec(ctx, "UPDATE password_reset_tokens SET used_at = now() WHERE token_hash = $1", tokenB)
		if err != nil {
			return err
		}
		if updateToken.RowsAffected() != 0 {
			t.Errorf("tenant A updated %d of tenant B's reset tokens, want 0", updateToken.RowsAffected())
		}
		deleteToken, err := tx.Exec(ctx, "DELETE FROM password_reset_tokens WHERE token_hash = $1", tokenB)
		if err != nil {
			return err
		}
		if deleteToken.RowsAffected() != 0 {
			t.Errorf("tenant A deleted %d of tenant B's reset tokens, want 0", deleteToken.RowsAffected())
		}
		deleteUser, err := tx.Exec(ctx, "DELETE FROM users WHERE id = $1", userB)
		if err != nil {
			return err
		}
		if deleteUser.RowsAffected() != 0 {
			t.Errorf("tenant A deleted %d of tenant B's users, want 0", deleteUser.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("writing auth tables as tenant A: %v", err)
	}

	assertUserRow(t, owner, tenantB.Tenant, userB, "b-owner@example.com", "hash-b")
	assertOpenResetToken(t, owner, userB, tokenB)
}

// assertCrossTenantUserInsertRejected checks that A cannot insert a user under
// B's tenant id: the WITH CHECK clause rejects the row.
func assertCrossTenantUserInsertRejected(t *testing.T, db *postgres.DB, tenantA, tenantB pgtest.Fixture) {
	t.Helper()

	insertErr := db.WithTenant(t.Context(), tenantA.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO users (id, tenant_id, email, password_hash, role)
			VALUES (gen_random_uuid(), $1, 'mallory@example.com', 'hash', 'owner')`, tenantB.Tenant)
		return err
	})
	if !hasSQLState(insertErr, "42501") {
		t.Errorf("inserting a user for another tenant returned %v, want SQLSTATE 42501", insertErr)
	}
}

// assertResetTokenScoped checks that A cannot spend B's token, that the failed
// attempt leaves it open for B, and that a connection without a tenant sees
// neither auth table.
func assertResetTokenScoped(t *testing.T, db *postgres.DB, appDSN string, owner *pgxpool.Pool, tenantA, tenantB pgtest.Fixture, userB uuid.UUID, tokenB []byte) {
	t.Helper()
	ctx := t.Context()

	if _, err := db.ConsumeResetToken(ctx, tenantA.Tenant, tokenB, "hijack-hash"); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("consuming another tenant's reset token = %v, want domain.ErrResetTokenInvalid", err)
	}
	assertUserRow(t, owner, tenantB.Tenant, userB, "b-owner@example.com", "hash-b")
	if _, err := db.ConsumeResetToken(ctx, tenantB.Tenant, tokenB, "b-new-hash"); err != nil {
		t.Errorf("tenant B could not consume its own token after tenant A's attempt: %v", err)
	}

	tenantless := pgtest.AppPool(t, appDSN)
	var users, tokens int
	if err := tenantless.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&users); err != nil {
		t.Fatalf("counting users without a tenant: %v", err)
	}
	if err := tenantless.QueryRow(ctx, "SELECT count(*) FROM password_reset_tokens").Scan(&tokens); err != nil {
		t.Fatalf("counting reset tokens without a tenant: %v", err)
	}
	if users != 0 || tokens != 0 {
		t.Errorf("a tenantless connection sees %d users and %d reset tokens, want 0 and 0", users, tokens)
	}
}

func assertUserRow(t *testing.T, owner *pgxpool.Pool, tenantID, id uuid.UUID, email, passwordHash string) {
	t.Helper()

	var (
		gotTenant uuid.UUID
		gotEmail  string
		gotHash   string
	)
	err := owner.QueryRow(t.Context(),
		"SELECT tenant_id, email, password_hash FROM users WHERE id = $1", id).
		Scan(&gotTenant, &gotEmail, &gotHash)
	if err != nil {
		t.Fatalf("reading user %s as the owner: %v", id, err)
	}
	if gotTenant != tenantID || gotEmail != email || gotHash != passwordHash {
		t.Errorf("user %s = (tenant %s, email %q, hash %q), want (%s, %q, %q)",
			id, gotTenant, gotEmail, gotHash, tenantID, email, passwordHash)
	}
}

// assertAvailabilityTablesIsolated proves the M3 tables sit behind the same
// tenant policy as the M1 tables: tenant A reads none of B's rules or absences,
// its updates and deletes touch none of B's rows, and an insert naming B's
// staff member is rejected by the tenant-consistent foreign key even though the
// row's own tenant id matches.
func assertAvailabilityTablesIsolated(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenantA, tenantB pgtest.Fixture) {
	t.Helper()

	ruleB := pgtest.SeedWeeklyRule(t, owner, tenantB, 1, 540, 600)
	offB := pgtest.SeedTimeOff(t, owner, tenantB, "2026-11-02T09:00:00Z", "2026-11-02T17:00:00Z")

	assertAvailabilityReadsScoped(t, db, tenantA, ruleB, offB)
	assertAvailabilityRowsUntouchable(t, db, owner, tenantA, ruleB, offB)
	assertCrossTenantAvailabilityInsertRejected(t, db, tenantA, tenantB)
}

// assertAvailabilityReadsScoped checks that tenant A sees none of tenant B's
// availability rows.
func assertAvailabilityReadsScoped(t *testing.T, db *postgres.DB, tenantA pgtest.Fixture, ruleB, offB uuid.UUID) {
	t.Helper()

	err := db.WithTenant(t.Context(), tenantA.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		var rules, absences int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM availability_rules WHERE id = $1", ruleB).Scan(&rules); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM time_off WHERE id = $1", offB).Scan(&absences); err != nil {
			return err
		}
		if rules != 0 {
			t.Errorf("tenant A sees %d of tenant B's rules, want 0", rules)
		}
		if absences != 0 {
			t.Errorf("tenant A sees %d of tenant B's absences, want 0", absences)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading availability tables as tenant A: %v", err)
	}
}

// assertAvailabilityRowsUntouchable checks that A's updates and deletes reach
// none of B's rows, and that B's rows are intact afterwards.
func assertAvailabilityRowsUntouchable(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenantA pgtest.Fixture, ruleB, offB uuid.UUID) {
	t.Helper()

	statements := []struct {
		name string
		sql  string
		id   uuid.UUID
	}{
		{"updated a rule", "UPDATE availability_rules SET start_minute = 0 WHERE id = $1", ruleB},
		{"deleted a rule", "DELETE FROM availability_rules WHERE id = $1", ruleB},
		{"updated an absence", "UPDATE time_off SET ends_at = now() WHERE id = $1", offB},
		{"deleted an absence", "DELETE FROM time_off WHERE id = $1", offB},
	}
	err := db.WithTenant(t.Context(), tenantA.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, stmt := range statements {
			tag, err := tx.Exec(ctx, stmt.sql, stmt.id)
			if err != nil {
				return fmt.Errorf("%s: %w", stmt.name, err)
			}
			if tag.RowsAffected() != 0 {
				t.Errorf("tenant A %s of tenant B's rows, want 0 rows affected", stmt.name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("writing availability tables as tenant A: %v", err)
	}
	assertAvailabilityRowsIntact(t, owner, ruleB, offB)
}

// assertAvailabilityRowsIntact re-reads B's rows as the owner, after A's
// rejected writes.
func assertAvailabilityRowsIntact(t *testing.T, owner *pgxpool.Pool, ruleB, offB uuid.UUID) {
	t.Helper()

	var startMinute int
	if err := owner.QueryRow(t.Context(), "SELECT start_minute FROM availability_rules WHERE id = $1", ruleB).Scan(&startMinute); err != nil {
		t.Fatalf("reading tenant B's rule as the owner: %v", err)
	}
	if startMinute != 540 {
		t.Errorf("tenant B's rule start_minute = %d, want its original 540", startMinute)
	}

	var endsAt time.Time
	if err := owner.QueryRow(t.Context(), "SELECT ends_at FROM time_off WHERE id = $1", offB).Scan(&endsAt); err != nil {
		t.Fatalf("reading tenant B's absence as the owner: %v", err)
	}
	if !endsAt.Equal(time.Date(2026, time.November, 2, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("tenant B's absence ends_at = %v, want its original 17:00Z", endsAt)
	}
}

// assertCrossTenantAvailabilityInsertRejected covers both cross-tenant shapes:
// a row whose tenant id is B's is rejected by the policy (42501), and a row
// under A's own tenant that names B's staff is rejected by the composite
// foreign key (23503), which the policy alone would accept.
func assertCrossTenantAvailabilityInsertRejected(t *testing.T, db *postgres.DB, tenantA, tenantB pgtest.Fixture) {
	t.Helper()
	ctx := t.Context()

	policyErr := db.WithTenant(ctx, tenantA.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO availability_rules (tenant_id, staff_id, weekday, start_minute, end_minute)
			VALUES ($1::uuid, $2::uuid, 1, 540, 600)`, tenantB.Tenant, tenantB.Staff)
		return err
	})
	if !hasSQLState(policyErr, "42501") {
		t.Errorf("inserting a rule for another tenant returned %v, want SQLSTATE 42501", policyErr)
	}

	ruleErr := db.WithTenant(ctx, tenantA.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO availability_rules (tenant_id, staff_id, weekday, start_minute, end_minute)
			VALUES ($1::uuid, $2::uuid, 1, 540, 600)`, tenantA.Tenant, tenantB.Staff)
		return err
	})
	if !hasSQLState(ruleErr, "23503") {
		t.Errorf("rule pointed at another tenant's staff returned %v, want SQLSTATE 23503", ruleErr)
	}

	offErr := db.WithTenant(ctx, tenantA.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO time_off (tenant_id, staff_id, starts_at, ends_at)
			VALUES ($1::uuid, $2::uuid, '2026-11-05T09:00:00Z', '2026-11-05T10:00:00Z')`,
			tenantA.Tenant, tenantB.Staff)
		return err
	})
	if !hasSQLState(offErr, "23503") {
		t.Errorf("absence pointed at another tenant's staff returned %v, want SQLSTATE 23503", offErr)
	}
}

// assertJobsIsolated proves the queue sits behind the same tenant policy as
// every other table: tenant A reads none of B's jobs, its writes touch none of
// B's rows, and both cross-tenant insert shapes are rejected — the policy
// refuses B's tenant id, and the composite foreign key refuses A's own tenant
// pointing at B's booking. job_claim is the one sanctioned exception, and it is
// pinned in the job store tests, not here.
func assertJobsIsolated(t *testing.T, db *postgres.DB, appDSN string, owner *pgxpool.Pool, tenantA, tenantB pgtest.Fixture) {
	t.Helper()

	bookingB := pgtest.SeedBooking(t, owner, tenantB, "2026-11-04T09:00:00Z")
	jobB := pgtest.SeedJob(t, owner, tenantB.Tenant, bookingB, "booking_confirmation", time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))

	assertJobsReadsScoped(t, db, tenantA.Tenant, jobB)
	assertJobsWritesUntouchable(t, db, owner, tenantA.Tenant, jobB)
	assertCrossTenantJobsInsertRejected(t, db, tenantA, tenantB, bookingB)
	assertTenantlessConnectionSeesNoJobs(t, appDSN)
}

// assertJobsReadsScoped checks that tenant A sees none of tenant B's jobs.
func assertJobsReadsScoped(t *testing.T, db *postgres.DB, tenantA, jobB uuid.UUID) {
	t.Helper()

	err := db.WithTenant(t.Context(), tenantA, func(ctx context.Context, tx pgx.Tx) error {
		var own, foreign int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM jobs").Scan(&own); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE id = $1", jobB).Scan(&foreign); err != nil {
			return err
		}
		if own != 0 {
			t.Errorf("tenant A sees %d jobs, want 0 of tenant B's", own)
		}
		if foreign != 0 {
			t.Errorf("tenant A sees %d of tenant B's jobs, want 0", foreign)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading jobs as tenant A: %v", err)
	}
}

// assertJobsWritesUntouchable checks that A's updates and deletes reach none of
// B's rows, and that B's job is intact afterwards.
func assertJobsWritesUntouchable(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenantA, jobB uuid.UUID) {
	t.Helper()

	err := db.WithTenant(t.Context(), tenantA, func(ctx context.Context, tx pgx.Tx) error {
		update, err := tx.Exec(ctx, "UPDATE jobs SET status = 'dead' WHERE id = $1", jobB)
		if err != nil {
			return err
		}
		if update.RowsAffected() != 0 {
			t.Errorf("tenant A updated %d of tenant B's jobs, want 0", update.RowsAffected())
		}
		del, err := tx.Exec(ctx, "DELETE FROM jobs WHERE id = $1", jobB)
		if err != nil {
			return err
		}
		if del.RowsAffected() != 0 {
			t.Errorf("tenant A deleted %d of tenant B's jobs, want 0", del.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("writing jobs as tenant A: %v", err)
	}

	var status string
	if err := owner.QueryRow(t.Context(), "SELECT status FROM jobs WHERE id = $1", jobB).Scan(&status); err != nil {
		t.Fatalf("reading tenant B's job as the owner: %v", err)
	}
	if status != "ready" {
		t.Errorf("tenant B's job status = %q after tenant A's writes, want its original ready", status)
	}
}

// assertCrossTenantJobsInsertRejected covers both insert shapes: B's tenant id
// under A's context is refused by the policy, and A's own tenant naming B's
// booking is refused by the tenant-consistent foreign key.
func assertCrossTenantJobsInsertRejected(t *testing.T, db *postgres.DB, tenantA, tenantB pgtest.Fixture, bookingB uuid.UUID) {
	t.Helper()
	ctx := t.Context()

	policyErr := db.WithTenant(ctx, tenantA.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO jobs (tenant_id, booking_id, kind, run_at)
			VALUES ($1::uuid, $2::uuid, 'booking_confirmation', now())`, tenantB.Tenant, bookingB)
		return err
	})
	if !hasSQLState(policyErr, "42501") {
		t.Errorf("inserting a job for another tenant returned %v, want SQLSTATE 42501", policyErr)
	}

	fkViolation := db.WithTenant(ctx, tenantA.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO jobs (tenant_id, booking_id, kind, run_at)
			VALUES ($1::uuid, $2::uuid, 'booking_confirmation', now())`, tenantA.Tenant, bookingB)
		return err
	})
	if !hasSQLState(fkViolation, "23503") {
		t.Errorf("job pointed at another tenant's booking returned %v, want SQLSTATE 23503", fkViolation)
	}
}

// assertTenantlessConnectionSeesNoJobs checks that a connection with no tenant
// set reads no queue rows at all.
func assertTenantlessConnectionSeesNoJobs(t *testing.T, appDSN string) {
	t.Helper()

	tenantless := pgtest.AppPool(t, appDSN)
	var visible int
	if err := tenantless.QueryRow(t.Context(), "SELECT count(*) FROM jobs").Scan(&visible); err != nil {
		t.Fatalf("counting jobs without a tenant: %v", err)
	}
	if visible != 0 {
		t.Errorf("a tenantless connection sees %d jobs, want 0", visible)
	}
}
