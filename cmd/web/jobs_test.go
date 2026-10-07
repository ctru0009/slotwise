//go:build integration

package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/adapters/worker"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// The booking email tests run the real worker loop over the same database the
// HTTP server wrote to: the queue rows the booking route commits are claimed
// and delivered by the library cmd/worker runs, through the harness's recording
// sender.

// TestBookingConfirmationIsDelivered pins the confirmation end to end: the
// booking commits with its job in one transaction, the worker delivers the
// message, and the cancel link in the body is the route the M4 cancel flow
// already accepts.
func TestBookingConfirmationIsDelivered(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")
	form := h.slotForm("book", fixture).values()

	created := h.post("/b/book/bookings", form, nil).
		wantStatus(t, http.StatusSeeOther)

	// The web process queued the mail but sent nothing: no worker has run yet.
	if got := h.sender.count(); got != 0 {
		t.Errorf("%d messages before any worker ran, want 0", got)
	}
	if got := h.jobStatuses(fixture.Tenant)["booking_confirmation"]; got != "ready" {
		t.Errorf("confirmation job status = %q before any worker ran, want ready", got)
	}

	stop := h.startWorker(t)
	h.waitForMessages(t, 1)
	stop()

	msg := h.sender.last(t)
	if msg.To != "ada@example.com" {
		t.Errorf("To = %q, want the booking's customer email", msg.To)
	}
	if !strings.Contains(msg.Subject, "Booking confirmed at Tenant book") {
		t.Errorf("subject %q does not name the business", msg.Subject)
	}
	link := h.cancelLinkIn(t, msg)
	h.get(link.RequestURI()).wantStatus(t, http.StatusOK).wantContains(t, `name="token" value="`)

	if got := h.jobStatuses(fixture.Tenant)["booking_confirmation"]; got != "done" {
		t.Errorf("confirmation job status = %q after delivery, want done", got)
	}

	// The replay answers the same Location, queues nothing, and delivers no
	// second message.
	h.post("/b/book/bookings", form, nil).
		wantStatus(t, http.StatusSeeOther).wantLocation(t, created.header.Get("Location"))
	stop = h.startWorker(t)
	time.Sleep(300 * time.Millisecond)
	stop()
	if got := h.sender.count(); got != 1 {
		t.Errorf("%d messages after the replay, want 1", got)
	}
}

// TestReminderGoesOut24HoursBeforeTheAppointment pins the reminder's schedule
// and its content: it is not delivered while the appointment is more than a day
// away, it fires once the clock reaches starts_at - 24h, and it carries the
// cancel link too.
func TestReminderGoesOut24HoursBeforeTheAppointment(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")

	h.post("/b/book/bookings", h.slotForm("book", fixture).values(), nil).
		wantStatus(t, http.StatusSeeOther)

	stop := h.startWorker(t)
	h.waitForMessages(t, 1)
	stop()
	if got := h.sender.count(); got != 1 {
		t.Fatalf("%d messages after the confirmation, want 1", got)
	}
	confirmation := h.sender.last(t)
	if !strings.Contains(confirmation.Subject, "Booking confirmed") {
		t.Fatalf("the first message is not the confirmation: %q", confirmation.Subject)
	}

	// Advance the fake clock to the reminder's moment: it is due exactly 24
	// hours before the appointment.
	h.clock.Set(h.bookingDay.Add(-24 * time.Hour))
	stop = h.startWorker(t)
	h.waitForMessages(t, 2)
	stop()

	reminder := h.sender.last(t)
	if reminder.To != "ada@example.com" {
		t.Errorf("reminder To = %q, want the booking's customer email", reminder.To)
	}
	if !strings.Contains(reminder.Subject, "Reminder") {
		t.Errorf("reminder subject %q does not say it is a reminder", reminder.Subject)
	}
	if !strings.Contains(reminder.Subject, "Tenant book") {
		t.Errorf("reminder subject %q does not name the business", reminder.Subject)
	}
	h.cancelLinkIn(t, reminder)

	statuses := h.jobStatuses(fixture.Tenant)
	if statuses["booking_confirmation"] != "done" || statuses["booking_reminder"] != "done" {
		t.Errorf("job statuses = %v, want both done", statuses)
	}
}

// TestBookingInsideTheWindowGetsNoReminder pins the scheduling policy decision:
// a booking made less than 24 hours ahead gets its confirmation only, and no
// reminder is ever delivered, even after the appointment's reminder moment and
// its start have passed.
func TestBookingInsideTheWindowGetsNoReminder(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")

	// One hour before the fixture's 09:00 slot, so the first offered slot is
	// inside the reminder's 24 hour lead window.
	h.clock.Set(h.bookingDay.Add(-time.Hour))

	h.post("/b/book/bookings", h.slotForm("book", fixture).values(), nil).
		wantStatus(t, http.StatusSeeOther)

	statuses := h.jobStatuses(fixture.Tenant)
	if _, ok := statuses["booking_reminder"]; ok {
		t.Errorf("job statuses = %v, want no reminder job inside the window", statuses)
	}
	if statuses["booking_confirmation"] != "ready" {
		t.Errorf("confirmation job status = %q, want ready", statuses["booking_confirmation"])
	}

	stop := h.startWorker(t)
	h.waitForMessages(t, 1)
	stop()
	if got := h.sender.count(); got != 1 {
		t.Fatalf("%d messages after the confirmation, want 1", got)
	}
	if !strings.Contains(h.sender.last(t).Subject, "Booking confirmed") {
		t.Errorf("the delivered message is not the confirmation: %q", h.sender.last(t).Subject)
	}

	// Past the appointment's reminder moment and its start: still one message.
	h.clock.Set(h.bookingDay.Add(time.Hour))
	stop = h.startWorker(t)
	time.Sleep(300 * time.Millisecond)
	stop()
	if got := h.sender.count(); got != 1 {
		t.Errorf("%d messages after the appointment passed, want the confirmation only", got)
	}
}

// startWorker runs the real worker loop over the harness database and returns
// the function that stops it.
func (h *harness) startWorker(t *testing.T) (stop func()) {
	t.Helper()
	db := pgtest.AppDB(t, h.appDSN)
	signer, err := app.NewCancelSigner(testCancelKey)
	if err != nil {
		t.Fatalf("building the signer: %v", err)
	}
	jobs := app.NewJobs(db, signer, h.sender, testBaseURL, h.clock)
	w, err := worker.New(worker.Config{
		WorkerID:       "web-e2e",
		Lease:          10 * time.Second,
		HandlerTimeout: time.Second,
		PollInterval:   20 * time.Millisecond,
		MaxAttempts:    3,
		BackoffBase:    time.Second,
		BackoffCap:     time.Minute,
	}, worker.Deps{
		Jobs:    db,
		Handler: jobs.Handle,
		Clock:   h.clock,
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("building the worker: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("worker Run = %v, want nil", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("the worker did not stop within 5s")
		}
	}
}

// waitForMessages waits for the recorder to hold n messages.
func (h *harness) waitForMessages(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for h.sender.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d messages after 10s, want %d", h.sender.count(), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// cancelLinkIn extracts the cancel link from a delivered message and parses it,
// so the test can walk the URL exactly as its recipient would.
func (h *harness) cancelLinkIn(t *testing.T, msg domain.Message) *url.URL {
	t.Helper()
	const marker = "Cancel this booking: "
	_, body, ok := strings.Cut(msg.Body, marker)
	if !ok {
		t.Fatalf("message body carries no cancel link:\n%s", msg.Body)
	}
	raw, _, _ := strings.Cut(body, "\n")
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		t.Fatalf("parsing the cancel link %q: %v", raw, err)
	}
	if parsed.Path == "" || parsed.Query().Get("token") == "" {
		t.Fatalf("cancel link %q carries no path or token", raw)
	}
	return parsed
}

// jobStatuses reads the tenant's queue rows as kind -> status.
func (h *harness) jobStatuses(tenantID uuid.UUID) map[string]string {
	h.t.Helper()
	rows, err := h.owner.Query(h.t.Context(),
		`SELECT kind, status FROM jobs WHERE tenant_id = $1::uuid`, tenantID.String())
	if err != nil {
		h.t.Fatalf("reading job statuses: %v", err)
	}
	defer rows.Close()

	statuses := map[string]string{}
	for rows.Next() {
		var kind, status string
		if err := rows.Scan(&kind, &status); err != nil {
			h.t.Fatalf("scanning job status: %v", err)
		}
		statuses[kind] = status
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("reading job statuses: %v", err)
	}
	return statuses
}

// count returns how many messages the recording sender captured.
func (s *recordingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

// last returns the most recent captured message.
func (s *recordingSender) last(t *testing.T) domain.Message {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		t.Fatal("no message was sent")
	}
	return s.messages[len(s.messages)-1]
}
