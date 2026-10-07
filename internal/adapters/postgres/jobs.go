package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ctru0009/slotwise/internal/adapters/postgres/dbgen"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

var _ app.JobStore = (*DB)(nil)

// claimJobSQL calls the SECURITY DEFINER claim function, which crosses tenants
// on purpose: the worker has no tenant of its own. sqlc cannot map a
// set-returning function's columns, so the call is hand-written exactly like
// the session resolver calls in session.go.
const claimJobSQL = `SELECT id, tenant_id, booking_id, kind, attempts FROM public.job_claim($1, $2, $3)`

// ClaimJob leases the next due job to workerID until claimTime plus
// leaseSeconds. It is one statement, never part of a longer transaction: a
// rolled-back claim would silently un-lease the row.
func (db *DB) ClaimJob(ctx context.Context, workerID string, claimTime time.Time, leaseSeconds int) (domain.Job, error) {
	var (
		job  domain.Job
		kind string
	)
	err := db.pool.QueryRow(ctx, claimJobSQL, workerID, claimTime, leaseSeconds).
		Scan(&job.ID, &job.TenantID, &job.BookingID, &kind, &job.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Job{}, fmt.Errorf("no due job: %w", domain.ErrNotFound)
	}
	if err != nil {
		return domain.Job{}, fmt.Errorf("claiming job: %w", err)
	}
	job.Kind = domain.JobKind(kind)
	return job, nil
}

// CompleteJob marks the job done.
func (db *DB) CompleteJob(ctx context.Context, workerID string, job domain.Job) (bool, error) {
	return db.jobTransition(ctx, job, "completing job", func(ctx context.Context, q *dbgen.Queries) (int64, error) {
		return q.CompleteJob(ctx, dbgen.CompleteJobParams{ID: job.ID, WorkerID: workerID})
	})
}

// RetryJob returns the job to the queue with runAt as its new due time.
func (db *DB) RetryJob(ctx context.Context, workerID string, job domain.Job, runAt time.Time, reason string) (bool, error) {
	return db.jobTransition(ctx, job, "retrying job", func(ctx context.Context, q *dbgen.Queries) (int64, error) {
		return q.RetryJob(ctx, dbgen.RetryJobParams{
			ID:       job.ID,
			WorkerID: workerID,
			RunAt:    runAt,
			Reason:   pgtype.Text{String: reason, Valid: true},
		})
	})
}

// DeadLetterJob marks the job dead, keeping reason as the record of what failed.
func (db *DB) DeadLetterJob(ctx context.Context, workerID string, job domain.Job, reason string) (bool, error) {
	return db.jobTransition(ctx, job, "dead-lettering job", func(ctx context.Context, q *dbgen.Queries) (int64, error) {
		return q.DeadLetterJob(ctx, dbgen.DeadLetterJobParams{
			ID:       job.ID,
			WorkerID: workerID,
			Reason:   pgtype.Text{String: reason, Valid: true},
		})
	})
}

// ReleaseJob hands an interrupted job back with its attempt refunded.
func (db *DB) ReleaseJob(ctx context.Context, workerID string, job domain.Job) (bool, error) {
	return db.jobTransition(ctx, job, "releasing job", func(ctx context.Context, q *dbgen.Queries) (int64, error) {
		return q.ReleaseJob(ctx, dbgen.ReleaseJobParams{ID: job.ID, WorkerID: workerID})
	})
}

// jobTransition runs one guarded job write inside the job's tenant and reports
// whether the worker still held the lease. A zero count means the lease moved
// on — another worker reclaimed an expired lease — which the caller logs
// instead of retrying.
func (db *DB) jobTransition(ctx context.Context, job domain.Job, name string, write func(ctx context.Context, q *dbgen.Queries) (int64, error)) (bool, error) {
	var applied bool
	err := db.WithTenant(ctx, job.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		affected, err := write(ctx, dbgen.New(tx))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		applied = affected > 0
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}
