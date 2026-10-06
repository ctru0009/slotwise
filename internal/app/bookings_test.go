package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// fakeBookingStore is an in-memory BookingStore that records what the use case
// asked it to do.
type fakeBookingStore struct {
	byKey map[string]domain.Booking
	byID  map[uuid.UUID]domain.Booking

	lookups   int
	creates   int
	reads     int
	cancels   int
	writes    []BookingWrite
	lookupErr error
	createErr error
	readErr   error
	cancelErr error
}

func (f *fakeBookingStore) BookingByIdempotencyKey(_ context.Context, tenantID uuid.UUID, key string) (domain.Booking, error) {
	f.lookups++
	if f.lookupErr != nil {
		return domain.Booking{}, f.lookupErr
	}
	booking, ok := f.byKey[key]
	if !ok || booking.TenantID != tenantID {
		return domain.Booking{}, domain.ErrNotFound
	}
	return booking, nil
}

func (f *fakeBookingStore) CreateBooking(_ context.Context, tenantID uuid.UUID, in BookingWrite) (domain.Booking, error) {
	f.creates++
	f.writes = append(f.writes, in)
	if f.createErr != nil {
		return domain.Booking{}, f.createErr
	}
	booking := domain.Booking{
		ID:            uuid.New(),
		TenantID:      tenantID,
		StaffID:       in.StaffID,
		ServiceID:     in.ServiceID,
		CustomerName:  in.CustomerName,
		CustomerEmail: in.CustomerEmail,
		StartsAt:      in.StartsAt,
		EndsAt:        in.EndsAt,
		Status:        domain.BookingConfirmed,
		CreatedAt:     in.StartsAt,
	}
	f.store(booking, in.IdempotencyKey)
	return booking, nil
}

func (f *fakeBookingStore) BookingByID(_ context.Context, tenantID, id uuid.UUID) (domain.Booking, error) {
	f.reads++
	if f.readErr != nil {
		return domain.Booking{}, f.readErr
	}
	booking, ok := f.byID[id]
	if !ok || booking.TenantID != tenantID {
		return domain.Booking{}, domain.ErrNotFound
	}
	return booking, nil
}

func (f *fakeBookingStore) CancelBooking(_ context.Context, tenantID, id uuid.UUID) error {
	f.cancels++
	if f.cancelErr != nil {
		return f.cancelErr
	}
	booking, ok := f.byID[id]
	if !ok || booking.TenantID != tenantID {
		return domain.ErrNotFound
	}
	booking.Status = domain.BookingCancelled
	f.store(booking, "")
	return nil
}

// store records one booking under its id and, when key is not empty, its
// idempotency key.
func (f *fakeBookingStore) store(booking domain.Booking, key string) {
	if f.byID == nil {
		f.byID = make(map[uuid.UUID]domain.Booking)
	}
	f.byID[booking.ID] = booking
	if key == "" {
		return
	}
	if f.byKey == nil {
		f.byKey = make(map[string]domain.Booking)
	}
	f.byKey[key] = booking
}

// bookingFixture is one Berlin tenant wired to the booking use case and the
// fakes. The clock is parked before the fixture date so NotBefore filters
// nothing, the staff member works Monday 09:00–12:00, and the service blocks
// 40 minutes: 30 of appointment and 10 of buffer.
type bookingFixture struct {
	useCase  *Bookings
	bookings *fakeBookingStore
	avail    *fakeAvailabilityStore
	tenants  *fakeTenantStore
	clock    *clock.Fake
	signer   *CancelSigner
	tenantID uuid.UUID
	staffID  uuid.UUID
	service  domain.Service
}

// bookingSecret is long enough for NewCancelSigner; the signer tests pin the
// boundary itself.
const bookingSecret = "test-cancel-secret-that-is-long-enough"

func newBookingFixture(t *testing.T) bookingFixture {
	t.Helper()
	tenantID, staffID := uuid.New(), uuid.New()
	service := domain.Service{
		ID:              uuid.New(),
		TenantID:        tenantID,
		Name:            "Cut",
		DurationMinutes: 30,
		BufferMinutes:   10,
		Active:          true,
	}
	bookings := &fakeBookingStore{}
	avail := &fakeAvailabilityStore{snapshot: domain.SlotSnapshot{
		Service: service,
		Staff: []domain.StaffSchedule{{
			Staff: domain.Staff{ID: staffID, TenantID: tenantID, Name: "Ada", Email: "ada@example.com", Active: true},
			Rules: []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 9 * 60, EndMinute: 12 * 60}},
		}},
	}}
	tenants := &fakeTenantStore{bySlug: map[string]domain.Tenant{
		"acme": {ID: tenantID, Slug: "acme", Name: "Acme", Timezone: "Europe/Berlin"},
	}}
	fakeClock := clock.NewFake(time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC))
	signer, err := NewCancelSigner(bookingSecret)
	if err != nil {
		t.Fatalf("building the signer: %v", err)
	}
	return bookingFixture{
		useCase:  NewBookings(bookings, avail, tenants, fakeClock, signer),
		bookings: bookings,
		avail:    avail,
		tenants:  tenants,
		clock:    fakeClock,
		signer:   signer,
		tenantID: tenantID,
		staffID:  staffID,
		service:  service,
	}
}

// request is a booking for the fixture's staff and service at the given Berlin
// wall time on Monday 2026-11-02.
func (f bookingFixture) request(t *testing.T, hour, minute int, key string) BookingInput {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("loading Berlin: %v", err)
	}
	return BookingInput{
		ServiceID:      f.service.ID,
		StaffID:        f.staffID,
		StartsAt:       time.Date(2026, time.November, 2, hour, minute, 0, 0, loc),
		CustomerName:   "Ada Lovelace",
		CustomerEmail:  "ada@example.com",
		IdempotencyKey: key,
	}
}

func TestCreateBooksTheOfferedSlot(t *testing.T) {
	t.Parallel()
	f := newBookingFixture(t)

	booking, err := f.useCase.Create(t.Context(), f.tenantID, f.request(t, 9, 0, "key-1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(f.bookings.writes) != 1 {
		t.Fatalf("the store saw %d writes, want 1", len(f.bookings.writes))
	}
	write := f.bookings.writes[0]
	if want := f.request(t, 9, 0, "key-1").StartsAt; !write.StartsAt.Equal(want) {
		t.Errorf("stored starts_at = %v, want %v", write.StartsAt, want)
	}
	// The occupied end is duration plus buffer, which is what the exclusion
	// constraint then enforces.
	if want := write.StartsAt.Add(40 * time.Minute); !write.EndsAt.Equal(want) {
		t.Errorf("stored ends_at = %v, want %v (duration plus buffer)", write.EndsAt, want)
	}
	if !booking.EndsAt.Equal(write.EndsAt) {
		t.Errorf("returned ends_at = %v, want the stored %v", booking.EndsAt, write.EndsAt)
	}
	if booking.Status != domain.BookingConfirmed {
		t.Errorf("status = %q, want %q", booking.Status, domain.BookingConfirmed)
	}
}

// TestCreateReplaySkipsTheCalendar pins the ordering the replay rule needs: a
// key that already booked returns its booking even when the calendar could no
// longer offer the slot.
func TestCreateReplaySkipsTheCalendar(t *testing.T) {
	t.Parallel()
	f := newBookingFixture(t)
	stored := domain.Booking{
		ID:        uuid.New(),
		TenantID:  f.tenantID,
		StaffID:   f.staffID,
		ServiceID: f.service.ID,
		StartsAt:  f.request(t, 9, 0, "key-1").StartsAt,
		EndsAt:    f.request(t, 9, 0, "key-1").StartsAt.Add(40 * time.Minute),
		Status:    domain.BookingConfirmed,
	}
	f.bookings.store(stored, "key-1")
	f.avail.snapshotErr = errors.New("the calendar must not be read on a replay")
	f.clock.Set(time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC))

	booking, err := f.useCase.Create(t.Context(), f.tenantID, f.request(t, 9, 0, "key-1"))
	if err != nil {
		t.Fatalf("Create on a replayed key: %v", err)
	}
	if booking.ID != stored.ID {
		t.Errorf("replay returned booking %s, want the stored %s", booking.ID, stored.ID)
	}
	if f.avail.snapshotCalls != 0 {
		t.Errorf("the replay read the snapshot %d times, want 0", f.avail.snapshotCalls)
	}
	if f.bookings.creates != 0 {
		t.Errorf("the replay inserted %d times, want 0", f.bookings.creates)
	}
}

func TestCreateRejectsUnbookableStarts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		start [2]int
		tweak func(*bookingFixture)
	}{
		{name: "off the grid", start: [2]int{9, 7}},
		{name: "already busy", start: [2]int{9, 0}, tweak: func(f *bookingFixture) {
			f.avail.snapshot.Staff[0].Busy = []domain.Interval{{
				Start: time.Date(2026, time.November, 2, 8, 0, 0, 0, time.UTC),
				End:   time.Date(2026, time.November, 2, 8, 40, 0, 0, time.UTC),
			}}
		}},
		{name: "time off", start: [2]int{9, 0}, tweak: func(f *bookingFixture) {
			f.avail.snapshot.Staff[0].TimeOff = []domain.Interval{{
				Start: time.Date(2026, time.November, 2, 0, 0, 0, 0, time.UTC),
				End:   time.Date(2026, time.November, 3, 0, 0, 0, 0, time.UTC),
			}}
		}},
		{name: "in the past", start: [2]int{9, 0}, tweak: func(f *bookingFixture) {
			f.clock.Set(time.Date(2026, time.November, 2, 9, 30, 0, 0, time.UTC))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newBookingFixture(t)
			if tt.tweak != nil {
				tt.tweak(&f)
			}
			_, err := f.useCase.Create(t.Context(), f.tenantID, f.request(t, tt.start[0], tt.start[1], "key-1"))
			if !errors.Is(err, domain.ErrSlotTaken) {
				t.Fatalf("Create = %v, want domain.ErrSlotTaken", err)
			}
			if f.bookings.creates != 0 {
				t.Errorf("an unbookable start was stored %d times, want 0", f.bookings.creates)
			}
		})
	}
}

// TestCreateSnapshotCoversTheStartsLocalDay pins the read the validation needs:
// one tenant-local day, because a start near local midnight is the previous day
// in UTC.
func TestCreateSnapshotCoversTheStartsLocalDay(t *testing.T) {
	t.Parallel()
	f := newBookingFixture(t)

	if _, err := f.useCase.Create(t.Context(), f.tenantID, f.request(t, 9, 0, "key-1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantFrom := time.Date(2026, time.November, 1, 23, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, time.November, 2, 23, 0, 0, 0, time.UTC)
	if !f.avail.snapshotFrom.Equal(wantFrom) || !f.avail.snapshotTo.Equal(wantTo) {
		t.Errorf("snapshot range = [%v, %v), want [%v, %v)", f.avail.snapshotFrom, f.avail.snapshotTo, wantFrom, wantTo)
	}
}

func TestCreateRejectsUnknownServiceOrStaff(t *testing.T) {
	t.Parallel()

	t.Run("inactive or unknown service", func(t *testing.T) {
		t.Parallel()
		f := newBookingFixture(t)
		f.avail.snapshotErr = fmt.Errorf("service %s: %w", f.service.ID, domain.ErrNotFound)
		_, err := f.useCase.Create(t.Context(), f.tenantID, f.request(t, 9, 0, "key-1"))
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Create = %v, want domain.ErrNotFound", err)
		}
	})

	t.Run("staff not on the roster", func(t *testing.T) {
		t.Parallel()
		f := newBookingFixture(t)
		in := f.request(t, 9, 0, "key-1")
		in.StaffID = uuid.New()
		_, err := f.useCase.Create(t.Context(), f.tenantID, in)
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Create = %v, want domain.ErrNotFound", err)
		}
		if f.bookings.creates != 0 {
			t.Errorf("a booking for an invisible staff member was stored %d times, want 0", f.bookings.creates)
		}
	})
}

func TestCreateRelaysStoreErrors(t *testing.T) {
	t.Parallel()

	t.Run("replay lookup", func(t *testing.T) {
		t.Parallel()
		f := newBookingFixture(t)
		f.bookings.lookupErr = errors.New("lookup failed")
		_, err := f.useCase.Create(t.Context(), f.tenantID, f.request(t, 9, 0, "key-1"))
		if !errors.Is(err, f.bookings.lookupErr) {
			t.Fatalf("Create = %v, want the lookup error", err)
		}
	})

	t.Run("insert", func(t *testing.T) {
		t.Parallel()
		f := newBookingFixture(t)
		f.bookings.createErr = errors.New("insert failed")
		_, err := f.useCase.Create(t.Context(), f.tenantID, f.request(t, 9, 0, "key-1"))
		if !errors.Is(err, f.bookings.createErr) {
			t.Fatalf("Create = %v, want the insert error", err)
		}
		if errors.Is(err, domain.ErrSlotTaken) {
			t.Errorf("Create = %v, want the store's own error", err)
		}
	})
}

// TestBookingTokenGuardsTheStore pins where the cancel link is checked: a token
// that does not authorise the booking is refused before the store is read, so a
// guessed id cannot probe which bookings exist.
func TestBookingTokenGuardsTheStore(t *testing.T) {
	t.Parallel()
	f := newBookingFixture(t)
	stored := domain.Booking{
		ID:        uuid.New(),
		TenantID:  f.tenantID,
		StaffID:   f.staffID,
		ServiceID: f.service.ID,
		StartsAt:  f.request(t, 9, 0, "key-1").StartsAt,
		Status:    domain.BookingConfirmed,
	}
	f.bookings.store(stored, "key-1")
	otherToken := f.useCase.CancelToken(uuid.New())

	for _, token := range []string{"", "not-a-token", otherToken} {
		if _, err := f.useCase.Get(t.Context(), f.tenantID, stored.ID, token); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("Get with token %q = %v, want domain.ErrForbidden", token, err)
		}
		if err := f.useCase.Cancel(t.Context(), f.tenantID, stored.ID, token); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("Cancel with token %q = %v, want domain.ErrForbidden", token, err)
		}
	}
	if f.bookings.reads != 0 || f.bookings.cancels != 0 {
		t.Fatalf("the store was reached with a bad token: %d reads, %d cancels", f.bookings.reads, f.bookings.cancels)
	}
}

// TestBookingTokenReachesTheStore is the other half of the guard: the token
// Token minted for this booking reads and cancels it, and the cancel is
// status-blind, so repeating it succeeds.
func TestBookingTokenReachesTheStore(t *testing.T) {
	t.Parallel()
	f := newBookingFixture(t)
	stored := domain.Booking{
		ID:        uuid.New(),
		TenantID:  f.tenantID,
		StaffID:   f.staffID,
		ServiceID: f.service.ID,
		StartsAt:  f.request(t, 9, 0, "key-1").StartsAt,
		Status:    domain.BookingConfirmed,
	}
	f.bookings.store(stored, "key-1")

	token := f.useCase.CancelToken(stored.ID)
	booking, err := f.useCase.Get(t.Context(), f.tenantID, stored.ID, token)
	if err != nil {
		t.Fatalf("Get with the signed token: %v", err)
	}
	if booking.ID != stored.ID {
		t.Errorf("Get returned %s, want %s", booking.ID, stored.ID)
	}
	if err := f.useCase.Cancel(t.Context(), f.tenantID, stored.ID, token); err != nil {
		t.Fatalf("Cancel with the signed token: %v", err)
	}
	if err := f.useCase.Cancel(t.Context(), f.tenantID, stored.ID, token); err != nil {
		t.Fatalf("repeated Cancel: %v", err)
	}
	if f.bookings.cancels != 2 {
		t.Errorf("the store saw %d cancels, want 2", f.bookings.cancels)
	}
	booking, err = f.useCase.Get(t.Context(), f.tenantID, stored.ID, token)
	if err != nil {
		t.Fatalf("Get after cancelling: %v", err)
	}
	if booking.Status != domain.BookingCancelled {
		t.Errorf("status after cancelling = %q, want %q", booking.Status, domain.BookingCancelled)
	}
}

func TestGetRelaysStoreErrors(t *testing.T) {
	t.Parallel()
	f := newBookingFixture(t)
	id := uuid.New()
	f.bookings.readErr = errors.New("read failed")

	if _, err := f.useCase.Get(t.Context(), f.tenantID, id, f.useCase.CancelToken(id)); !errors.Is(err, f.bookings.readErr) {
		t.Fatalf("Get = %v, want the read error", err)
	}
	if err := f.useCase.Cancel(t.Context(), f.tenantID, id, f.useCase.CancelToken(id)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Cancel of an unknown booking = %v, want domain.ErrNotFound", err)
	}
}
