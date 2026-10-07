package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// testCancelSecret is long enough for NewCancelSigner.
const testCancelSecret = "app-test-cancel-secret-32-bytes-plus"

// recordingSender captures what the Jobs use case would have emailed.
type recordingSender struct {
	messages []domain.Message
	err      error
}

func (s *recordingSender) Send(_ context.Context, msg domain.Message) error {
	if s.err != nil {
		return s.err
	}
	s.messages = append(s.messages, msg)
	return nil
}

// jobsFixture wires the Jobs use case to a fake booking store, a recording
// sender and a fake clock, with one confirmed Berlin booking 25 hours out.
type jobsFixture struct {
	jobs    *Jobs
	store   *fakeBookingStore
	sender  *recordingSender
	clock   *clock.Fake
	booking domain.Booking
	job     domain.Job
}

func newJobsFixture(t *testing.T) jobsFixture {
	t.Helper()

	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("loading Berlin: %v", err)
	}
	now := time.Date(2026, time.November, 1, 8, 0, 0, 0, time.UTC)
	booking := domain.Booking{
		ID:            uuid.New(),
		TenantID:      uuid.New(),
		CustomerName:  "Ada Lovelace",
		CustomerEmail: "ada@example.com",
		StartsAt:      time.Date(2026, time.November, 2, 9, 0, 0, 0, loc),
		EndsAt:        time.Date(2026, time.November, 2, 9, 40, 0, 0, loc),
		Status:        domain.BookingConfirmed,
	}
	source := BookingMessage{
		Booking: booking,
		Tenant: domain.Tenant{
			ID:       booking.TenantID,
			Slug:     "book",
			Name:     "Salon Book",
			Timezone: "Europe/Berlin",
		},
		ServiceName: "Cut",
		StaffName:   "Sam Stylist",
	}
	store := &fakeBookingStore{messages: map[uuid.UUID]BookingMessage{booking.ID: source}}
	sender := &recordingSender{}
	signer, err := NewCancelSigner(testCancelSecret)
	if err != nil {
		t.Fatalf("building the signer: %v", err)
	}
	fake := clock.NewFake(now)
	return jobsFixture{
		jobs:    NewJobs(store, signer, sender, "http://slotwise.test/", fake),
		store:   store,
		sender:  sender,
		clock:   fake,
		booking: booking,
		job: domain.Job{
			ID:        uuid.New(),
			TenantID:  booking.TenantID,
			BookingID: booking.ID,
			Kind:      domain.JobBookingConfirmation,
			Attempts:  1,
		},
	}
}

func TestReminderRunAt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.November, 1, 8, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		starts time.Time
		want   *time.Time
	}{
		{
			name:   "outside the window schedules 24h before",
			starts: now.Add(48 * time.Hour),
			want:   new(now.Add(24 * time.Hour)),
		},
		{
			name:   "exactly at the window edge schedules nothing",
			starts: now.Add(24 * time.Hour),
			want:   nil,
		},
		{
			name:   "inside the window schedules nothing",
			starts: now.Add(2 * time.Hour),
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := reminderRunAt(now, tt.starts)
			switch {
			case got == nil && tt.want == nil:
			case got == nil || tt.want == nil:
				t.Errorf("reminderRunAt = %v, want %v", got, tt.want)
			case !got.Equal(*tt.want):
				t.Errorf("reminderRunAt = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCancelLinkMatchesThePublicRoute(t *testing.T) {
	t.Parallel()
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	got := cancelLink("http://slotwise.test/", "book", id, "tok-en_1")
	want := "http://slotwise.test/b/book/bookings/11111111-2222-3333-4444-555555555555?token=tok-en_1"
	if got != want {
		t.Errorf("cancelLink = %q, want %q", got, want)
	}
}

// TestHandleConfirmation pins the confirmation a customer receives: the
// recipient, a subject naming the business, the appointment in the tenant's
// timezone, and the signed cancel link the public cancel route accepts.
func TestHandleConfirmation(t *testing.T) {
	t.Parallel()
	f := newJobsFixture(t)

	if err := f.jobs.Handle(t.Context(), f.job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.sender.messages) != 1 {
		t.Fatalf("%d messages, want 1", len(f.sender.messages))
	}
	msg := f.sender.messages[0]
	if msg.To != f.booking.CustomerEmail {
		t.Errorf("To = %q, want %q", msg.To, f.booking.CustomerEmail)
	}
	if !strings.Contains(msg.Subject, "Salon Book") {
		t.Errorf("subject %q does not name the business", msg.Subject)
	}
	for _, want := range []string{
		"Cut",
		"Sam Stylist",
		"Mon 2 Nov 2026 09:00",
		"/b/book/bookings/" + f.booking.ID.String() + "?token=",
	} {
		if !strings.Contains(msg.Body, want) {
			t.Errorf("body does not contain %q:\n%s", want, msg.Body)
		}
	}
}

// TestHandleReminderCarriesTheCancelLink pins the reminder contract: it fires
// before the appointment and carries the same signed cancel link, so a
// reminder's recipient can still cancel.
func TestHandleReminderCarriesTheCancelLink(t *testing.T) {
	t.Parallel()
	f := newJobsFixture(t)
	f.job.Kind = domain.JobBookingReminder
	f.clock.Set(f.booking.StartsAt.Add(-23 * time.Hour))

	if err := f.jobs.Handle(t.Context(), f.job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.sender.messages) != 1 {
		t.Fatalf("%d messages, want 1", len(f.sender.messages))
	}
	msg := f.sender.messages[0]
	if !strings.Contains(msg.Subject, "Reminder") {
		t.Errorf("subject %q does not say it is a reminder", msg.Subject)
	}
	if !strings.Contains(msg.Body, "/b/book/bookings/"+f.booking.ID.String()+"?token=") {
		t.Errorf("reminder body carries no cancel link:\n%s", msg.Body)
	}
}

func TestHandleSkipsCancelledBooking(t *testing.T) {
	t.Parallel()
	f := newJobsFixture(t)
	cancelled := f.store.messages[f.booking.ID]
	cancelled.Booking.Status = domain.BookingCancelled
	f.store.messages[f.booking.ID] = cancelled

	err := f.jobs.Handle(t.Context(), f.job)
	if !errors.Is(err, domain.ErrJobSkipped) {
		t.Errorf("Handle = %v, want domain.ErrJobSkipped", err)
	}
	if len(f.sender.messages) != 0 {
		t.Errorf("%d messages for a cancelled booking, want 0", len(f.sender.messages))
	}
}

func TestHandleSkipsReminderAfterTheAppointmentStarted(t *testing.T) {
	t.Parallel()
	f := newJobsFixture(t)
	f.job.Kind = domain.JobBookingReminder
	f.clock.Set(f.booking.StartsAt.Add(time.Minute))

	err := f.jobs.Handle(t.Context(), f.job)
	if !errors.Is(err, domain.ErrJobSkipped) {
		t.Errorf("Handle = %v, want domain.ErrJobSkipped", err)
	}
	if len(f.sender.messages) != 0 {
		t.Errorf("%d messages for a started appointment, want 0", len(f.sender.messages))
	}
}

// TestHandleSendsLateConfirmation: a confirmation delayed past the appointment
// is still the customer's record of the booking, so it goes out.
func TestHandleSendsLateConfirmation(t *testing.T) {
	t.Parallel()
	f := newJobsFixture(t)
	f.clock.Set(f.booking.StartsAt.Add(time.Hour))

	if err := f.jobs.Handle(t.Context(), f.job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.sender.messages) != 1 {
		t.Errorf("%d messages, want 1", len(f.sender.messages))
	}
}

func TestHandleUnknownKindIsAnError(t *testing.T) {
	t.Parallel()
	f := newJobsFixture(t)
	f.job.Kind = "booking_telegram"

	err := f.jobs.Handle(t.Context(), f.job)
	if err == nil || errors.Is(err, domain.ErrJobSkipped) {
		t.Errorf("Handle = %v, want a hard error", err)
	}
	if len(f.sender.messages) != 0 {
		t.Errorf("%d messages, want 0", len(f.sender.messages))
	}
}

func TestHandleMissingBookingIsSkipped(t *testing.T) {
	t.Parallel()
	f := newJobsFixture(t)
	delete(f.store.messages, f.booking.ID)

	err := f.jobs.Handle(t.Context(), f.job)
	if !errors.Is(err, domain.ErrJobSkipped) {
		t.Errorf("Handle = %v, want domain.ErrJobSkipped", err)
	}
}

func TestHandleRelaysStoreAndSendFailures(t *testing.T) {
	t.Parallel()

	t.Run("store read", func(t *testing.T) {
		t.Parallel()
		f := newJobsFixture(t)
		f.store.messageErr = errors.New("connection reset")
		err := f.jobs.Handle(t.Context(), f.job)
		if err == nil || errors.Is(err, domain.ErrJobSkipped) {
			t.Errorf("Handle = %v, want a hard error", err)
		}
	})

	t.Run("unknown timezone", func(t *testing.T) {
		t.Parallel()
		f := newJobsFixture(t)
		source := f.store.messages[f.booking.ID]
		source.Tenant.Timezone = "Mars/Olympus"
		f.store.messages[f.booking.ID] = source
		err := f.jobs.Handle(t.Context(), f.job)
		if err == nil || errors.Is(err, domain.ErrJobSkipped) {
			t.Errorf("Handle = %v, want a hard error", err)
		}
	})

	t.Run("send", func(t *testing.T) {
		t.Parallel()
		f := newJobsFixture(t)
		f.sender.err = errors.New("smtp refused")
		err := f.jobs.Handle(t.Context(), f.job)
		if err == nil || errors.Is(err, domain.ErrJobSkipped) {
			t.Errorf("Handle = %v, want a hard error", err)
		}
	})
}
