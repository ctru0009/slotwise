-- +goose Up

-- Bookings reference services, so removing one would either fail or destroy
-- booking history; deactivating hides it from new bookings and from the slot
-- search that M3 adds.
ALTER TABLE services ADD COLUMN active boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE services DROP COLUMN active;
