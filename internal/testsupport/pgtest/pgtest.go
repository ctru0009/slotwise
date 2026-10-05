//go:build integration

package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	// goose's provider API takes a database/sql handle, so the pgx stdlib
	// driver has to be registered for it to open the migration connection.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/migrations"
)

const (
	appRole     = "slotwise_app"
	appPassword = "app-password"
	adminDSN    = "postgres://postgres:postgres@%s:%s/slotwise?sslmode=disable"
	tenantDSN   = "postgres://" + appRole + ":" + appPassword + "@%s:%s/slotwise?sslmode=disable&pool_max_conns=20"
)

// Fixture is one tenant with a staff member and a service.
type Fixture struct {
	Tenant  uuid.UUID
	Staff   uuid.UUID
	Service uuid.UUID
}

// StartBare runs a throwaway Postgres. The application role is not created
// here, so tests can exercise the migration path that expects it to be missing.
func StartBare(t *testing.T) (appDSN, ownerDSN string) {
	t.Helper()
	ctx := t.Context()

	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("slotwise"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		t.Fatalf("starting postgres: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminating container: %v", err)
		}
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}

	ownerDSN = fmt.Sprintf(adminDSN, host, port.Port())
	appDSN = fmt.Sprintf(tenantDSN, host, port.Port())
	return appDSN, ownerDSN
}

// Start is StartBare plus the application role and the committed migrations,
// which is what most integration tests need.
func Start(t *testing.T) (appDSN, ownerDSN string) {
	t.Helper()
	appDSN, ownerDSN = StartBare(t)
	createAppRole(t, ownerDSN)
	migrate(t, ownerDSN)
	return appDSN, ownerDSN
}

func createAppRole(t *testing.T, ownerDSN string) {
	t.Helper()
	owner := Owner(t, ownerDSN)
	_, err := owner.Exec(t.Context(),
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOBYPASSRLS", appRole, appPassword))
	if err != nil {
		t.Fatalf("creating %s: %v", appRole, err)
	}
}

// Migrate applies every migration with goose, the same way a deploy would. The
// Provider API is used instead of goose's package-level setters: those are
// globals, and parallel tests would race on them. It returns the error instead
// of failing the test so callers can assert on the migration outcome.
func Migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("opening migration connection: %w", err)
	}
	defer func() { _ = db.Close() }()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("building goose provider: %w", err)
	}
	defer func() { _ = provider.Close() }()

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	return nil
}

func migrate(t *testing.T, dsn string) {
	t.Helper()
	if err := Migrate(t.Context(), dsn); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}
}

// Owner opens a pool connected as the database owner, which bypasses row level
// security.
func Owner(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// AppDB opens the application's DB handle connected as the application role.
func AppDB(t *testing.T, dsn string) *postgres.DB {
	t.Helper()
	db, err := postgres.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("opening app connection: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// AppPool opens a pool connected as the application role.
func AppPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting as the app role: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Seed inserts one tenant with a staff member and a service and returns their
// ids.
func Seed(t *testing.T, owner *pgxpool.Pool, slug string) Fixture {
	t.Helper()
	ctx := t.Context()

	var tenantID, staffID, serviceID string
	err := owner.QueryRow(ctx,
		`INSERT INTO tenants (slug, name, timezone) VALUES ($1, $2, 'Europe/Berlin') RETURNING id::text`,
		slug, "Tenant "+slug).Scan(&tenantID)
	if err != nil {
		t.Fatalf("seeding tenant %s: %v", slug, err)
	}
	err = owner.QueryRow(ctx,
		`INSERT INTO staff (tenant_id, name, email) VALUES ($1::uuid, $2, $3) RETURNING id::text`,
		tenantID, "Staff "+slug, slug+"@example.com").Scan(&staffID)
	if err != nil {
		t.Fatalf("seeding staff for %s: %v", slug, err)
	}
	err = owner.QueryRow(ctx,
		`INSERT INTO services (tenant_id, name, duration_minutes) VALUES ($1::uuid, $2, 30) RETURNING id::text`,
		tenantID, "Cut "+slug).Scan(&serviceID)
	if err != nil {
		t.Fatalf("seeding service for %s: %v", slug, err)
	}
	return Fixture{Tenant: uuid.MustParse(tenantID), Staff: uuid.MustParse(staffID), Service: uuid.MustParse(serviceID)}
}

// SeedStaff adds another staff member to the same tenant, so multi-row lock
// tests have a second row to use.
func SeedStaff(t *testing.T, owner *pgxpool.Pool, f Fixture, name, email string) uuid.UUID {
	t.Helper()

	var staffID string
	err := owner.QueryRow(t.Context(),
		`INSERT INTO staff (tenant_id, name, email) VALUES ($1::uuid, $2, $3) RETURNING id::text`,
		f.Tenant.String(), name, email).Scan(&staffID)
	if err != nil {
		t.Fatalf("seeding staff %s: %v", name, err)
	}
	return uuid.MustParse(staffID)
}

// SeedBooking inserts a confirmed booking, bypassing RLS as the table owner.
func SeedBooking(t *testing.T, owner *pgxpool.Pool, f Fixture, startsAt string) uuid.UUID {
	t.Helper()
	var id string
	err := owner.QueryRow(t.Context(), `
		INSERT INTO bookings (tenant_id, staff_id, service_id, customer_name, customer_email,
		                      starts_at, ends_at, status, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'Ada', 'ada@example.com',
		        $4::timestamptz, $4::timestamptz + interval '30 minutes', 'confirmed', $5)
		RETURNING id::text`,
		f.Tenant.String(), f.Staff.String(), f.Service.String(), startsAt, "seed-"+startsAt).Scan(&id)
	if err != nil {
		t.Fatalf("seeding booking: %v", err)
	}
	return uuid.MustParse(id)
}

// SeedUser inserts a tenant user with the given email, password hash and role
// ("owner" or "staff") and returns its id.
func SeedUser(t *testing.T, owner *pgxpool.Pool, f Fixture, email, passwordHash, role string) uuid.UUID {
	t.Helper()
	var id string
	err := owner.QueryRow(t.Context(), `
		INSERT INTO users (id, tenant_id, email, password_hash, role)
		VALUES (gen_random_uuid(), $1::uuid, $2, $3, $4)
		RETURNING id::text`,
		f.Tenant.String(), email, passwordHash, role).Scan(&id)
	if err != nil {
		t.Fatalf("seeding user %s: %v", email, err)
	}
	return uuid.MustParse(id)
}
