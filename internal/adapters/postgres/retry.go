package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Postgres aborts transactions that lose a lock cycle or a serialization race.
// Concurrent inserts for one staff member deadlock on the exclusion constraint's
// GiST index, so a booking path that does not retry reports those to the caller
// instead of "slot taken".
const (
	deadlockDetected        = "40P01"
	serializationFailure    = "40001"
	withTenantRetryAttempts = 8
	retryBackoff            = 10 * time.Millisecond
)

// WithTenantRetry runs fn like WithTenant and retries the whole transaction when
// Postgres aborts it with a deadlock or serialization failure. This is what makes
// "one slot, one winner" hold under contention: without it, concurrent inserts
// can all fail. fn must be safe to run more than once.
func (db *DB) WithTenantRetry(ctx context.Context, tenantID uuid.UUID, fn func(ctx context.Context, tx pgx.Tx) error) error {
	var err error
	for attempt := range withTenantRetryAttempts {
		if err = db.WithTenant(ctx, tenantID, fn); !isTransient(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting to retry: %w", ctx.Err())
		case <-time.After(time.Duration(attempt+1) * retryBackoff):
		}
	}
	return fmt.Errorf("after %d attempts: %w", withTenantRetryAttempts, err)
}

// isTransient reports whether err is worth retrying the transaction for.
func isTransient(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == deadlockDetected || pgErr.Code == serializationFailure
}
