//go:build integration

package postgres_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// TestAdapterCRUD drives the tenant-scoped store methods of two seeded tenants.
// Every tenant-scoped call runs inside WithTenant, so no query carries its own
// tenant predicate and the cross-tenant paths below are decided by row level
// security.
func TestAdapterCRUD(t *testing.T) {
	t.Parallel()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	tenantA := pgtest.Seed(t, owner, "a")
	tenantB := pgtest.Seed(t, owner, "b")
	db := pgtest.AppDB(t, appDSN)

	assertServiceStore(t, db, owner, tenantA, tenantB)
	assertStaffStore(t, db, owner, tenantA, tenantB)
	assertUserStore(t, db, tenantA, tenantB)
	assertResetTokenStore(t, db, owner, tenantA)
	assertTenantInsert(t, db)
}

// assertTenantInsert covers the provisioning path: the insert runs inside
// WithTenant keyed on the new tenant's own id, and a slug that already exists
// is reported as not inserted instead of as an error.
func assertTenantInsert(t *testing.T, db *postgres.DB) {
	t.Helper()
	ctx := t.Context()

	tenant := domain.Tenant{ID: uuid.New(), Slug: "inserted", Name: "Inserted", Timezone: "Europe/Berlin"}
	inserted, err := db.InsertTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("inserting tenant: %v", err)
	}
	if !inserted {
		t.Fatal("InsertTenant reported false for a new slug, want true")
	}
	found, err := db.TenantBySlug(ctx, "inserted")
	if err != nil {
		t.Fatalf("resolving the inserted tenant: %v", err)
	}
	if found.ID != tenant.ID || found.Name != "Inserted" || found.Timezone != "Europe/Berlin" {
		t.Errorf("TenantBySlug = %+v, want the inserted tenant", found)
	}

	duplicate := tenant
	duplicate.ID = uuid.New()
	inserted, err = db.InsertTenant(ctx, duplicate)
	if err != nil {
		t.Fatalf("inserting an existing slug: %v", err)
	}
	if inserted {
		t.Error("InsertTenant reported true for a slug that already exists, want false")
	}
}

func assertServiceStore(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenantA, tenantB pgtest.Fixture) {
	t.Helper()
	created := createService(t, db, tenantA)
	updateService(t, db, tenantA, created)
	deactivateService(t, db, owner, tenantA, created)
	assertServiceMisses(t, db, owner, tenantA, tenantB)
}

func createService(t *testing.T, db *postgres.DB, tenant pgtest.Fixture) domain.Service {
	t.Helper()

	in := app.ServiceInput{Name: "Massage", DurationMinutes: 60, BufferMinutes: 15, PriceCents: 9900}
	if err := db.CreateService(t.Context(), tenant.Tenant, in); err != nil {
		t.Fatalf("creating service: %v", err)
	}
	created := findService(t, db, tenant.Tenant, "Massage")
	if created.TenantID != tenant.Tenant {
		t.Errorf("created service tenant = %s, want %s", created.TenantID, tenant.Tenant)
	}
	if created.DurationMinutes != 60 || created.BufferMinutes != 15 || created.PriceCents != 9900 {
		t.Errorf("created service = %+v, want duration 60, buffer 15 and price 9900", created)
	}
	if !created.Active {
		t.Error("a newly created service is inactive, want active")
	}
	return created
}

func updateService(t *testing.T, db *postgres.DB, tenant pgtest.Fixture, created domain.Service) {
	t.Helper()

	update := app.ServiceInput{Name: "Deep Tissue", DurationMinutes: 90, PriceCents: 12000}
	if err := db.UpdateService(t.Context(), tenant.Tenant, created.ID, update); err != nil {
		t.Fatalf("updating service: %v", err)
	}
	updated := findService(t, db, tenant.Tenant, "Deep Tissue")
	if updated.ID != created.ID {
		t.Errorf("updated service id = %s, want %s", updated.ID, created.ID)
	}
	if updated.DurationMinutes != 90 || updated.BufferMinutes != 0 || updated.PriceCents != 12000 {
		t.Errorf("updated service = %+v, want duration 90, buffer 0 and price 12000", updated)
	}
}

func deactivateService(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenant pgtest.Fixture, created domain.Service) {
	t.Helper()

	if err := db.SetServiceActive(t.Context(), tenant.Tenant, created.ID, false); err != nil {
		t.Fatalf("deactivating service: %v", err)
	}
	if deactivated := findService(t, db, tenant.Tenant, "Deep Tissue"); deactivated.Active {
		t.Error("SetServiceActive(false) left the service active")
	}
	assertServiceRow(t, owner, tenant.Tenant, created.ID, "Deep Tissue", false)
}

func assertServiceMisses(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenantA, tenantB pgtest.Fixture) {
	t.Helper()

	for _, id := range []uuid.UUID{tenantB.Service, uuid.New()} {
		if err := db.UpdateService(t.Context(), tenantA.Tenant, id, app.ServiceInput{Name: "Hijack", DurationMinutes: 5, PriceCents: 1}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("UpdateService(%s) = %v, want domain.ErrNotFound", id, err)
		}
		if err := db.SetServiceActive(t.Context(), tenantA.Tenant, id, false); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("SetServiceActive(%s) = %v, want domain.ErrNotFound", id, err)
		}
	}
	assertServiceRow(t, owner, tenantB.Tenant, tenantB.Service, "Cut b", true)
	assertServiceListIsScoped(t, db, tenantA, tenantB)
}

func assertServiceListIsScoped(t *testing.T, db *postgres.DB, tenantA, tenantB pgtest.Fixture) {
	t.Helper()

	for _, service := range listServices(t, db, tenantA.Tenant) {
		if service.ID == tenantB.Service {
			t.Errorf("tenant B's service %s appears in tenant A's list", service.ID)
		}
	}
	empty, err := db.ListServices(t.Context(), uuid.New())
	if err != nil {
		t.Fatalf("listing services of an unknown tenant: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("ListServices(unknown tenant) = %#v, want a non-nil empty slice", empty)
	}
}

func assertStaffStore(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenantA, tenantB pgtest.Fixture) {
	t.Helper()
	created := createStaff(t, db, tenantA)
	updateStaff(t, db, tenantA, created)
	deactivateStaff(t, db, owner, tenantA, created)
	assertStaffMisses(t, db, owner, tenantA, tenantB)
}

func createStaff(t *testing.T, db *postgres.DB, tenant pgtest.Fixture) domain.Staff {
	t.Helper()

	in := app.StaffInput{Name: "Ada", Email: "ada@example.com"}
	if err := db.CreateStaff(t.Context(), tenant.Tenant, in); err != nil {
		t.Fatalf("creating staff: %v", err)
	}
	created := findStaff(t, db, tenant.Tenant, "Ada")
	if created.TenantID != tenant.Tenant || created.Email != "ada@example.com" {
		t.Errorf("created staff = %+v, want tenant %s and email ada@example.com", created, tenant.Tenant)
	}
	if !created.Active {
		t.Error("a newly created staff member is inactive, want active")
	}
	return created
}

func updateStaff(t *testing.T, db *postgres.DB, tenant pgtest.Fixture, created domain.Staff) {
	t.Helper()

	update := app.StaffInput{Name: "Ada Lovelace", Email: "ada.lovelace@example.com"}
	if err := db.UpdateStaff(t.Context(), tenant.Tenant, created.ID, update); err != nil {
		t.Fatalf("updating staff: %v", err)
	}
	updated := findStaff(t, db, tenant.Tenant, "Ada Lovelace")
	if updated.ID != created.ID || updated.Email != "ada.lovelace@example.com" {
		t.Errorf("updated staff = %+v, want id %s and email ada.lovelace@example.com", updated, created.ID)
	}
}

func deactivateStaff(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenant pgtest.Fixture, created domain.Staff) {
	t.Helper()

	if err := db.SetStaffActive(t.Context(), tenant.Tenant, created.ID, false); err != nil {
		t.Fatalf("deactivating staff: %v", err)
	}
	if deactivated := findStaff(t, db, tenant.Tenant, "Ada Lovelace"); deactivated.Active {
		t.Error("SetStaffActive(false) left the staff member active")
	}
	assertStaffRow(t, owner, tenant.Tenant, created.ID, "Ada Lovelace", false)
}

func assertStaffMisses(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenantA, tenantB pgtest.Fixture) {
	t.Helper()

	for _, id := range []uuid.UUID{tenantB.Staff, uuid.New()} {
		if err := db.UpdateStaff(t.Context(), tenantA.Tenant, id, app.StaffInput{Name: "Hijack", Email: "hijack@example.com"}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("UpdateStaff(%s) = %v, want domain.ErrNotFound", id, err)
		}
		if err := db.SetStaffActive(t.Context(), tenantA.Tenant, id, false); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("SetStaffActive(%s) = %v, want domain.ErrNotFound", id, err)
		}
	}
	assertStaffRow(t, owner, tenantB.Tenant, tenantB.Staff, "Staff b", true)

	for _, member := range listStaff(t, db, tenantA.Tenant) {
		if member.ID == tenantB.Staff {
			t.Errorf("tenant B's staff %s appears in tenant A's list", member.ID)
		}
	}
	empty, err := db.ListStaff(t.Context(), uuid.New())
	if err != nil {
		t.Fatalf("listing staff of an unknown tenant: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("ListStaff(unknown tenant) = %#v, want a non-nil empty slice", empty)
	}
}

func assertUserStore(t *testing.T, db *postgres.DB, tenantA, tenantB pgtest.Fixture) {
	t.Helper()
	user := insertUser(t, db, tenantA)
	assertUserReads(t, db, tenantA, user)
	assertUserMisses(t, db, tenantA, tenantB)
}

func insertUser(t *testing.T, db *postgres.DB, tenant pgtest.Fixture) domain.User {
	t.Helper()
	ctx := t.Context()

	user := domain.User{
		ID:           uuid.New(),
		TenantID:     tenant.Tenant,
		Email:        "Owner@Example.com",
		PasswordHash: "hash-one",
		Role:         domain.RoleOwner,
	}
	created, err := db.InsertUser(ctx, user)
	if err != nil {
		t.Fatalf("inserting user: %v", err)
	}
	if !created {
		t.Fatal("InsertUser reported false for a new email, want true")
	}

	duplicate := user
	duplicate.ID = uuid.New()
	duplicate.Email = "owner@example.com"
	created, err = db.InsertUser(ctx, duplicate)
	if err != nil {
		t.Fatalf("inserting a duplicate email: %v", err)
	}
	if created {
		t.Error("InsertUser reported true for an email that already exists in the tenant, want false")
	}
	return user
}

func assertUserReads(t *testing.T, db *postgres.DB, tenant pgtest.Fixture, user domain.User) {
	t.Helper()
	ctx := t.Context()

	byEmail, err := db.UserByEmail(ctx, tenant.Tenant, "OWNER@EXAMPLE.COM")
	if err != nil {
		t.Fatalf("reading user by email: %v", err)
	}
	if byEmail.ID != user.ID || byEmail.TenantID != tenant.Tenant || byEmail.Role != domain.RoleOwner {
		t.Errorf("UserByEmail = %+v, want the inserted row", byEmail)
	}
	if byEmail.Email != "Owner@Example.com" || byEmail.PasswordHash != "hash-one" || byEmail.StaffID != nil {
		t.Errorf("UserByEmail = %+v, want the inserted email and hash with a nil staff id", byEmail)
	}

	byID, err := db.UserByID(ctx, tenant.Tenant, user.ID)
	if err != nil {
		t.Fatalf("reading user by id: %v", err)
	}
	if byID.ID != user.ID || byID.Email != "Owner@Example.com" {
		t.Errorf("UserByID = %+v, want the inserted row", byID)
	}
}

func assertUserMisses(t *testing.T, db *postgres.DB, tenantA, tenantB pgtest.Fixture) {
	t.Helper()
	ctx := t.Context()

	if _, err := db.UserByEmail(ctx, tenantA.Tenant, "nobody@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("UserByEmail(unknown) = %v, want domain.ErrNotFound", err)
	}
	if _, err := db.UserByID(ctx, tenantA.Tenant, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("UserByID(unknown) = %v, want domain.ErrNotFound", err)
	}
	// The email is unique per tenant, so the same address in another tenant is
	// simply not there.
	if _, err := db.UserByEmail(ctx, tenantB.Tenant, "owner@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("UserByEmail(other tenant) = %v, want domain.ErrNotFound", err)
	}
}

func assertResetTokenStore(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenant pgtest.Fixture) {
	t.Helper()
	userID := pgtest.SeedUser(t, owner, tenant, "reset@example.com", "old-hash", "owner")
	second := issueTwoResetTokens(t, db, owner, tenant, userID)
	assertConsumedResetToken(t, db, tenant, userID, second)
	assertExpiredResetToken(t, db, owner, tenant, userID)
}

func issueTwoResetTokens(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenant pgtest.Fixture, userID uuid.UUID) []byte {
	t.Helper()
	ctx := t.Context()

	live := time.Now().Add(time.Hour)
	if err := db.IssueResetToken(ctx, tenant.Tenant, userID, tokenHash("token-one"), live); err != nil {
		t.Fatalf("issuing first reset token: %v", err)
	}
	second := tokenHash("token-two")
	if err := db.IssueResetToken(ctx, tenant.Tenant, userID, second, live); err != nil {
		t.Fatalf("issuing second reset token: %v", err)
	}
	assertOpenResetToken(t, owner, userID, second)
	return second
}

func assertConsumedResetToken(t *testing.T, db *postgres.DB, tenant pgtest.Fixture, userID uuid.UUID, token []byte) {
	t.Helper()
	ctx := t.Context()

	consumed, err := db.ConsumeResetToken(ctx, tenant.Tenant, token, "new-hash")
	if err != nil {
		t.Fatalf("consuming reset token: %v", err)
	}
	if consumed != userID {
		t.Errorf("ConsumeResetToken returned user %s, want %s", consumed, userID)
	}
	if _, err := db.ConsumeResetToken(ctx, tenant.Tenant, token, "attacker-hash"); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("replaying a consumed token = %v, want domain.ErrResetTokenInvalid", err)
	}
	assertUserPassword(t, db, tenant, userID, "new-hash")
}

func assertExpiredResetToken(t *testing.T, db *postgres.DB, owner *pgxpool.Pool, tenant pgtest.Fixture, userID uuid.UUID) {
	t.Helper()
	ctx := t.Context()

	// No store method creates an expired token, so the owner seeds the row.
	expired := tokenHash("token-expired")
	_, err := owner.Exec(ctx, `
		INSERT INTO password_reset_tokens (token_hash, tenant_id, user_id, expires_at)
		VALUES ($1, $2, $3, now() - interval '1 hour')`,
		expired, tenant.Tenant, userID)
	if err != nil {
		t.Fatalf("seeding an expired reset token: %v", err)
	}
	if _, err := db.ConsumeResetToken(ctx, tenant.Tenant, expired, "attacker-hash"); !errors.Is(err, domain.ErrResetTokenInvalid) {
		t.Errorf("consuming an expired token = %v, want domain.ErrResetTokenInvalid", err)
	}
	assertUserPassword(t, db, tenant, userID, "new-hash")
}

func listServices(t *testing.T, db *postgres.DB, tenantID uuid.UUID) []domain.Service {
	t.Helper()
	services, err := db.ListServices(t.Context(), tenantID)
	if err != nil {
		t.Fatalf("listing services: %v", err)
	}
	return services
}

func findService(t *testing.T, db *postgres.DB, tenantID uuid.UUID, name string) domain.Service {
	t.Helper()
	for _, service := range listServices(t, db, tenantID) {
		if service.Name == name {
			return service
		}
	}
	t.Fatalf("service %q is not in the tenant's list", name)
	return domain.Service{}
}

func listStaff(t *testing.T, db *postgres.DB, tenantID uuid.UUID) []domain.Staff {
	t.Helper()
	staff, err := db.ListStaff(t.Context(), tenantID)
	if err != nil {
		t.Fatalf("listing staff: %v", err)
	}
	return staff
}

func findStaff(t *testing.T, db *postgres.DB, tenantID uuid.UUID, name string) domain.Staff {
	t.Helper()
	for _, member := range listStaff(t, db, tenantID) {
		if member.Name == name {
			return member
		}
	}
	t.Fatalf("staff %q is not in the tenant's list", name)
	return domain.Staff{}
}

func assertServiceRow(t *testing.T, owner *pgxpool.Pool, tenantID, id uuid.UUID, name string, active bool) {
	t.Helper()

	var (
		gotTenant uuid.UUID
		gotName   string
		gotActive bool
	)
	err := owner.QueryRow(t.Context(),
		"SELECT tenant_id, name, active FROM services WHERE id = $1", id).
		Scan(&gotTenant, &gotName, &gotActive)
	if err != nil {
		t.Fatalf("reading service %s as the owner: %v", id, err)
	}
	if gotTenant != tenantID || gotName != name || gotActive != active {
		t.Errorf("service %s = (tenant %s, name %q, active %v), want (%s, %q, %v)",
			id, gotTenant, gotName, gotActive, tenantID, name, active)
	}
}

func assertStaffRow(t *testing.T, owner *pgxpool.Pool, tenantID, id uuid.UUID, name string, active bool) {
	t.Helper()

	var (
		gotTenant uuid.UUID
		gotName   string
		gotActive bool
	)
	err := owner.QueryRow(t.Context(),
		"SELECT tenant_id, name, active FROM staff WHERE id = $1", id).
		Scan(&gotTenant, &gotName, &gotActive)
	if err != nil {
		t.Fatalf("reading staff %s as the owner: %v", id, err)
	}
	if gotTenant != tenantID || gotName != name || gotActive != active {
		t.Errorf("staff %s = (tenant %s, name %q, active %v), want (%s, %q, %v)",
			id, gotTenant, gotName, gotActive, tenantID, name, active)
	}
}

func assertOpenResetToken(t *testing.T, owner *pgxpool.Pool, userID uuid.UUID, want []byte) {
	t.Helper()

	rows, err := owner.Query(t.Context(),
		"SELECT token_hash FROM password_reset_tokens WHERE user_id = $1 AND used_at IS NULL", userID)
	if err != nil {
		t.Fatalf("reading open reset tokens: %v", err)
	}
	defer rows.Close()

	var hashes [][]byte
	for rows.Next() {
		var hash []byte
		if err := rows.Scan(&hash); err != nil {
			t.Fatalf("scanning open reset token: %v", err)
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading open reset tokens: %v", err)
	}
	if len(hashes) != 1 {
		t.Fatalf("%d open reset tokens for %s, want exactly 1", len(hashes), userID)
	}
	if !bytes.Equal(hashes[0], want) {
		t.Errorf("open reset token hash = %x, want %x", hashes[0], want)
	}
}

func assertUserPassword(t *testing.T, db *postgres.DB, tenant pgtest.Fixture, userID uuid.UUID, want string) {
	t.Helper()

	user, err := db.UserByID(t.Context(), tenant.Tenant, userID)
	if err != nil {
		t.Fatalf("reading user %s: %v", userID, err)
	}
	if user.PasswordHash != want {
		t.Errorf("user %s password hash = %q, want %q", userID, user.PasswordHash, want)
	}
}

func tokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
