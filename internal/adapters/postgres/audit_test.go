//go:build integration

package postgres_test

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// TestAuditVocabularyMatchesTheDatabase pins the Go vocabulary to the column:
// domain.AuditActions must list exactly the string values the audit_log.action
// check constraint accepts, in the same order, so adding an action on one side
// without the other fails here instead of at the first write.
func TestAuditVocabularyMatchesTheDatabase(t *testing.T) {
	t.Parallel()
	_, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)

	accepted := auditActionValues(t, owner)
	want := make([]string, 0, len(domain.AuditActions))
	for _, action := range domain.AuditActions {
		want = append(want, string(action))
	}
	if !slices.Equal(accepted, want) {
		t.Errorf("audit_log.action accepts %v, domain.AuditActions lists %v: the Go vocabulary and the check constraint have drifted",
			accepted, want)
	}
}

// singleQuotedLiteral matches the contents of a SQL single-quoted literal. The
// action check renders as `action = ANY (ARRAY['a'::text, 'b'::text])`, so the
// quoted pieces are exactly the accepted values.
var singleQuotedLiteral = regexp.MustCompile(`'([^']*)'`)

// auditActionValues reads the audit_log.action check constraint's definition
// from pg_constraint and returns the string values it accepts, in the order it
// names them. A missing constraint, or more than one check that mentions
// action, fails the read; otherwise the parsed values are exactly what the
// vocabulary assertion compares.
func auditActionValues(t *testing.T, owner *pgxpool.Pool) []string {
	t.Helper()
	rows, err := owner.Query(t.Context(), `
		SELECT pg_get_constraintdef(oid)
		  FROM pg_constraint
		 WHERE conrelid = 'public.audit_log'::regclass
		   AND contype = 'c'
		   AND pg_get_constraintdef(oid) LIKE '%action%'`)
	if err != nil {
		t.Fatalf("reading the audit_log check constraints: %v", err)
	}
	defer rows.Close()

	var definitions []string
	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			t.Fatalf("scanning a check constraint: %v", err)
		}
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the audit_log check constraints: %v", err)
	}
	if len(definitions) != 1 {
		t.Fatalf("%d audit_log check constraints mention action, want exactly 1: %q", len(definitions), definitions)
	}

	values := []string{}
	for _, match := range singleQuotedLiteral.FindAllStringSubmatch(definitions[0], -1) {
		values = append(values, match[1])
	}
	return values
}

// TestAuditLogIsAppendOnly pins the trail's append-only shape: the application
// role can read and insert, and its UPDATE and DELETE are refused both by the
// missing privilege and by SQLSTATE, so a bug cannot rewrite or erase history.
func TestAuditLogIsAppendOnly(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, "append-only")
	booking := pgtest.SeedBooking(t, owner, fixture, "2026-11-02T09:00:00Z")
	db := pgtest.AppDB(t, appDSN)

	assertAuditPrivileges(t, owner)
	assertAuditWriteRefused(t, db, fixture.Tenant, `UPDATE audit_log SET action = 'staff.created'`, "UPDATE")
	assertAuditWriteRefused(t, db, fixture.Tenant, `DELETE FROM audit_log`, "DELETE")

	// A writer can still add a row: the trail is append-only, not read-only.
	insertErr := db.WithTenant(ctx, fixture.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO audit_log (tenant_id, action, booking_id)
			VALUES ($1::uuid, 'booking.created', $2::uuid)`, fixture.Tenant, booking)
		return err
	})
	if insertErr != nil {
		t.Fatalf("INSERT into audit_log as the app role: %v", insertErr)
	}
	readErr := db.WithTenant(ctx, fixture.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		var stored string
		if err := tx.QueryRow(ctx, `SELECT action FROM audit_log WHERE booking_id = $1::uuid`, booking).Scan(&stored); err != nil {
			return err
		}
		if stored != string(domain.AuditBookingCreated) {
			t.Errorf("stored action = %q, want %q", stored, domain.AuditBookingCreated)
		}
		return nil
	})
	if readErr != nil {
		t.Fatalf("SELECT from audit_log as the app role: %v", readErr)
	}

	// The trail cascades from its tenant, and PostgreSQL runs a referential
	// action as the table owner, so an app role holding DELETE on tenants could
	// erase a whole tenant's trail without ever touching audit_log.
	assertAuditWriteRefused(t, db, fixture.Tenant, `DELETE FROM tenants WHERE id = '`+fixture.Tenant.String()+`'`, "DELETE FROM tenants")
	var survived int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE tenant_id = $1::uuid`, fixture.Tenant).Scan(&survived); err != nil {
		t.Fatalf("counting the tenant's audit rows: %v", err)
	}
	if survived != 1 {
		t.Errorf("the tenant's audit rows after the refused delete = %d, want the one row written above", survived)
	}
}

// assertAuditPrivileges pins the table grants the append-only promise rests on.
func assertAuditPrivileges(t *testing.T, owner *pgxpool.Pool) {
	t.Helper()

	var canUpdate, canDelete, canSelect, canInsert bool
	err := owner.QueryRow(t.Context(), `
		SELECT has_table_privilege('slotwise_app', 'public.audit_log', 'UPDATE'),
		       has_table_privilege('slotwise_app', 'public.audit_log', 'DELETE'),
		       has_table_privilege('slotwise_app', 'public.audit_log', 'SELECT'),
		       has_table_privilege('slotwise_app', 'public.audit_log', 'INSERT')`).
		Scan(&canUpdate, &canDelete, &canSelect, &canInsert)
	if err != nil {
		t.Fatalf("reading the app role's audit_log privileges: %v", err)
	}
	if canUpdate || canDelete {
		t.Errorf("the app role holds UPDATE=%v DELETE=%v on audit_log, want neither: the trail is append-only", canUpdate, canDelete)
	}
	if !canSelect || !canInsert {
		t.Errorf("the app role holds SELECT=%v INSERT=%v on audit_log, want both", canSelect, canInsert)
	}
}

// assertAuditWriteRefused runs one statement as the app role inside the
// tenant's transaction and insists it is refused for want of privilege.
func assertAuditWriteRefused(t *testing.T, db *postgres.DB, tenant uuid.UUID, statement, name string) {
	t.Helper()

	err := db.WithTenant(t.Context(), tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, statement)
		return execErr
	})
	if !hasSQLState(err, "42501") {
		t.Errorf("%s as the app role returned %v, want SQLSTATE 42501", name, err)
	}
}

// TestAuditRowCommitsWithItsChange proves the audit row rides the change's own
// transaction: with every audit insert poisoned, the whole booking write must
// roll back leaving no booking, no job and no audit row; once the poison is
// gone, the same write commits all three.
func TestAuditRowCommitsWithItsChange(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "audit-atomic")
	dropPoison := poisonAuditInserts(t, f.owner)

	in := bookingInput(t, f.fixture, "key-audit-atomic")
	_, err := f.useCase.Create(ctx, f.fixture.Tenant, in)
	if err == nil {
		t.Fatal("Create succeeded while every audit insert raised")
	}
	if !strings.Contains(err.Error(), "poisoned audit insert") {
		t.Fatalf("Create failed with %v, want the poisoned audit insert", err)
	}
	if got := countBookings(t, f.owner, f.fixture.Tenant); got != 0 {
		t.Errorf("%d booking rows after the failed transaction, want 0", got)
	}
	if got := countJobs(t, f.owner, f.fixture.Tenant); got != 0 {
		t.Errorf("%d job rows after the failed transaction, want 0", got)
	}
	if got := countAuditRows(t, f.owner, f.fixture.Tenant); got != 0 {
		t.Errorf("%d audit rows after the failed transaction, want 0", got)
	}

	dropPoison()

	booking, err := f.useCase.Create(ctx, f.fixture.Tenant, in)
	if err != nil {
		t.Fatalf("Create after dropping the poison: %v", err)
	}
	rows := readAuditRows(t, f.owner, f.fixture.Tenant)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows after the successful write, want exactly 1", len(rows))
	}
	if rows[0].action != string(domain.AuditBookingCreated) {
		t.Errorf("audit action = %q, want %q", rows[0].action, domain.AuditBookingCreated)
	}
	if rows[0].booking == nil || *rows[0].booking != booking.ID {
		t.Errorf("audit subject = %v, want the booking %s", rows[0].booking, booking.ID)
	}
	if rows[0].actor != nil {
		t.Errorf("audit actor = %v, want NULL: the public booking flow has no login", *rows[0].actor)
	}
}

// TestAuditActorAndSubjectAreTenantConsistent proves the composite keys and the
// policy keep one tenant's audit rows away from another tenant's subjects and
// actors, while the public booking flow can record a change with no login; the
// tenant's own list never shows another tenant's row.
func TestAuditActorAndSubjectAreTenantConsistent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	tenantA := pgtest.Seed(t, owner, "a")
	tenantB := pgtest.Seed(t, owner, "b")
	bookingA := pgtest.SeedBooking(t, owner, tenantA, "2026-11-02T09:00:00Z")
	userB := pgtest.SeedUser(t, owner, tenantB, "b@example.com", "hash", "owner")
	db := pgtest.AppDB(t, appDSN)

	// Every case inserts under tenant A's context. The first two name a row
	// that tenant B owns, which only the tenant-consistent composite keys can
	// refuse; the third names tenant B outright, which the policy refuses; the
	// standalone insert below is the public booking flow's shape and must
	// commit.
	//
	// The refusing inserts carry no RETURNING: with it, Postgres also checks
	// the returned row against the SELECT policy, and the third case would pass
	// for a reason this test is not about.
	cases := []struct {
		name  string
		query string
		args  []any
		want  string
	}{
		{
			name: "subject from another tenant",
			query: `INSERT INTO audit_log (tenant_id, action, service_id)
			        VALUES ($1::uuid, 'service.created', $2::uuid)`,
			args: []any{tenantA.Tenant, tenantB.Service},
			want: "23503",
		},
		{
			name: "actor from another tenant",
			query: `INSERT INTO audit_log (tenant_id, actor_user_id, action, service_id)
			        VALUES ($1::uuid, $2::uuid, 'service.created', $3::uuid)`,
			args: []any{tenantA.Tenant, userB, tenantA.Service},
			want: "23503",
		},
		{
			name: "row under another tenant",
			query: `INSERT INTO audit_log (tenant_id, action, service_id)
			        VALUES ($1::uuid, 'service.created', $2::uuid)`,
			args: []any{tenantB.Tenant, tenantB.Service},
			want: "42501",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := execAudit(ctx, db, tenantA.Tenant, tc.query, tc.args...)
			if !hasSQLState(err, tc.want) {
				t.Errorf("insert returned %v, want SQLSTATE %s", err, tc.want)
			}
		})
	}

	aRowID, err := insertAudit(ctx, t, db, tenantA.Tenant,
		`INSERT INTO audit_log (tenant_id, action, booking_id)
		 VALUES ($1::uuid, 'booking.created', $2::uuid) RETURNING id`,
		tenantA.Tenant, bookingA)
	if err != nil {
		t.Fatalf("inserting a booking audit row without an actor: %v", err)
	}

	var bRowID uuid.UUID
	err = owner.QueryRow(ctx, `
		INSERT INTO audit_log (tenant_id, action, service_id)
		VALUES ($1::uuid, 'service.created', $2::uuid) RETURNING id`,
		tenantB.Tenant, tenantB.Service).Scan(&bRowID)
	if err != nil {
		t.Fatalf("seeding tenant B's audit row: %v", err)
	}

	entries, err := db.ListAuditLog(ctx, tenantA.Tenant, 50)
	if err != nil {
		t.Fatalf("ListAuditLog: %v", err)
	}
	ids := make([]uuid.UUID, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	if !slices.Equal(ids, []uuid.UUID{aRowID}) {
		t.Errorf("tenant A's audit list = %v, want only %s; tenant B's row %s must never appear", ids, aRowID, bRowID)
	}
}

// TestBookingCreatedIsRecordedOncePerBooking pins the audit write to the
// fresh-insert path: a replay that resolves the key through the store's own
// conflict branch must return the stored booking without recording the change
// again, or queueing its mail again.
func TestBookingCreatedIsRecordedOncePerBooking(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newBookingFixture(t, "audit-once")
	in := bookingInput(t, f.fixture, "key-audit-once")

	first, err := f.useCase.Create(ctx, f.fixture.Tenant, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The replay re-runs the store write with the same key, so it reaches the
	// ON CONFLICT branch rather than the use case's early key check, and
	// returns the stored row with nothing new recorded.
	stored, err := f.db.BookingByIdempotencyKey(ctx, f.fixture.Tenant, in.IdempotencyKey)
	if err != nil {
		t.Fatalf("reading the stored booking: %v", err)
	}
	replay, err := f.db.CreateBooking(ctx, f.fixture.Tenant, app.BookingWrite{
		StaffID:        stored.StaffID,
		ServiceID:      stored.ServiceID,
		CustomerName:   stored.CustomerName,
		CustomerEmail:  stored.CustomerEmail,
		StartsAt:       stored.StartsAt,
		EndsAt:         stored.EndsAt,
		IdempotencyKey: in.IdempotencyKey,
		EnqueuedAt:     f.clock.Now(),
	})
	if err != nil {
		t.Fatalf("replayed CreateBooking: %v", err)
	}
	if replay.ID != first.ID {
		t.Fatalf("the replay returned %s, want the stored booking %s", replay.ID, first.ID)
	}

	rows := readAuditRows(t, f.owner, f.fixture.Tenant)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows after the replay, want exactly 1: the key-conflict path must not record the change again", len(rows))
	}
	if rows[0].action != string(domain.AuditBookingCreated) {
		t.Errorf("audit action = %q, want %q", rows[0].action, domain.AuditBookingCreated)
	}
	if rows[0].booking == nil || *rows[0].booking != first.ID {
		t.Errorf("audit subject = %v, want the booking %s", rows[0].booking, first.ID)
	}
	if got := countJobs(t, f.owner, f.fixture.Tenant); got != 2 {
		t.Errorf("%d jobs after the replay, want the two the fresh insert queued", got)
	}
}

// TestCancelledBookingIsAudited pins the cancel audit row: the store's cancel
// writes one booking.cancelled entry with no actor, because the cancel link is
// the public flow's proof rather than a login.
func TestCancelledBookingIsAudited(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, "cancel-audit")
	booking := pgtest.SeedBooking(t, owner, fixture, "2026-11-02T09:00:00Z")
	db := pgtest.AppDB(t, appDSN)

	if err := db.CancelBooking(ctx, fixture.Tenant, booking); err != nil {
		t.Fatalf("CancelBooking: %v", err)
	}

	rows := readAuditRows(t, owner, fixture.Tenant)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows after the cancel, want exactly 1", len(rows))
	}
	row := rows[0]
	if row.action != string(domain.AuditBookingCancelled) {
		t.Errorf("audit action = %q, want %q", row.action, domain.AuditBookingCancelled)
	}
	if row.booking == nil || *row.booking != booking {
		t.Errorf("audit subject = %v, want the booking %s", row.booking, booking)
	}
	if row.actor != nil {
		t.Errorf("audit actor = %v, want NULL: the cancel link carries the caller's proof, not a login", *row.actor)
	}

	var status string
	if err := owner.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1::uuid`, booking).Scan(&status); err != nil {
		t.Fatalf("reading the cancelled booking: %v", err)
	}
	if status != string(domain.BookingCancelled) {
		t.Errorf("booking status = %q, want %q", status, domain.BookingCancelled)
	}
}

// auditRow is one audit_log row as the tests assert it, read as the owner so
// row level security does not hide what the app role wrote.
type auditRow struct {
	id        uuid.UUID
	action    string
	actor     *uuid.UUID
	booking   *uuid.UUID
	service   *uuid.UUID
	staff     *uuid.UUID
	createdAt time.Time
}

// readAuditRows returns the tenant's audit rows in insertion order.
func readAuditRows(t *testing.T, owner *pgxpool.Pool, tenantID uuid.UUID) []auditRow {
	t.Helper()
	rows, err := owner.Query(t.Context(), `
		SELECT id, action, actor_user_id, booking_id, service_id, staff_id, created_at
		  FROM audit_log
		 WHERE tenant_id = $1::uuid
		 ORDER BY created_at, id`, tenantID)
	if err != nil {
		t.Fatalf("reading audit rows: %v", err)
	}
	defer rows.Close()

	var out []auditRow
	for rows.Next() {
		var row auditRow
		if err := rows.Scan(&row.id, &row.action, &row.actor, &row.booking, &row.service, &row.staff, &row.createdAt); err != nil {
			t.Fatalf("scanning an audit row: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading audit rows: %v", err)
	}
	return out
}

// countAuditRows counts a tenant's audit rows as the owner pool.
func countAuditRows(t *testing.T, owner *pgxpool.Pool, tenantID uuid.UUID) int {
	t.Helper()
	return len(readAuditRows(t, owner, tenantID))
}

// insertAudit runs one audit insert as the app role under tenantID and returns
// the new row's id when it commits. The INSERT must end in RETURNING id.
func insertAudit(ctx context.Context, t *testing.T, db *postgres.DB, tenantID uuid.UUID, query string, args ...any) (uuid.UUID, error) {
	t.Helper()
	var id uuid.UUID
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, args...).Scan(&id)
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// execAudit runs one audit INSERT as the app role under tenantID, with no
// RETURNING: a returned row would also face the SELECT policy, which is not
// what the row-shape assertions are about.
func execAudit(ctx context.Context, db *postgres.DB, tenantID uuid.UUID, query string, args ...any) error {
	return db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, query, args...)
		return err
	})
}

// poisonAuditInserts makes every insert into audit_log raise until the returned
// function is called, which is how the atomicity test proves the audit row
// shares the change's transaction.
func poisonAuditInserts(t *testing.T, owner *pgxpool.Pool) (drop func()) {
	t.Helper()
	_, err := owner.Exec(t.Context(), `
		CREATE FUNCTION test_poison_audit() RETURNS trigger LANGUAGE plpgsql
		AS $$ BEGIN RAISE EXCEPTION 'poisoned audit insert'; END $$;
		CREATE TRIGGER test_poison_audit BEFORE INSERT ON audit_log
		    FOR EACH ROW EXECUTE FUNCTION test_poison_audit();`)
	if err != nil {
		t.Fatalf("installing the audit poison trigger: %v", err)
	}
	drop = func() {
		_, err := owner.Exec(context.WithoutCancel(t.Context()), `
			DROP TRIGGER IF EXISTS test_poison_audit ON audit_log;
			DROP FUNCTION IF EXISTS test_poison_audit();`)
		if err != nil {
			t.Errorf("removing the audit poison trigger: %v", err)
		}
	}
	t.Cleanup(drop)
	return drop
}
