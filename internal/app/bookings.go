package app

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// Booking field bounds. The email and key limits are byte limits because both
// travel as one header or one form value.
const (
	maxEmailBytes          = 254
	maxIdempotencyKeyBytes = 255
)

// BookingWrite is one booking to store. EndsAt is the occupied end: StartsAt
// plus the service's duration and buffer.
type BookingWrite struct {
	StaffID        uuid.UUID
	ServiceID      uuid.UUID
	CustomerName   string
	CustomerEmail  string
	StartsAt       time.Time
	EndsAt         time.Time
	IdempotencyKey string
}

// Bookings implements the public booking use cases. No method takes an actor:
// the customer is not signed in, so the tenant comes from the URL and the
// cancel link carries its own proof.
type Bookings struct {
	store   BookingStore
	avail   AvailabilityStore
	tenants TenantStore
	clock   clock.Clock
	signer  *CancelSigner
}

// NewBookings returns a Bookings backed by store, validating against avail,
// resolving tenant timezones through tenants, reading the current instant from
// clk and signing cancel links with signer.
func NewBookings(store BookingStore, avail AvailabilityStore, tenants TenantStore, clk clock.Clock, signer *CancelSigner) *Bookings {
	return &Bookings{store: store, avail: avail, tenants: tenants, clock: clk, signer: signer}
}

// Create books the requested start. The start must be one the slot engine
// offers for that day: the grid, the weekly rules, time off, existing bookings
// and the current instant all have to allow it. A key that already booked
// returns the stored booking instead of inserting again.
func (b *Bookings) Create(ctx context.Context, tenantID uuid.UUID, in BookingInput) (domain.Booking, error) {
	if err := in.validate(); err != nil {
		return domain.Booking{}, err
	}

	// The replay check runs before any calendar work: a retry must not be
	// turned away by a schedule that changed after the booking it replays.
	existing, err := b.store.BookingByIdempotencyKey(ctx, tenantID, in.IdempotencyKey)
	switch {
	case err == nil:
		return existing, nil
	case errors.Is(err, domain.ErrNotFound):
	default:
		return domain.Booking{}, fmt.Errorf("reading booking by key: %w", err)
	}

	loc, err := tenantLocation(ctx, b.tenants, tenantID)
	if err != nil {
		return domain.Booking{}, err
	}
	date := localDateOf(in.StartsAt, loc)
	snapshot, err := b.avail.SlotSnapshot(ctx, tenantID, in.ServiceID,
		domain.DayStart(date, loc), domain.DayStart(date.Next(), loc))
	if err != nil {
		return domain.Booking{}, fmt.Errorf("reading slot snapshot: %w", err)
	}

	schedule, ok := scheduleFor(snapshot.Staff, in.StaffID)
	if !ok {
		return domain.Booking{}, fmt.Errorf("staff %s: %w", in.StaffID, domain.ErrNotFound)
	}
	query := slotQuery(loc, snapshot.Service, schedule, date, date, b.clock.Now())
	if !slices.ContainsFunc(domain.SlotTimes(query), func(start time.Time) bool { return start.Equal(in.StartsAt) }) {
		return domain.Booking{}, fmt.Errorf("slot %s: %w", in.StartsAt, domain.ErrSlotTaken)
	}

	booking, err := b.store.CreateBooking(ctx, tenantID, BookingWrite{
		StaffID:        in.StaffID,
		ServiceID:      in.ServiceID,
		CustomerName:   in.CustomerName,
		CustomerEmail:  in.CustomerEmail,
		StartsAt:       in.StartsAt,
		EndsAt:         in.StartsAt.Add(snapshot.Service.Block()),
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return domain.Booking{}, fmt.Errorf("creating booking: %w", err)
	}
	return booking, nil
}

// Get returns the booking a signed cancel link names. A token that does not
// authorise this booking is domain.ErrForbidden before the store is read, so a
// guessed id cannot probe which bookings exist.
func (b *Bookings) Get(ctx context.Context, tenantID, id uuid.UUID, token string) (domain.Booking, error) {
	if !b.signer.Verify(id, token) {
		return domain.Booking{}, domain.ErrForbidden
	}
	booking, err := b.store.BookingByID(ctx, tenantID, id)
	if err != nil {
		return domain.Booking{}, fmt.Errorf("reading booking: %w", err)
	}
	return booking, nil
}

// Cancel cancels the booking a signed cancel link names. Cancelling twice
// succeeds: the second call finds the booking already cancelled.
func (b *Bookings) Cancel(ctx context.Context, tenantID, id uuid.UUID, token string) error {
	if !b.signer.Verify(id, token) {
		return domain.ErrForbidden
	}
	if err := b.store.CancelBooking(ctx, tenantID, id); err != nil {
		return fmt.Errorf("cancelling booking: %w", err)
	}
	return nil
}

// CancelToken returns the token that authorises cancelling the booking with id.
func (b *Bookings) CancelToken(id uuid.UUID) string {
	return b.signer.Token(id)
}

// validate normalises in in place and reports the first booking field that is
// out of bounds, so the store only ever sees a trimmed name and a lower-cased
// email.
func (in *BookingInput) validate() error {
	in.CustomerName = strings.TrimSpace(in.CustomerName)
	in.CustomerEmail = strings.ToLower(strings.TrimSpace(in.CustomerEmail))
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	switch {
	case in.ServiceID == uuid.Nil:
		return domain.ValidationError{Field: "service_id", Message: "is required"}
	case in.StaffID == uuid.Nil:
		return domain.ValidationError{Field: "staff_id", Message: "is required"}
	case in.StartsAt.IsZero():
		return domain.ValidationError{Field: "starts_at", Message: "is required"}
	}
	switch {
	case in.CustomerName == "":
		return domain.ValidationError{Field: "customer_name", Message: "is required"}
	case utf8.RuneCountInString(in.CustomerName) > maxNameRunes:
		return domain.ValidationError{Field: "customer_name", Message: "must be at most 200 characters"}
	}
	switch {
	case in.CustomerEmail == "":
		return domain.ValidationError{Field: "customer_email", Message: "is required"}
	case len(in.CustomerEmail) > maxEmailBytes:
		return domain.ValidationError{Field: "customer_email", Message: "must be at most 254 bytes"}
	}
	if _, err := mail.ParseAddress(in.CustomerEmail); err != nil {
		return domain.ValidationError{Field: "customer_email", Message: "must be a valid email address"}
	}
	switch {
	case in.IdempotencyKey == "":
		return domain.ValidationError{Field: "idempotency_key", Message: "is required"}
	case len(in.IdempotencyKey) > maxIdempotencyKeyBytes:
		return domain.ValidationError{Field: "idempotency_key", Message: "must be at most 255 bytes"}
	}
	return nil
}

// localDateOf returns the tenant-local calendar date the instant falls on.
func localDateOf(t time.Time, loc *time.Location) domain.LocalDate {
	year, month, day := t.In(loc).Date()
	return domain.LocalDate{Year: year, Month: month, Day: day}
}

// scheduleFor finds one staff member's schedule in the snapshot.
func scheduleFor(schedules []domain.StaffSchedule, staffID uuid.UUID) (domain.StaffSchedule, bool) {
	for _, schedule := range schedules {
		if schedule.Staff.ID == staffID {
			return schedule, true
		}
	}
	return domain.StaffSchedule{}, false
}
