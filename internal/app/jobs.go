package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// reminderLead is how long before an appointment the reminder goes out.
const reminderLead = 24 * time.Hour

// BookingMessage is what one email is built from: the booking, the tenant whose
// name and timezone it is written in, and the two display names it mentions.
type BookingMessage struct {
	Booking     domain.Booking
	Tenant      domain.Tenant
	ServiceName string
	StaffName   string
}

// Jobs delivers the mail a booking owes. It is the queue's handler: the worker
// claims one job, hands it here, and this either sends or reports
// domain.ErrJobSkipped when there is nothing useful left to say.
type Jobs struct {
	bookings BookingStore
	signer   *CancelSigner
	sender   Sender
	baseURL  string
	clock    clock.Clock
}

// NewJobs wires the delivery use case to its store, the signer whose links the
// mail carries, the sender that delivers it, the public base URL those links
// hang off, and the clock the staleness check reads.
func NewJobs(bookings BookingStore, signer *CancelSigner, sender Sender, baseURL string, clk clock.Clock) *Jobs {
	return &Jobs{bookings: bookings, signer: signer, sender: sender, baseURL: baseURL, clock: clk}
}

// Handle delivers one claimed job. The booking is re-read at delivery time, so a
// booking cancelled between the enqueue and the send is skipped rather than
// confirmed: the cancel route stays unchanged and the queue needs no
// cancellation write.
func (j *Jobs) Handle(ctx context.Context, job domain.Job) error {
	if !job.Kind.Valid() {
		return fmt.Errorf("job %s: unknown kind %q", job.ID, job.Kind)
	}

	source, err := j.bookings.BookingMessage(ctx, job.TenantID, job.BookingID)
	if errors.Is(err, domain.ErrNotFound) {
		// The booking is gone and its jobs went with it, so there is nothing to
		// send and nothing to fail.
		return fmt.Errorf("booking %s: %w", job.BookingID, domain.ErrJobSkipped)
	}
	if err != nil {
		return fmt.Errorf("reading booking for job %s: %w", job.ID, err)
	}
	loc, err := time.LoadLocation(source.Tenant.Timezone)
	if err != nil {
		return fmt.Errorf("loading timezone %q: %w", source.Tenant.Timezone, err)
	}
	if err := j.deliverable(job, source); err != nil {
		return err
	}

	msg := j.message(job, source, loc)
	if err := j.sender.Send(ctx, msg); err != nil {
		return fmt.Errorf("sending %s for booking %s: %w", job.Kind, job.BookingID, err)
	}
	return nil
}

// deliverable decides whether the message is still worth sending. A cancelled
// booking never gets mail; a reminder whose appointment has already started is
// skipped because its only content has become false, while a late confirmation
// is still the customer's record of the booking, so it is sent.
func (j *Jobs) deliverable(job domain.Job, source BookingMessage) error {
	if source.Booking.Status != domain.BookingConfirmed {
		return fmt.Errorf("booking %s is %s: %w", source.Booking.ID, source.Booking.Status, domain.ErrJobSkipped)
	}
	if job.Kind == domain.JobBookingReminder && !source.Booking.StartsAt.After(j.clock.Now()) {
		return fmt.Errorf("booking %s started at %s: %w",
			source.Booking.ID, source.Booking.StartsAt.Format(time.RFC3339), domain.ErrJobSkipped)
	}
	return nil
}

// message builds the mail for one kind. Both kinds carry the signed cancel link:
// the customer has no account, so that link is the only way to act on the
// booking, and a reminder whose recipient deleted the confirmation would
// otherwise have no way out.
func (j *Jobs) message(job domain.Job, source BookingMessage, loc *time.Location) domain.Message {
	when := formatWhen(source.Booking, loc)
	link := cancelLink(j.baseURL, source.Tenant.Slug, source.Booking.ID, j.signer.Token(source.Booking.ID))
	switch job.Kind {
	case domain.JobBookingReminder:
		return domain.Message{
			To:      source.Booking.CustomerEmail,
			Subject: fmt.Sprintf("Reminder: your booking at %s is %s", source.Tenant.Name, when),
			Body: fmt.Sprintf("Your booking is coming up.\n\nWhat: %s\nWhen: %s\nWith: %s\n\nCancel this booking: %s\n",
				source.ServiceName, when, source.StaffName, link),
		}
	case domain.JobBookingConfirmation:
		return domain.Message{
			To:      source.Booking.CustomerEmail,
			Subject: fmt.Sprintf("Booking confirmed at %s", source.Tenant.Name),
			Body: fmt.Sprintf("Your booking is confirmed.\n\nWhat: %s\nWhen: %s\nWith: %s\n\nCancel this booking: %s\n",
				source.ServiceName, when, source.StaffName, link),
		}
	}
	// Handle rejects every other kind before this runs.
	return domain.Message{}
}

// reminderRunAt is when the reminder for a booking starting at startsAt should
// go out, or nil when the booking was made inside the lead window and gets
// none. A booking made inside 24 hours is followed by its own confirmation
// seconds later, so an immediate reminder could only repeat it.
func reminderRunAt(now, startsAt time.Time) *time.Time {
	at := startsAt.Add(-reminderLead)
	if !at.After(now) {
		return nil
	}
	return &at
}

// formatWhen renders an appointment in the tenant's timezone, the wall clock
// the customer booked on.
func formatWhen(b domain.Booking, loc *time.Location) string {
	start, end := b.StartsAt.In(loc), b.EndsAt.In(loc)
	return start.Format("Mon 2 Jan 2006 15:04") + "-" + end.Format("15:04") + " " + start.Format("MST")
}

// cancelLink is the public URL that authorises cancelling one booking. It is the
// same path and query the public cancel route accepts:
// /b/{slug}/bookings/{id}?token=…
func cancelLink(baseURL, slug string, id uuid.UUID, token string) string {
	return strings.TrimSuffix(baseURL, "/") + "/b/" + url.PathEscape(slug) + "/bookings/" +
		url.PathEscape(id.String()) + "?token=" + url.QueryEscape(token)
}
