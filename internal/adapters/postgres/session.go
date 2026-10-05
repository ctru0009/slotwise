package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5"
)

// SessionStore is the scs session store. Sessions are pre-tenant: a lookup
// happens before any tenant is known, so every method talks to the shared pool
// and goes through the session_* SECURITY DEFINER functions. The sessions table
// itself is not reachable as the app role — 0003 revokes every privilege on it
// — so the store must not fall back to raw SQL.
type SessionStore struct {
	ctx context.Context
	db  *DB
}

// NewSessionStore returns a store that runs the non-context scs.Store methods
// on ctx. cmd/web passes the process context built from its signal handler, so
// a shutdown cancels in-flight session work; context.Background is banned in
// this package and would hide that.
func NewSessionStore(ctx context.Context, db *DB) *SessionStore {
	return &SessionStore{ctx: ctx, db: db}
}

// FindCtx returns the data for token. An unknown or expired token reports
// found=false with a nil error: session_find filters on the database clock, so
// the application can never resurrect a session the database considers dead,
// and a malformed token is simply not found.
func (s *SessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	var (
		data   []byte
		expiry time.Time
	)
	err := s.db.pool.QueryRow(ctx,
		"SELECT data, expiry FROM public.session_find($1)", token).
		Scan(&data, &expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("finding session: %w", err)
	}
	return data, true, nil
}

// CommitCtx stores token's data and expiry through session_commit, replacing an
// existing session for the same token.
func (s *SessionStore) CommitCtx(ctx context.Context, token string, data []byte, expiry time.Time) error {
	if _, err := s.db.pool.Exec(ctx,
		"SELECT public.session_commit($1, $2, $3)", token, data, expiry); err != nil {
		return fmt.Errorf("committing session: %w", err)
	}
	return nil
}

// DeleteCtx removes token's session through session_delete. Deleting a token
// that is not there is a no-op, which is the contract scs expects.
func (s *SessionStore) DeleteCtx(ctx context.Context, token string) error {
	if _, err := s.db.pool.Exec(ctx, "SELECT public.session_delete($1)", token); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

// Find implements scs.Store. scs calls FindCtx whenever the store implements
// CtxStore, so this method exists only to satisfy the embedded interface and
// uses the context captured by NewSessionStore.
func (s *SessionStore) Find(token string) ([]byte, bool, error) {
	return s.FindCtx(s.ctx, token)
}

// Commit implements scs.Store and delegates to CommitCtx for the same reason as
// Find.
func (s *SessionStore) Commit(token string, data []byte, expiry time.Time) error {
	return s.CommitCtx(s.ctx, token, data, expiry)
}

// Delete implements scs.Store and delegates to DeleteCtx for the same reason as
// Find.
func (s *SessionStore) Delete(token string) error {
	return s.DeleteCtx(s.ctx, token)
}

// PurgeSessions drops every expired session through session_purge. cmd/web
// calls it once at startup; the table is otherwise reachable only through the
// session functions.
func (db *DB) PurgeSessions(ctx context.Context) error {
	if _, err := db.pool.Exec(ctx, "SELECT public.session_purge()"); err != nil {
		return fmt.Errorf("purging sessions: %w", err)
	}
	return nil
}

var _ scs.CtxStore = (*SessionStore)(nil)
