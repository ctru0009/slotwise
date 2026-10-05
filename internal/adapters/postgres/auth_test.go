//go:build integration

package postgres_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/internal/domain"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// TestAuthResolverAndSessionStore covers the two pre-tenant paths. The app
// connection below never sets a tenant: resolving a slug and reading a session
// both happen before a tenant is known, so needing one would make these paths
// silently see nothing.
func TestAuthResolverAndSessionStore(t *testing.T) {
	t.Parallel()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	fixture := pgtest.Seed(t, owner, "a")
	pgtest.Seed(t, owner, "b")

	db := pgtest.AppDB(t, appDSN)
	assertResolver(t, db, fixture)
	assertSessionStore(t, db, owner)
}

func assertResolver(t *testing.T, db *postgres.DB, fixture pgtest.Fixture) {
	t.Helper()
	ctx := t.Context()

	tenant, err := db.TenantBySlug(ctx, "a")
	if err != nil {
		t.Fatalf("resolving tenant a: %v", err)
	}
	if tenant.ID != fixture.Tenant {
		t.Errorf("TenantBySlug returned id %s, want %s", tenant.ID, fixture.Tenant)
	}
	if tenant.Name != "Tenant a" || tenant.Timezone != "Europe/Berlin" {
		t.Errorf("TenantBySlug returned name %q timezone %q, want %q and %q",
			tenant.Name, tenant.Timezone, "Tenant a", "Europe/Berlin")
	}
	// The resolver returns only the three public columns, so the slug a caller
	// gets back can only be the one it asked for — the function itself never
	// returns a slug column (schema_test.go pins that surface).
	if tenant.Slug != "a" {
		t.Errorf("TenantBySlug returned slug %q, want the requested %q", tenant.Slug, "a")
	}
	if _, err := db.TenantBySlug(ctx, "does-not-exist"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("TenantBySlug(unknown slug) = %v, want domain.ErrNotFound", err)
	}
}

func assertSessionStore(t *testing.T, db *postgres.DB, owner *pgxpool.Pool) {
	t.Helper()

	store := postgres.NewSessionStore(t.Context(), db)
	assertSessionRoundTrip(t, store)
	assertSessionDelete(t, store)
	assertSessionPurge(t, db, store, owner)
}

func assertSessionRoundTrip(t *testing.T, store *postgres.SessionStore) {
	t.Helper()
	ctx := t.Context()

	live := time.Now().Add(time.Hour)
	if err := store.CommitCtx(ctx, "session-token-a", []byte(`{"tenant_id":"a"}`), live); err != nil {
		t.Fatalf("committing session a: %v", err)
	}
	if err := store.CommitCtx(ctx, "session-token-b", []byte(`{"tenant_id":"b"}`), live); err != nil {
		t.Fatalf("committing session b: %v", err)
	}
	assertSessionData(t, store, "session-token-a", []byte(`{"tenant_id":"a"}`))
	assertSessionData(t, store, "session-token-b", []byte(`{"tenant_id":"b"}`))

	if _, found, err := store.FindCtx(ctx, "never-committed"); err != nil || found {
		t.Errorf("FindCtx(unknown token) = (_, %v, %v), want (false, nil)", found, err)
	}
}

func assertSessionDelete(t *testing.T, store *postgres.SessionStore) {
	t.Helper()
	ctx := t.Context()

	if err := store.DeleteCtx(ctx, "session-token-a"); err != nil {
		t.Fatalf("deleting session a: %v", err)
	}
	if _, found, _ := store.FindCtx(ctx, "session-token-a"); found {
		t.Error("FindCtx found a session after DeleteCtx")
	}
	if err := store.DeleteCtx(ctx, "session-token-a"); err != nil {
		t.Errorf("DeleteCtx of a missing token = %v, want nil", err)
	}
	if _, found, _ := store.FindCtx(ctx, "session-token-b"); !found {
		t.Error("deleting one session removed another")
	}
}

// assertSessionPurge proves expiry is the database's decision: a row the owner
// seeded in the past is invisible to FindCtx even though the row exists, and
// PurgeSessions removes it without touching a live session.
func assertSessionPurge(t *testing.T, db *postgres.DB, store *postgres.SessionStore, owner *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()

	expired := "expired-token"
	seedSession(t, owner, expired, []byte("stale"), time.Now().Add(-time.Hour))
	if _, found, err := store.FindCtx(ctx, expired); err != nil || found {
		t.Errorf("FindCtx(expired token) = (_, %v, %v), want (false, nil)", found, err)
	}

	if err := db.PurgeSessions(ctx); err != nil {
		t.Fatalf("purging sessions: %v", err)
	}
	if _, found, _ := store.FindCtx(ctx, expired); found {
		t.Error("purge left an expired session discoverable")
	}
	if _, found, _ := store.FindCtx(ctx, "session-token-b"); !found {
		t.Error("purge removed a live session")
	}
	assertSessionGone(t, owner, expired)
}

func assertSessionData(t *testing.T, store *postgres.SessionStore, token string, want []byte) {
	t.Helper()

	got, found, err := store.FindCtx(t.Context(), token)
	if err != nil {
		t.Fatalf("finding session %s: %v", token, err)
	}
	if !found {
		t.Fatalf("FindCtx(%s) reported found=false, want true", token)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("FindCtx(%s) = %q, want %q", token, got, want)
	}
}

func seedSession(t *testing.T, owner *pgxpool.Pool, token string, data []byte, expiry time.Time) {
	t.Helper()

	_, err := owner.Exec(t.Context(), `
		INSERT INTO sessions (token_hash, data, expiry)
		VALUES (sha256(convert_to($1, 'UTF8')), $2, $3)`, token, data, expiry)
	if err != nil {
		t.Fatalf("seeding session %s: %v", token, err)
	}
}

func assertSessionGone(t *testing.T, owner *pgxpool.Pool, token string) {
	t.Helper()

	var rows int
	err := owner.QueryRow(t.Context(),
		"SELECT count(*) FROM sessions WHERE token_hash = sha256(convert_to($1, 'UTF8'))", token).
		Scan(&rows)
	if err != nil {
		t.Fatalf("counting session rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("purge left %d rows for %s, want 0", rows, token)
	}
}
