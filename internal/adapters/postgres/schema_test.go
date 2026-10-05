//go:build integration

package postgres_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSchemaInvariants stops the pen from drifting. Requirement 2 depends on
// every table the application can reach having row level security enabled and
// forced plus at least one policy, and on the application connecting as a role
// that cannot bypass any of it. Without this test a future migration can add a
// table, or drop FORCE, and the isolation tests stay green.
func TestSchemaInvariants(t *testing.T) {
	t.Parallel()
	appDSN, _ := startPostgres(t)
	pool := newAppPool(t, appDSN)

	assertReachableTablesAreProtected(t, pool)
	assertBookkeepingIsUnreachable(t, pool)
	assertRoleCannotBypassPolicies(t, pool)
}

// TestMigrationsRequireTheAppRole pins the deploy-time guard. 0001 granted
// privileges only when the application role already existed, so a deployment
// that created it later got a green migration and then failed at request time
// with "permission denied"; 0002 asserts the precondition instead.
func TestMigrationsRequireTheAppRole(t *testing.T) {
	t.Parallel()
	_, ownerDSN := startContainer(t)

	err := applyMigrations(t.Context(), ownerDSN)
	if err == nil {
		t.Fatal("migrations succeeded without the slotwise_app role, so the grants silently did nothing")
	}
	if !strings.Contains(err.Error(), "slotwise_app") {
		t.Errorf("migration error %v does not name the missing role", err)
	}
}

func assertReachableTablesAreProtected(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()

	rows, err := pool.Query(ctx, `
		SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       has_table_privilege(current_user, c.oid, 'SELECT,INSERT,UPDATE,DELETE'),
		       (SELECT count(*) FROM pg_policies p
		         WHERE p.schemaname = 'public' AND p.tablename = c.relname)
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind = 'r'
		 ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	defer rows.Close()

	tables, reachable := 0, 0
	for rows.Next() {
		var (
			name     string
			enabled  bool
			forced   bool
			canReach bool
			policies int
		)
		if err := rows.Scan(&name, &enabled, &forced, &canReach, &policies); err != nil {
			t.Fatalf("scanning table invariants: %v", err)
		}
		tables++
		if !canReach {
			continue
		}
		reachable++
		if !enabled || !forced || policies == 0 {
			t.Errorf("table %s is reachable by the app role but rls=%v force=%v policies=%d, want enabled, forced and at least one policy",
				name, enabled, forced, policies)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading tables: %v", err)
	}
	if tables == 0 || reachable == 0 {
		t.Fatalf("invariant test saw %d tables and %d reachable ones, it is proving nothing", tables, reachable)
	}
}

func assertBookkeepingIsUnreachable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	var reachable bool
	err := pool.QueryRow(t.Context(),
		"SELECT has_table_privilege(current_user, 'public.goose_db_version', 'SELECT,INSERT,UPDATE,DELETE')").
		Scan(&reachable)
	if err != nil {
		t.Fatalf("checking goose access: %v", err)
	}
	if reachable {
		t.Error("the app role can reach goose_db_version, migration bookkeeping is not application data")
	}
}

func assertRoleCannotBypassPolicies(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()

	var super, bypass bool
	err := pool.QueryRow(ctx,
		"SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&super, &bypass)
	if err != nil {
		t.Fatalf("reading role attributes: %v", err)
	}
	if super || bypass {
		t.Errorf("application role is superuser=%v bypassrls=%v, want false false", super, bypass)
	}

	var owned int
	err = pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind = 'r'
		   AND c.relowner = (SELECT oid FROM pg_roles WHERE rolname = current_user)`).Scan(&owned)
	if err != nil {
		t.Fatalf("counting owned tables: %v", err)
	}
	if owned != 0 {
		t.Errorf("application role owns %d tables in public, so it can disable their policies", owned)
	}
}
