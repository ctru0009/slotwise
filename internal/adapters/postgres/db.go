package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/domain"
)

// exclusionViolation is Postgres' SQLSTATE for an exclusion constraint breach.
const exclusionViolation = "23P01"

// DB is the Postgres-backed store. Queries run inside WithTenant so that row
// level security sees the request's tenant.
type DB struct {
	pool *pgxpool.Pool
}

// New opens a pool for dsn.
func New(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("opening pool: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Close releases the pool.
func (db *DB) Close() { db.pool.Close() }

// WithTenant runs fn in a transaction scoped to tenantID. The setting is
// transaction-local, so it cannot leak to another request sharing the
// connection.
func (db *DB) WithTenant(ctx context.Context, tenantID uuid.UUID, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID.String()); err != nil {
		return fmt.Errorf("setting tenant: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return mapError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapError(err)
	}
	return nil
}

// mapError turns Postgres failures into the domain errors callers match on.
func mapError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == exclusionViolation {
		return fmt.Errorf("%w: %s", domain.ErrSlotTaken, pgErr.Message)
	}
	return err
}
