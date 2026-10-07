-- +goose Up

-- The queue's jobs reference their booking the way 0002 requires: a referential
-- check runs as the table owner and ignores row level security, so tenant_id
-- travels with the reference and one tenant cannot point a job at another
-- tenant's booking.
ALTER TABLE bookings ADD CONSTRAINT bookings_tenant_id_id_key UNIQUE (tenant_id, id);

-- One row per unit of mail a booking owes. attempts counts claims, so the
-- claim that hands a job to a worker has already incremented it; scheduling
-- fields (run_at, locked_until) are written from the application's injected
-- clock and compared against a claim_time parameter, never against the
-- database's now(), so one authority decides when a job is due. created_at and
-- updated_at stay database-stamped audit columns.
--
-- The unique key is the enqueue's idempotence: a confirmed booking has at most
-- one confirmation and at most one reminder row, whatever a replay does. That
-- also means a dead job is requeued with an UPDATE, never with a second INSERT.
CREATE TABLE jobs (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    booking_id   uuid NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('booking_confirmation', 'booking_reminder')),
    status       text NOT NULL DEFAULT 'ready' CHECK (status IN ('ready', 'running', 'done', 'dead')),
    attempts     integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    run_at       timestamptz NOT NULL,
    locked_by    text,
    locked_until timestamptz,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, booking_id, kind),
    FOREIGN KEY (tenant_id, booking_id) REFERENCES bookings (tenant_id, id) ON DELETE CASCADE,
    -- A lease exists exactly while a worker holds the job.
    CHECK (status <> 'running' OR (locked_by IS NOT NULL AND locked_until IS NOT NULL)),
    CHECK (status = 'running' OR (locked_by IS NULL AND locked_until IS NULL))
);

-- The claim reads ready rows by run_at and expired leases by locked_until, so
-- each partial index matches one branch's predicate exactly and the claim
-- stays an index scan however long the backlog grows.
CREATE INDEX jobs_ready_idx ON jobs (run_at, created_at, id) WHERE status = 'ready';
CREATE INDEX jobs_running_idx ON jobs (locked_until, id) WHERE status = 'running';

ALTER TABLE jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY jobs_isolation ON jobs
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

-- The tables above are created after 0005's grant block ran, so re-apply the
-- envelope: assert the application role exists, grant it, and keep the default
-- privileges for later tables. Then re-apply the seals earlier migrations
-- established, because the blanket grant would otherwise undo them.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'slotwise_app') THEN
        RAISE EXCEPTION 'role slotwise_app must exist before migrating: create it first, or the application will have no privileges';
    END IF;

    GRANT USAGE ON SCHEMA public TO slotwise_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO slotwise_app;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public
        GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO slotwise_app;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.sessions') IS NOT NULL THEN
        REVOKE ALL ON TABLE sessions FROM slotwise_app;
    END IF;
    IF to_regclass('public.goose_db_version') IS NOT NULL THEN
        REVOKE ALL ON TABLE goose_db_version FROM slotwise_app;
    END IF;
END
$$;
-- +goose StatementEnd

-- job_claim runs as its owner to see the whole queue, so the migration role
-- must bypass row level security the way 0003 demanded for the session
-- functions; a definer that sees nothing would silently stop delivery instead
-- of failing here.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles
                    WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
        RAISE EXCEPTION 'migrations must run as a role that bypasses row level security: the SECURITY DEFINER functions below cannot see the queue otherwise';
    END IF;
END
$$;
-- +goose StatementEnd

-- Claims the next due job for any tenant: the worker has no tenant of its own,
-- so a tenant-scoped policy would hide every tenant's queue from it. This is
-- the one cross-tenant operation the application role can perform, and it is
-- bounded to the queue: the booking the job names is still read later under
-- WithTenant(job.tenant_id), and no other table gains a path around its policy.
--
-- Ready jobs are preferred over expired leases. Each branch takes its row lock
-- before the update (FOR UPDATE SKIP LOCKED) and its predicate matches one
-- partial index; the outer UPDATE repeats the predicate so a row whose claim
-- committed between the branch and the update matches nothing and the caller
-- simply polls again. attempts increments here, at claim time, so an attempt
-- that dies unreported is still counted.
-- +goose StatementBegin
CREATE FUNCTION public.job_claim(worker_id text, claim_time timestamptz, lease_seconds integer)
RETURNS TABLE(id uuid, tenant_id uuid, booking_id uuid, kind text, attempts integer)
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = ''
AS $$
    WITH ready AS (
        SELECT j.id
          FROM public.jobs j
         WHERE j.status = 'ready' AND j.run_at <= $2
         ORDER BY j.run_at, j.created_at, j.id
         FOR UPDATE SKIP LOCKED
         LIMIT 1
    ), expired AS (
        SELECT j.id
          FROM public.jobs j
         WHERE j.status = 'running' AND j.locked_until <= $2
         ORDER BY j.locked_until, j.id
         FOR UPDATE SKIP LOCKED
         LIMIT 1
    ), picked AS (
        SELECT id, 0 AS priority FROM ready
        UNION ALL
        SELECT id, 1 FROM expired
        ORDER BY priority
        LIMIT 1
    )
    UPDATE public.jobs j
       SET status = 'running',
           locked_by = $1,
           locked_until = $2 + pg_catalog.make_interval(secs => $3),
           attempts = j.attempts + 1,
           updated_at = $2
      FROM picked p
     WHERE j.id = p.id
       AND ((j.status = 'ready' AND j.run_at <= $2)
            OR (j.status = 'running' AND j.locked_until <= $2))
    RETURNING j.id, j.tenant_id, j.booking_id, j.kind, j.attempts
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.job_claim(text, timestamptz, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.job_claim(text, timestamptz, integer) TO slotwise_app;

-- +goose Down
DROP FUNCTION IF EXISTS public.job_claim(text, timestamptz, integer);
DROP TABLE IF EXISTS jobs;
ALTER TABLE bookings DROP CONSTRAINT IF EXISTS bookings_tenant_id_id_key;
