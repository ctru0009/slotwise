-- +goose Up

-- Who changed what, when. A row names exactly one subject through a composite
-- key, so referential checks run as the table owner and cannot be used to point
-- one tenant's audit row at another tenant's row. actor_user_id is NULL for the
-- public booking flow: the customer has no account, so there is no login to
-- name. The check constraint list mirrors domain.AuditActions and
-- TestAuditVocabularyMatchesTheDatabase fails if the two drift apart.
CREATE TABLE audit_log (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    actor_user_id uuid,
    action        text NOT NULL CHECK (action IN (
                      'booking.created', 'booking.cancelled',
                      'service.created', 'service.updated',
                      'service.activated', 'service.deactivated',
                      'staff.created', 'staff.updated',
                      'staff.activated', 'staff.deactivated')),
    booking_id    uuid,
    service_id    uuid,
    staff_id      uuid,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (num_nonnulls(booking_id, service_id, staff_id) = 1),
    FOREIGN KEY (tenant_id, actor_user_id) REFERENCES users (tenant_id, id),
    FOREIGN KEY (tenant_id, booking_id) REFERENCES bookings (tenant_id, id),
    FOREIGN KEY (tenant_id, service_id) REFERENCES services (tenant_id, id),
    FOREIGN KEY (tenant_id, staff_id) REFERENCES staff (tenant_id, id)
);

-- The dashboard reads the tenant's newest rows and stops at fifty, so the index
-- matches that order exactly instead of sorting a growing table.
CREATE INDEX audit_log_tenant_created_idx ON audit_log (tenant_id, created_at DESC, id DESC);

ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_log_isolation ON audit_log
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

-- The tables above are created after 0006's grant block ran, so re-apply the
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

-- Append-only is a privilege, not a convention: the envelope above hands the
-- application role UPDATE and DELETE on every table, so this one gives them
-- back. A correction to the trail is a new row, never an edit of an old one.
REVOKE UPDATE, DELETE ON TABLE audit_log FROM slotwise_app;

-- The same promise needs the tenants revoke: audit rows cascade from their
-- tenant, and PostgreSQL runs that referential action as the table owner, so
-- without this the application role could still erase a whole trail by deleting
-- its own tenant row. Deleting a tenant is an operator action, never the
-- application's.
REVOKE DELETE ON TABLE tenants FROM slotwise_app;


-- +goose Down
DROP TABLE IF EXISTS audit_log;
