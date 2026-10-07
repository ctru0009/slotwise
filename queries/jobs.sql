-- The enqueue rides the booking's own transaction: a booking that committed
-- always has its confirmation row, and a transaction that failed built neither.
-- The fresh-insert path is the only caller, so the no-op conflict clause is a
-- belt-and-braces guard on the unique (tenant, booking, kind) key.
-- name: InsertBookingJob :execrows
INSERT INTO jobs (tenant_id, booking_id, kind, run_at)
VALUES (@tenant_id, @booking_id, @kind, @run_at)
ON CONFLICT (tenant_id, booking_id, kind) DO NOTHING;

-- Every transition is guarded by the lease the caller still holds: a job whose
-- lease expired and was reclaimed by another worker matches nothing, and the
-- caller logs that instead of fighting over the row.

-- name: CompleteJob :execrows
UPDATE jobs
   SET status = 'done', locked_by = NULL, locked_until = NULL, updated_at = now()
 WHERE id = @id AND locked_by = @worker_id::text AND status = 'running';

-- name: RetryJob :execrows
UPDATE jobs
   SET status = 'ready', run_at = @run_at, last_error = @reason,
       locked_by = NULL, locked_until = NULL, updated_at = now()
 WHERE id = @id AND locked_by = @worker_id::text AND status = 'running';

-- name: DeadLetterJob :execrows
UPDATE jobs
   SET status = 'dead', last_error = @reason, locked_by = NULL, locked_until = NULL,
       updated_at = now()
 WHERE id = @id AND locked_by = @worker_id::text AND status = 'running';

-- The refund keeps a shutdown from spending a job's attempt budget: the claim
-- incremented attempts, the handler never finished, so the claim is undone. A
-- claim whose lease expired and moved on fails the guard and stays counted.
-- name: ReleaseJob :execrows
UPDATE jobs
   SET status = 'ready', locked_by = NULL, locked_until = NULL,
       attempts = greatest(attempts - 1, 0), updated_at = now()
 WHERE id = @id AND locked_by = @worker_id::text AND status = 'running';
