//go:build integration

package postgres_test

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// TestSchemaInvariants stops the pen from drifting. Requirement 2 depends on
// every table the application can reach having row level security enabled and
// forced plus at least one policy, and on the application connecting as a role
// that cannot bypass any of it. Without this test a future migration can add a
// table, or drop FORCE, and the isolation tests stay green.
func TestSchemaInvariants(t *testing.T) {
	t.Parallel()
	appDSN, _ := pgtest.Start(t)
	pool := pgtest.AppPool(t, appDSN)

	assertReachableTablesAreProtected(t, pool)
	assertBookkeepingIsUnreachable(t, pool)
	assertPreTenantTablesAreUnreachable(t, pool)
	assertResolverFunctionsAreNarrow(t, pool)
	assertTenantResolverExposesOnlyPublicColumns(t, pool)
	assertRoleCannotBypassPolicies(t, pool)
}

// assertTenantResolverExposesOnlyPublicColumns pins the resolver's return type.
// The slug it is called with is the caller's own input, so the function must
// return exactly the three public columns and nothing a later migration could
// widen into a leak.
func assertTenantResolverExposesOnlyPublicColumns(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	const want = "TABLE(id uuid, name text, timezone text)"
	var got string
	err := pool.QueryRow(t.Context(),
		"SELECT pg_catalog.pg_get_function_result('public.tenant_by_slug(text)'::pg_catalog.regprocedure)").Scan(&got)
	if err != nil {
		t.Fatalf("reading the tenant resolver's result type: %v", err)
	}
	if got != want {
		t.Errorf("tenant_by_slug returns %q, want %q", got, want)
	}
}

// TestMigrationsRequireTheAppRole pins the deploy-time guard. 0001 granted
// privileges only when the application role already existed, so a deployment
// that created it later got a green migration and then failed at request time
// with "permission denied"; 0002 asserts the precondition instead.
func TestMigrationsRequireTheAppRole(t *testing.T) {
	t.Parallel()
	_, ownerDSN := pgtest.StartBare(t)

	err := pgtest.Migrate(t.Context(), ownerDSN)
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

// assertPreTenantTablesAreUnreachable covers the other function-only table.
// sessions has no policy, so the revoke is the only thing keeping tenant-owned
// connections away from it; a future grant would otherwise expose every
// tenant's session data through the default privileges of 0002.
func assertPreTenantTablesAreUnreachable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	var reachable bool
	err := pool.QueryRow(t.Context(),
		"SELECT has_table_privilege(current_user, 'public.sessions', 'SELECT,INSERT,UPDATE,DELETE')").
		Scan(&reachable)
	if err != nil {
		t.Fatalf("checking sessions access: %v", err)
	}
	if reachable {
		t.Error("the app role can reach sessions directly, so the SECURITY DEFINER functions are not the only path")
	}
}

// resolverFunctions are the SECURITY DEFINER functions 0003 adds. They are the
// only pre-tenant reads and writes, so each one must run as its definer, pin
// search_path, be unreachable for PUBLIC and executable for the app role.
var resolverFunctions = []string{
	"tenant_by_slug",
	"session_find",
	"session_commit",
	"session_delete",
	"session_purge",
}

func assertResolverFunctionsAreNarrow(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	rows, err := pool.Query(t.Context(), `
		SELECT p.proname,
		       p.prosecdef,
		       COALESCE(p.proconfig, '{}'),
		       p.proacl IS NULL,
		       COALESCE((SELECT array_agg(
		                   CASE WHEN a.grantee = 0 THEN 'PUBLIC'
		                        ELSE pg_catalog.pg_get_userbyid(a.grantee) END
		                   || ':' || a.privilege_type)
		                   FROM pg_catalog.aclexplode(p.proacl) a), '{}'::text[])
		  FROM pg_catalog.pg_proc p
		  JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		 WHERE n.nspname = 'public' AND p.proname::text = ANY($1::text[])
		 ORDER BY p.proname`, resolverFunctions)
	if err != nil {
		t.Fatalf("listing resolver functions: %v", err)
	}
	defer rows.Close()

	seen := make(map[string]bool, len(resolverFunctions))
	for rows.Next() {
		var (
			name      string
			secdef    bool
			proconfig []string
			aclNull   bool
			acl       []string
		)
		if err := rows.Scan(&name, &secdef, &proconfig, &aclNull, &acl); err != nil {
			t.Fatalf("scanning resolver function: %v", err)
		}
		seen[name] = true
		assertResolverFunction(t, name, secdef, proconfig, aclNull, acl)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading resolver functions: %v", err)
	}
	for _, name := range resolverFunctions {
		if !seen[name] {
			t.Errorf("resolver function %s is missing", name)
		}
	}
}

// assertResolverFunction checks one function's row from pg_proc: it must run as
// its definer, pin search_path, and grant EXECUTE to the app role and no one
// else. A NULL ACL is the default state where PUBLIC holds EXECUTE, so it is a
// failure in itself.
func assertResolverFunction(t *testing.T, name string, secdef bool, proconfig []string, aclNull bool, acl []string) {
	t.Helper()

	if !secdef {
		t.Errorf("%s is not SECURITY DEFINER, so it cannot read the tenant or session tables either", name)
	}
	if !hasSearchPath(proconfig) {
		t.Errorf("%s does not pin search_path: proconfig = %q", name, proconfig)
	}
	if aclNull {
		t.Errorf("%s has a NULL ACL, so PUBLIC still holds the default EXECUTE", name)
		return
	}
	grantedToApp := false
	for _, entry := range acl {
		if strings.HasPrefix(entry, "PUBLIC:") {
			t.Errorf("%s still grants %s to PUBLIC", name, entry)
		}
		if entry == "slotwise_app:EXECUTE" {
			grantedToApp = true
		}
	}
	if !grantedToApp {
		t.Errorf("%s does not grant EXECUTE to slotwise_app, acl = %q", name, acl)
	}
}

func hasSearchPath(proconfig []string) bool {
	for _, entry := range proconfig {
		if strings.HasPrefix(entry, "search_path=") {
			return true
		}
	}
	return false
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
