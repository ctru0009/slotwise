package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ctru0009/slotwise/internal/adapters/postgres/dbgen"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

var _ app.BookingStore = (*DB)(nil)

// CreateBooking stores one booking, or returns the booking its key already
// created. The staff row is locked before the insert, so concurrent bookings
// for one staff member queue behind it and the exclusion constraint picks the
// winner instead of the transactions deadlocking on its index.
func (db *DB) CreateBooking(ctx context.Context, tenantID uuid.UUID, in app.BookingWrite) (domain.Booking, error) {
	var booking domain.Booking
	err := db.WithTenantRetry(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		stored, err := insertBooking(ctx, dbgen.New(tx), tenantID, in)
		if err != nil {
			return err
		}
		booking = stored
		return nil
	})
	if err != nil {
		// The exclusion violation and the key's own booking are not mutually
		// exclusive: a request that lost the slot can still replay a booking
		// whose response the client never saw, so the key decides before the
		// error is reported.
		stored, readErr := db.BookingByIdempotencyKey(ctx, tenantID, in.IdempotencyKey)
		if readErr == nil {
			return stored, nil
		}
		return domain.Booking{}, err
	}
	return booking, nil
}

// insertBooking is one attempt of the write, safe to replay under
// WithTenantRetry: a repeated key returns its stored row instead of inserting
// again.
func insertBooking(ctx context.Context, q *dbgen.Queries, tenantID uuid.UUID, in app.BookingWrite) (domain.Booking, error) {
	if err := lockActiveStaffRow(ctx, q, in.StaffID); err != nil {
		return domain.Booking{}, err
	}
	row, err := q.InsertBooking(ctx, dbgen.InsertBookingParams{
		TenantID:       tenantID,
		StaffID:        in.StaffID,
		ServiceID:      in.ServiceID,
		CustomerName:   in.CustomerName,
		CustomerEmail:  in.CustomerEmail,
		StartsAt:       in.StartsAt,
		EndsAt:         in.EndsAt,
		IdempotencyKey: in.IdempotencyKey,
	})
	switch {
	case err == nil:
		return toDomainBooking(row), nil
	case errors.Is(err, pgx.ErrNoRows):
		existing, readErr := bookingByKey(ctx, q, tenantID, in.IdempotencyKey)
		if errors.Is(readErr, domain.ErrNotFound) {
			// A booking is never deleted, so a key that conflicted and now has
			// no row is a broken invariant, not a miss the caller can act on.
			return domain.Booking{}, fmt.Errorf("booking with key %q has no row after a key conflict", in.IdempotencyKey)
		}
		return existing, readErr
	default:
		return domain.Booking{}, fmt.Errorf("inserting booking: %w", err)
	}
}

// bookingByKey reads the row a key already created in the caller's transaction,
// or domain.ErrNotFound.
func bookingByKey(ctx context.Context, q *dbgen.Queries, tenantID uuid.UUID, key string) (domain.Booking, error) {
	row, err := q.BookingByIdempotencyKey(ctx, dbgen.BookingByIdempotencyKeyParams{
		TenantID:       tenantID,
		IdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Booking{}, fmt.Errorf("booking with key %q: %w", key, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Booking{}, fmt.Errorf("reading booking by key: %w", err)
	}
	return toDomainBooking(row), nil
}

// BookingByIdempotencyKey returns the booking a key already created, or
// domain.ErrNotFound. The key is tenant-scoped, so the same key in another
// tenant is a different booking.
func (db *DB) BookingByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (domain.Booking, error) {
	var booking domain.Booking
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		stored, err := bookingByKey(ctx, dbgen.New(tx), tenantID, key)
		if err != nil {
			return err
		}
		booking = stored
		return nil
	})
	if err != nil {
		return domain.Booking{}, err
	}
	return booking, nil
}

// BookingByID returns the booking with id, or domain.ErrNotFound. Another
// tenant's booking is invisible, so it reads as a miss.
func (db *DB) BookingByID(ctx context.Context, tenantID, id uuid.UUID) (domain.Booking, error) {
	var booking domain.Booking
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).BookingByID(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("booking %s: %w", id, domain.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading booking: %w", err)
		}
		booking = toDomainBooking(row)
		return nil
	})
	if err != nil {
		return domain.Booking{}, err
	}
	return booking, nil
}

// CancelBooking marks the booking cancelled. The update is status-blind, so
// cancelling twice succeeds and only an unknown or invisible id reports
// domain.ErrNotFound.
func (db *DB) CancelBooking(ctx context.Context, tenantID, id uuid.UUID) error {
	return db.WithTenantRetry(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		affected, err := dbgen.New(tx).CancelBooking(ctx, id)
		if err != nil {
			return fmt.Errorf("cancelling booking: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("booking %s: %w", id, domain.ErrNotFound)
		}
		return nil
	})
}

// lockActiveStaffRow takes the active staff member's row lock inside the
// tenant's transaction, serializing concurrent bookings for the same staff
// member. An unknown, invisible or deactivated staff member reports
// domain.ErrNotFound.
func lockActiveStaffRow(ctx context.Context, q *dbgen.Queries, staffID uuid.UUID) error {
	_, err := q.LockActiveStaff(ctx, staffID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("staff %s: %w", staffID, domain.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("locking staff %s: %w", staffID, err)
	}
	return nil
}

// toDomainBooking maps one stored row.
func toDomainBooking(row dbgen.Booking) domain.Booking {
	return domain.Booking{
		ID:            row.ID,
		TenantID:      row.TenantID,
		StaffID:       row.StaffID,
		ServiceID:     row.ServiceID,
		CustomerName:  row.CustomerName,
		CustomerEmail: row.CustomerEmail,
		StartsAt:      row.StartsAt,
		EndsAt:        row.EndsAt,
		Status:        domain.BookingStatus(row.Status),
		CreatedAt:     row.CreatedAt,
	}
}
