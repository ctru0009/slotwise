//go:build integration

package main

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// The public booking tests drive the same wired server the login tests do; the
// fixture's staff member works Monday 09:00–12:00 in Berlin and the service
// blocks 40 minutes, 10 of them buffer. The Monday itself is derived from the
// clock (harness.bookingDay): the harness clock stands at the real current
// time, so a pinned date would turn these tests red the moment the calendar
// passed it.
const (
	bookingSlotTaken = "was just taken"
	cancelLinkDead   = "not valid"
	missingKeyText   = "could not be identified"
)

// TestPublicSlotsRoute covers the search route's refusals: an unknown business
// is a 404, a malformed service or date a 400, and an unknown or inactive
// service a 404 rather than an empty list.
func TestPublicSlotsRoute(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")

	page := h.get(h.slotsPath("book", fixture)).wantStatus(t, http.StatusOK)
	if !strings.Contains(page.body, `action="/b/book/bookings"`) {
		t.Error("the slots page carries no booking form")
	}
	for _, want := range []string{
		`action="/b/book/slots"`,
		`name="from" type="date" value="` + h.bookingDate() + `"`,
		`name="service" value="` + fixture.Service.String() + `"`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the slots page does not carry the range form (%s)", want)
		}
	}

	h.get("/b/book/slots?service=not-a-uuid&from="+h.bookingDate()).
		wantStatus(t, http.StatusBadRequest)
	h.get("/b/book/slots?service="+fixture.Service.String()+"&from=nope").
		wantStatus(t, http.StatusBadRequest)
	h.get(h.slotsPath("book", fixture)+"&to="+h.bookingDay.AddDate(0, 0, -1).Format("2006-01-02")).
		wantStatus(t, http.StatusBadRequest)
	h.get("/b/book/slots?service="+uuid.NewString()+"&from="+h.bookingDate()).
		wantStatus(t, http.StatusNotFound).wantContains(t, notFoundPageText)
	h.get("/b/nobody/slots?service="+fixture.Service.String()+"&from="+h.bookingDate()).
		wantStatus(t, http.StatusNotFound).wantContains(t, notFoundPageText)
}

// TestBookingFlow books from the rendered form and pins the whole round trip:
// the create redirects to the booking page, the replay answers the same
// Location without storing a second row, and the page offers the cancel form.
func TestBookingFlow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")
	form := h.slotForm("book", fixture)

	created := h.post("/b/book/bookings", form.values(), nil).
		wantStatus(t, http.StatusSeeOther)
	location := created.header.Get("Location")
	if !strings.HasPrefix(location, "/b/book/bookings/") || !strings.Contains(location, "?token=") {
		t.Fatalf("Location = %q, want /b/book/bookings/{id}?token=…", location)
	}

	page := h.get(location).wantStatus(t, http.StatusOK).wantContains(t, "Ada Lovelace")
	if !strings.Contains(page.body, `name="token" value="`) {
		t.Error("the booking page carries no cancel form")
	}
	if got := h.bookingRows(fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows, want 1", got)
	}

	replayed := h.post("/b/book/bookings", form.values(), nil).
		wantStatus(t, http.StatusSeeOther).wantLocation(t, location)
	if got := h.bookingRows(fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows after the replay, want 1", got)
	}
	if replayed.header.Get("Location") != location {
		t.Errorf("the replay answered %q, want %q", replayed.header.Get("Location"), location)
	}
}

// TestBookingUsesTheHeaderKeyWhenThereIsNoField pins the header contract the
// spec documents: a script can send Idempotency-Key and omit the hidden field.
func TestBookingUsesTheHeaderKeyWhenThereIsNoField(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")
	form := h.slotForm("book", fixture)
	values := form.values()
	values.Del("idempotency_key")

	headers := map[string]string{"Idempotency-Key": "header-key-1"}
	created := h.post("/b/book/bookings", values, headers).wantStatus(t, http.StatusSeeOther)
	h.post("/b/book/bookings", values, headers).
		wantStatus(t, http.StatusSeeOther).wantLocation(t, created.header.Get("Location"))
	if got := h.bookingRows(fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows, want 1", got)
	}
}

func TestBookingWithoutAKeyIsRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")
	values := h.slotForm("book", fixture).values()
	values.Del("idempotency_key")

	h.post("/b/book/bookings", values, nil).
		wantStatus(t, http.StatusBadRequest).wantContains(t, missingKeyText)
	if got := h.bookingRows(fixture.Tenant); got != 0 {
		t.Errorf("%d booking rows, want 0", got)
	}
}

func TestBookingOnATakenSlotIsAConflict(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")

	// The form is rendered while the slot is free, and another customer takes
	// it before the POST arrives.
	values := h.slotForm("book", fixture).values()
	if got := values.Get("starts_at"); got != h.bookingStart() {
		t.Fatalf("the form offers %q, want the 09:00 slot %q", got, h.bookingStart())
	}
	pgtest.SeedBookingSpan(t, h.owner, fixture, h.bookingStartUTC(), 40)

	h.post("/b/book/bookings", values, nil).
		wantStatus(t, http.StatusConflict).wantContains(t, bookingSlotTaken)
	if got := h.bookingRows(fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows, want only the seeded one", got)
	}
}

// TestConcurrentSameKeyBookingsOverHTTP is the HTTP half of the same-key race:
// two requests from one rendered form must land on one booking and one
// Location.
func TestConcurrentSameKeyBookingsOverHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")
	values := h.slotForm("book", fixture).values()

	const callers = 8
	statuses := make([]int, callers)
	locations := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			statuses[n], locations[n], errs[n] = h.tryPost("/b/book/bookings", values, nil)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	for i, status := range statuses {
		if status != http.StatusSeeOther {
			t.Errorf("caller %d got status %d, want %d", i, status, http.StatusSeeOther)
		}
	}
	for i, location := range locations {
		if location != locations[0] {
			t.Errorf("caller %d landed on %q, want %q", i, location, locations[0])
		}
	}
	if got := h.bookingRows(fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows, want 1", got)
	}
}

// TestConcurrentBookingRaceOverHTTP fires twenty different customers at one
// slot: exactly one wins the redirect, the rest are told the slot is gone, and
// one row is stored. The emails differ because the limiter is per account.
func TestConcurrentBookingRaceOverHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")
	form := h.slotForm("book", fixture)

	const callers = 20
	statuses := make([]int, callers)
	locations := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			values := form.values()
			values.Set("idempotency_key", "http-race-"+uuid.NewString())
			values.Set("customer_email", fmt.Sprintf("racer%d@example.com", n))
			statuses[n], locations[n], errs[n] = h.tryPost("/b/book/bookings", values, nil)
		}(i)
	}
	wg.Wait()

	wins, conflicts := 0, 0
	var winnerLocation string
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		switch statuses[i] {
		case http.StatusSeeOther:
			wins++
			winnerLocation = locations[i]
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("caller %d got status %d, want 303 or 409", i, statuses[i])
		}
	}
	if wins != 1 {
		t.Errorf("%d callers booked the slot, want exactly 1", wins)
	}
	if conflicts != callers-1 {
		t.Errorf("%d callers got a conflict, want %d", conflicts, callers-1)
	}
	if got := h.bookingRows(fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows, want 1", got)
	}
	if !strings.Contains(h.get(winnerLocation).body, "Ada Lovelace") {
		t.Error("the winner's Location does not resolve to its booking")
	}
}

// TestBookingPostsAreThrottled spends one customer's budget with replays, which
// all succeed, so the refusal is the limiter and not a conflict.
func TestBookingPostsAreThrottled(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")
	values := h.slotForm("book", fixture).values()

	// The harness clock stands still, so the window cannot roll over mid-test.
	for range bookingLimit {
		h.post("/b/book/bookings", values, nil).wantStatus(t, http.StatusSeeOther)
	}
	h.post("/b/book/bookings", values, nil).
		wantStatus(t, http.StatusTooManyRequests).wantContains(t, tooManyText)
	if got := h.bookingRows(fixture.Tenant); got != 1 {
		t.Errorf("%d booking rows, want 1", got)
	}
}

// TestBookingWritesAreThrottledPerBusiness pins the bucket a client cannot
// rotate: one address has its own budget, so inventing a new address per request
// still runs into the business's. Sixty attempts are admitted — the first books,
// the rest are conflicts — and the sixty-first is refused.
func TestBookingWritesAreThrottledPerBusiness(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")
	form := h.slotForm("book", fixture)

	booked := 0
	for i := range bookingTenantLimit {
		values := form.values()
		values.Set("idempotency_key", fmt.Sprintf("tenant-budget-%d", i))
		values.Set("customer_email", fmt.Sprintf("budget%d@example.com", i))
		switch status := h.post("/b/book/bookings", values, nil).status; status {
		case http.StatusSeeOther:
			booked++
		case http.StatusConflict:
		default:
			t.Fatalf("attempt %d got status %d, want 303 or 409", i, status)
		}
	}
	if booked != 1 {
		t.Errorf("%d attempts booked the slot, want 1", booked)
	}

	values := form.values()
	values.Set("idempotency_key", "tenant-budget-over")
	values.Set("customer_email", "budget-over@example.com")
	h.post("/b/book/bookings", values, nil).
		wantStatus(t, http.StatusTooManyRequests).wantContains(t, tooManyText)
}

// TestCancelBookingFlow walks the signed link the way its recipient does: the
// page renders, a wrong token is refused, the cancel is idempotent, and the
// page then shows the cancelled booking without a cancel form.
func TestCancelBookingFlow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")

	created := h.post("/b/book/bookings", h.slotForm("book", fixture).values(), nil).
		wantStatus(t, http.StatusSeeOther)
	location := created.header.Get("Location")

	page := h.get(location).wantStatus(t, http.StatusOK)
	token := inputValue(t, page.body, "token")
	id := bookingIDFromLocation(t, location)

	h.post("/b/book/bookings/"+id+"/cancel", url.Values{"token": {"forged-token"}}, nil).
		wantStatus(t, http.StatusForbidden).wantContains(t, cancelLinkDead)
	h.get("/b/book/bookings/"+id+"?token=forged-token").
		wantStatus(t, http.StatusForbidden).wantContains(t, cancelLinkDead)

	h.post("/b/book/bookings/"+id+"/cancel", url.Values{"token": {token}}, nil).
		wantStatus(t, http.StatusSeeOther).wantLocation(t, location)
	h.post("/b/book/bookings/"+id+"/cancel", url.Values{"token": {token}}, nil).
		wantStatus(t, http.StatusSeeOther).wantLocation(t, location)

	cancelled := h.get(location).wantStatus(t, http.StatusOK).wantContains(t, "This booking is cancelled.")
	if strings.Contains(cancelled.body, "/cancel") {
		t.Error("the cancelled booking still offers the cancel form")
	}
	if got := h.bookingStatus(fixture.Tenant); got != "cancelled" {
		t.Errorf("stored status = %q, want cancelled", got)
	}
}

// TestBookingOfAnotherTenantIsNotFound pins the HTTP tenancy boundary: a token
// that authorises a booking id is still refused when the booking belongs to
// another business, and the refusal is a 404 rather than a leak.
func TestBookingOfAnotherTenantIsNotFound(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	other := h.seedBookingFixture("other")
	h.seedBookingFixture("book")

	created := h.post("/b/other/bookings", h.slotForm("other", other).values(), nil).
		wantStatus(t, http.StatusSeeOther)
	location := created.header.Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parsing %q: %v", location, err)
	}
	// The same booking id and token, addressed under the other business.
	foreign := "/b/book" + strings.TrimPrefix(parsed.Path, "/b/other") + "?" + parsed.RawQuery
	h.get(foreign).wantStatus(t, http.StatusNotFound).wantContains(t, notFoundPageText)

	id := bookingIDFromLocation(t, location)
	h.post("/b/book/bookings/"+id+"/cancel", url.Values{"token": {parsed.Query().Get("token")}}, nil).
		wantStatus(t, http.StatusNotFound).wantContains(t, notFoundPageText)
	if got := h.bookingStatus(other.Tenant); got != "confirmed" {
		t.Errorf("the other tenant's booking reads %q, want confirmed", got)
	}
}

func TestCrossOriginBookingPostIsRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")

	h.post("/b/book/bookings", h.slotForm("book", fixture).values(), map[string]string{
		"Origin":         "https://evil.example",
		"Sec-Fetch-Site": "cross-site",
	}).wantStatus(t, http.StatusForbidden)
	if got := h.bookingRows(fixture.Tenant); got != 0 {
		t.Errorf("%d booking rows, want 0", got)
	}
}

// nextBookingMonday is 09:00 Berlin on the next Monday at least two days out.
// The harness clock stands at the real current time, so the fixture's week has
// to move with the clock; two days of margin keep the start clear of the
// engine's NotBefore filter however long a test run takes.
func nextBookingMonday(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("loading Berlin: %v", err)
	}
	now := time.Now().In(loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, loc).AddDate(0, 0, 2)
	for day.Weekday() != time.Monday {
		day = day.AddDate(0, 0, 1)
	}
	return day
}

// bookingDate is the fixture Monday the search route takes as from.
func (h *harness) bookingDate() string {
	return h.bookingDay.Format("2006-01-02")
}

// bookingStart is the fixture's 09:00 slot as the booking form submits it: an
// RFC3339 instant with the tenant's own offset, which the day's DST decides.
func (h *harness) bookingStart() string {
	return h.bookingDay.Format(time.RFC3339)
}

// bookingStartUTC is the same instant in UTC, which is how a seeded overlap is
// written.
func (h *harness) bookingStartUTC() string {
	return h.bookingDay.UTC().Format(time.RFC3339)
}

// seedBookingFixture provisions a tenant whose staff member works Monday
// 09:00–12:00 and whose service blocks 40 minutes, as the owner pool.
func (h *harness) seedBookingFixture(slug string) pgtest.Fixture {
	h.t.Helper()
	fixture := pgtest.Seed(h.t, h.owner, slug)
	_, err := h.owner.Exec(h.t.Context(),
		`UPDATE services SET buffer_minutes = 10 WHERE id = $1::uuid`, fixture.Service.String())
	if err != nil {
		h.t.Fatalf("setting the service buffer: %v", err)
	}
	pgtest.SeedWeeklyRule(h.t, h.owner, fixture, int(time.Monday), 9*60, 12*60)
	return fixture
}

// slotsPath is the search URL for the fixture's service on the fixture Monday.
func (h *harness) slotsPath(slug string, f pgtest.Fixture) string {
	h.t.Helper()
	return "/b/" + slug + "/slots?service=" + f.Service.String() + "&from=" + h.bookingDate()
}

// slotForm reads the first booking form off the slots page, so the test posts
// exactly what a browser would.
func (h *harness) slotForm(slug string, f pgtest.Fixture) slotForm {
	h.t.Helper()
	page := h.get(h.slotsPath(slug, f)).wantStatus(h.t, http.StatusOK)
	return slotForm{
		serviceID:      inputValue(h.t, page.body, "service_id"),
		staffID:        inputValue(h.t, page.body, "staff_id"),
		startsAt:       inputValue(h.t, page.body, "starts_at"),
		idempotencyKey: inputValue(h.t, page.body, "idempotency_key"),
	}
}

// bookingRows counts the tenant's bookings as the owner pool.
func (h *harness) bookingRows(tenantID uuid.UUID) int {
	h.t.Helper()
	var count int
	err := h.owner.QueryRow(h.t.Context(),
		`SELECT count(*) FROM bookings WHERE tenant_id = $1::uuid`, tenantID.String()).Scan(&count)
	if err != nil {
		h.t.Fatalf("counting bookings: %v", err)
	}
	return count
}

// bookingStatus reads the tenant's single booking status as the owner pool.
func (h *harness) bookingStatus(tenantID uuid.UUID) string {
	h.t.Helper()
	var status string
	err := h.owner.QueryRow(h.t.Context(),
		`SELECT status FROM bookings WHERE tenant_id = $1::uuid`, tenantID.String()).Scan(&status)
	if err != nil {
		h.t.Fatalf("reading the booking status: %v", err)
	}
	return status
}

// tryPost performs one request and reports its status and Location instead of
// failing the test, so concurrent callers can collect their own outcomes.
func (h *harness) tryPost(path string, form url.Values, headers map[string]string) (int, string, error) {
	req, err := http.NewRequestWithContext(h.t.Context(), http.MethodPost, h.ts.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return 0, "", err
	}
	return resp.StatusCode, resp.Header.Get("Location"), nil
}

// slotForm is the hidden state of one booking form on the slots page.
type slotForm struct {
	serviceID      string
	staffID        string
	startsAt       string
	idempotencyKey string
}

// values is the POST body that form would submit.
func (f slotForm) values() url.Values {
	return url.Values{
		"service_id":      {f.serviceID},
		"staff_id":        {f.staffID},
		"starts_at":       {f.startsAt},
		"idempotency_key": {f.idempotencyKey},
		"customer_name":   {"Ada Lovelace"},
		"customer_email":  {"ada@example.com"},
	}
}

// inputValue reads the value of a named input, undoing the escaping the
// template applied, exactly as a browser would.
func inputValue(t *testing.T, body, name string) string {
	t.Helper()
	pattern := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`)
	match := pattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("the page carries no %s input", name)
	}
	return html.UnescapeString(match[1])
}

// bookingIDFromLocation reads the booking id out of a booking Location.
func bookingIDFromLocation(t *testing.T, location string) string {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parsing %q: %v", location, err)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	id := segments[len(segments)-1]
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("Location %q carries no booking id: %v", location, err)
	}
	return id
}
