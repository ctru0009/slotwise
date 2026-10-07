package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
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

// attemptParam narrows a job's attempt count for the generated queries. The
// worker's attempt budget is a small configured number, far inside int32.
func attemptParam(attempts int) int32 {
	return int32(attempts) //nolint:gosec // attempts is bounded by the worker's MaxAttempts
}

// CompleteJob marks the job done.
func (db *DB) CompleteJob(ctx context.Context, workerID string, job domain.Job) (bool, error) {
	return db.jobTransition(ctx, job, "completing job", func(ctx context.Context, q *dbgen.Queries) (int64, error) {
		return q.CompleteJob(ctx, dbgen.CompleteJobParams{
			ID:       job.ID,
			WorkerID: workerID,
			Attempts: attemptParam(job.Attempts),
		})
	})
}

// RetryJob returns the job to the queue with runAt as its new due time.
func (db *DB) RetryJob(ctx context.Context, workerID string, job domain.Job, runAt time.Time, reason string) (bool, error) {
	return db.jobTransition(ctx, job, "retrying job", func(ctx context.Context, q *dbgen.Queries) (int64, error) {
		return q.RetryJob(ctx, dbgen.RetryJobParams{
			ID:       job.ID,
			WorkerID: workerID,
			Attempts: attemptParam(job.Attempts),
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
			Attempts: attemptParam(job.Attempts),
			Reason:   pgtype.Text{String: reason, Valid: true},
		})
	})
}

// ListDeadJobs returns the tenant's dead-lettered jobs, newest first.
func (db *DB) ListDeadJobs(ctx context.Context, tenantID uuid.UUID, limit int) ([]app.DeadJob, error) {
	dead := []app.DeadJob{}
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := dbgen.New(tx).ListDeadJobs(ctx, narrowInt32(limit))
		if err != nil {
			return fmt.Errorf("listing dead jobs: %w", err)
		}
		for _, row := range rows {
			job := app.DeadJob{
				ID:       row.ID,
				Kind:     domain.JobKind(row.Kind),
				Attempts: int(row.Attempts),
				DiedAt:   row.UpdatedAt,
			}
			if row.LastError.Valid {
				job.LastError = row.LastError.String
			}
			dead = append(dead, job)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dead, nil
}

// ReleaseJob hands an interrupted job back with its attempt refunded.
func (db *DB) ReleaseJob(ctx context.Context, workerID string, job domain.Job) (bool, error) {
	return db.jobTransition(ctx, job, "releasing job", func(ctx context.Context, q *dbgen.Queries) (int64, error) {
		return q.ReleaseJob(ctx, dbgen.ReleaseJobParams{
			ID:       job.ID,
			WorkerID: workerID,
			Attempts: attemptParam(job.Attempts),
		})
	})
}

// jobTransition runs one guarded job write inside the job's tenant and reports
// whether the caller still held the claim it was given. A zero count means the
// claim moved on — another worker reclaimed an expired lease, which also bumped
// the attempt count — and the caller logs it instead of retrying.
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
