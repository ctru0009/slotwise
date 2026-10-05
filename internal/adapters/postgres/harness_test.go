//go:build integration

package postgres_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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

// startPostgres runs a throwaway Postgres, provisions the non-owner app role and
// applies the committed migrations. It returns the DSN the application uses (a
// role without BYPASSRLS, so policies apply) and an owner DSN for fixtures.
func startPostgres(t *testing.T) (appDSN, ownerDSN string) {
	t.Helper()
	ctx := t.Context()

	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("slotwise"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
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

	owner := newOwnerPool(t, ownerDSN)
	_, err = owner.Exec(ctx,
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOBYPASSRLS", appRole, appPassword))
	if err != nil {
		t.Fatalf("creating %s: %v", appRole, err)
	}

	migrate(t, ownerDSN)
	return appDSN, ownerDSN
}

// migrate applies every migration with goose, the same way a deploy would. The
// Provider API is used instead of goose's package-level setters: those are
// globals, and parallel tests would race on them.
func migrate(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("opening migration connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatalf("building goose provider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}
}

func newOwnerPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting as owner: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newAppDB(t *testing.T, dsn string) *postgres.DB {
	t.Helper()
	db, err := postgres.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("opening app connection: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// fixture is one tenant with a staff member and a service.
type fixture struct {
	tenant  uuid.UUID
	staff   uuid.UUID
	service uuid.UUID
}

func seed(t *testing.T, owner *pgxpool.Pool, slug string) fixture {
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
	return fixture{tenant: uuid.MustParse(tenantID), staff: uuid.MustParse(staffID), service: uuid.MustParse(serviceID)}
}

// seedBooking inserts a confirmed booking, bypassing RLS as the table owner.
func seedBooking(t *testing.T, owner *pgxpool.Pool, f fixture, startsAt string) uuid.UUID {
	t.Helper()
	var id string
	err := owner.QueryRow(t.Context(), `
		INSERT INTO bookings (tenant_id, staff_id, service_id, customer_name, customer_email,
		                      starts_at, ends_at, status, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'Ada', 'ada@example.com',
		        $4::timestamptz, $4::timestamptz + interval '30 minutes', 'confirmed', $5)
		RETURNING id::text`,
		f.tenant.String(), f.staff.String(), f.service.String(), startsAt, "seed-"+startsAt).Scan(&id)
	if err != nil {
		t.Fatalf("seeding booking: %v", err)
	}
	return uuid.MustParse(id)
}
