-- +goose Up

CREATE TABLE availability_rules (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    staff_id     uuid NOT NULL,
    weekday      smallint NOT NULL CHECK (weekday BETWEEN 0 AND 6),      -- 0 = Sunday
    start_minute integer  NOT NULL CHECK (start_minute BETWEEN 0 AND 1439),
    end_minute   integer  NOT NULL CHECK (end_minute BETWEEN 1 AND 1440),
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (end_minute > start_minute),
    -- Composite key for the same reason as 0002: referential checks bypass row
    -- level security, so tenant_id travels with every reference and one tenant
    -- cannot point a rule at another tenant's staff member.
    FOREIGN KEY (tenant_id, staff_id) REFERENCES staff (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX availability_rules_staff_idx ON availability_rules (tenant_id, staff_id);

-- Overlapping rules and overlapping time off mean union, not conflict, so
-- neither table carries an exclusion constraint.
CREATE TABLE time_off (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    staff_id   uuid NOT NULL,
    starts_at  timestamptz NOT NULL,
    ends_at    timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at),
    FOREIGN KEY (tenant_id, staff_id) REFERENCES staff (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX time_off_staff_idx ON time_off (tenant_id, staff_id, starts_at);

ALTER TABLE availability_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE availability_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY availability_rules_isolation ON availability_rules
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE time_off ENABLE ROW LEVEL SECURITY;
ALTER TABLE time_off FORCE ROW LEVEL SECURITY;
CREATE POLICY time_off_isolation ON time_off
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

-- The tables above are created after 0003's grant block ran, and 0001's guard
-- no-oped if the application role arrived later, so re-apply the envelope here
-- too: a green migration followed by "permission denied for table
-- availability_rules" at the first search is the failure this prevents.
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

-- The blanket grant above also reaches the tables earlier migrations sealed
-- off, exactly the way 0003's revoke of sessions would be undone by any later
-- grant on all tables. Re-apply those revokes here: sessions stays function-only
-- machinery and goose_db_version stays migration bookkeeping.
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

-- +goose Down
DROP TABLE IF EXISTS time_off;
DROP TABLE IF EXISTS availability_rules;
