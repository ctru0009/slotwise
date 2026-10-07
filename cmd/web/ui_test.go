//go:build integration

package main

import (
	"html"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// The pages the public flow and the dashboard render, driven through the wired
// server: the landing page a stranger sees, the slots page a customer books
// from, the ICS download the booking page offers, and the calendar an owner
// looks at.

// TestLandingPageIsThePublicCatalogue pins what GET /b/{slug} publishes: the
// business name and its active services, each linking into the slot search, and
// nothing about any other tenant.
func TestLandingPageIsThePublicCatalogue(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedFixture("shop")
	other := h.seedFixture("other")
	withdrawn := h.seedService("shop", "Withdrawn")
	h.hideServiceRow("shop", "Withdrawn")

	landing := h.get("/b/shop").wantStatus(t, http.StatusOK)
	landing.wantContains(t, "Tenant shop")
	if _, ok := serviceLink(t, landing.body, fixture.Service.String()); !ok {
		t.Errorf("the landing page does not link into the slot search for %s", fixture.Service)
	}
	if _, ok := serviceLink(t, landing.body, withdrawn); ok {
		t.Error("the landing page links a withdrawn service into the slot search")
	}
	if strings.Contains(landing.body, "Withdrawn") {
		t.Error("the landing page publishes a withdrawn service")
	}
	if strings.Contains(landing.body, other.Service.String()) || strings.Contains(landing.body, "Tenant other") {
		t.Error("the landing page publishes another tenant's catalogue")
	}
	if email := h.staffEmail("shop"); email != "" && strings.Contains(landing.body, email) {
		t.Errorf("the landing page publishes a staff email (%s)", email)
	}

	h.get("/b/nobody").wantStatus(t, http.StatusNotFound).wantContains(t, notFoundPageText)
}

// TestSlotsPageNamesTheStaffMember pins the public identity rule: a customer
// sees who they are booking with by name, never by address, while the booking
// form still carries the ids it has to submit.
func TestSlotsPageNamesTheStaffMember(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")

	page := h.get(h.slotsPath("book", fixture)).wantStatus(t, http.StatusOK)
	page.wantContains(t, "Staff book")
	page.wantContains(t, `name="staff_id" value="`+fixture.Staff.String()+`"`)
	if email := h.staffEmail("book"); email != "" && strings.Contains(page.body, email) {
		t.Errorf("the slots page publishes a staff email (%s)", email)
	}
}

// TestBookingCalendarDownload walks the whole download: the booking page offers
// it, a wrong or wrong-purpose token is refused before anything is read, the
// file carries the appointment's exact instants, and a cancelled booking is
// gone rather than downloadable.
func TestBookingCalendarDownload(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")

	created := h.post("/b/book/bookings", h.slotForm("book", fixture).values(), nil).
		wantStatus(t, http.StatusSeeOther)
	booking, cancelToken := splitBookingLink(t, created.header.Get("Location"))
	page := h.get(created.header.Get("Location")).wantStatus(t, http.StatusOK)
	download := downloadLink(t, page.body)

	downloadToken := tokenOf(t, download)

	// A missing token, a token that is simply wrong, and the token minted for
	// the other purpose are all refused, and the refusal happens before the
	// booking is read.
	h.get(booking+"/ics").wantStatus(t, http.StatusForbidden)
	h.get(booking+"/ics?token="+strings.Repeat("A", 43)).wantStatus(t, http.StatusForbidden)
	h.get(booking+"/ics?token="+cancelToken).wantStatus(t, http.StatusForbidden)
	// The purpose split holds the other way round too: the calendar token does
	// not open the booking page and cannot cancel.
	h.get(booking+"?token="+downloadToken).wantStatus(t, http.StatusForbidden)
	h.post(booking+"/cancel", url.Values{"token": {downloadToken}}, nil).wantStatus(t, http.StatusForbidden)

	// A second business exists, so the slug resolves: what refuses the request
	// is the tenant-scoped read, not a missing tenant. A token minted for one
	// tenant's booking must not hand it to another tenant's page.
	h.seedFixture("other")
	elsewhere := strings.Replace(booking, "/b/book/", "/b/other/", 1)
	h.get(elsewhere+"/ics?token="+downloadToken).wantStatus(t, http.StatusNotFound)
	h.get(elsewhere+"?token="+cancelToken).wantStatus(t, http.StatusNotFound)

	file := h.get(download).wantStatus(t, http.StatusOK)
	if got := file.header.Get("Content-Type"); got != "text/calendar; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/calendar", got)
	}
	wantName := `attachment; filename="book-` + h.bookingDate() + `.ics"`
	if got := file.header.Get("Content-Disposition"); got != wantName {
		t.Errorf("Content-Disposition = %q, want %q", got, wantName)
	}
	assertCalendarFile(t, file.body, h.bookingStartUTC(), occupiedEnd(t, h.bookingStartUTC()))
	if again := h.get(download).wantStatus(t, http.StatusOK); again.body != file.body {
		t.Error("two downloads of one booking differ, so the event is not stable")
	}
	if strings.Contains(file.body, "ada@example.com") {
		t.Error("the calendar file carries the customer's address")
	}

	h.cancelBooking(created.header.Get("Location"), page.body)
	h.get(download).wantStatus(t, http.StatusGone)
}

// TestCalendarWindowAcrossDST is the timezone gate: the week and day windows
// are local days, so the week that contains a transition is 167 or 169 hours
// long and the appointments either side of the boundary land in the right
// column with the right local time.
func TestCalendarWindowAcrossDST(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedFixture("cal")
	hash, err := app.HashPassword(ownerPassword)
	if err != nil {
		t.Fatalf("hashing the owner password: %v", err)
	}
	pgtest.SeedUser(t, h.owner, fixture, ownerEmail, hash, "owner")
	h.login("cal", ownerEmail, ownerPassword)

	// Berlin moves to summer time on 29 March 2026: that Sunday is 23 hours
	// long and the week holding it is 167 hours. 21:00Z is 23:00 local on
	// Sunday; 22:30Z is already 00:30 on Monday 30 March, which the window of
	// the week starting 23 March must not show.
	pgtest.SeedBookingSpan(t, h.owner, fixture, "2026-03-29T21:00:00Z", 40)
	pgtest.SeedBookingSpan(t, h.owner, fixture, "2026-03-29T22:30:00Z", 40)

	week := h.get("/app?mode=week&anchor=2026-03-23").wantStatus(t, http.StatusOK)
	week.wantContains(t, "Sun 29 Mar 2026 23:00 CEST")
	for _, day := range []string{"2026-03-23", "2026-03-24", "2026-03-25", "2026-03-26", "2026-03-27", "2026-03-28", "2026-03-29"} {
		week.wantContains(t, day)
	}
	if strings.Contains(week.body, "Mon 30 Mar 2026 00:30 CEST") {
		t.Error("the week of 23 March shows a booking that belongs to the next week")
	}

	// Berlin moves back on 25 October 2026: that Sunday is 25 hours long and
	// the week holding it is 169 hours, so 22:30Z is 23:30 local on Sunday and
	// belongs to this window.
	pgtest.SeedBookingSpan(t, h.owner, fixture, "2026-10-25T22:30:00Z", 40)
	back := h.get("/app?mode=week&anchor=2026-10-19").wantStatus(t, http.StatusOK)
	back.wantContains(t, "Sun 25 Oct 2026 23:30 CET")

	day := h.get("/app?mode=day&anchor=2026-03-29").wantStatus(t, http.StatusOK)
	day.wantContains(t, "Sun 29 Mar 2026 23:00 CEST")
	if strings.Contains(day.body, "Mon 30 Mar 2026 00:30 CEST") {
		t.Error("the day view of 29 March shows a booking that belongs to 30 March")
	}

	fragment := h.get("/app/calendar?mode=week&anchor=2026-03-23").wantStatus(t, http.StatusOK)
	fragment.wantContains(t, `id="calendar"`)
	fragment.wantContains(t, "Sun 29 Mar 2026 23:00 CEST")
	if strings.Contains(fragment.body, "<!DOCTYPE html>") || strings.Contains(fragment.body, "Sign out") {
		t.Error("the calendar fragment carries the whole page instead of one element")
	}
	if strings.Contains(fragment.body, `hx-push-url="/app/calendar`) {
		t.Error("the fragment pushes its own URL, so a reload would render a fragment")
	}

	h.get("/app?mode=day&anchor=nope").wantStatus(t, http.StatusBadRequest)
	h.get("/app?mode=month").wantStatus(t, http.StatusBadRequest)
}

// TestCalendarFragmentSendsHTMXToSignIn covers the expired session: htmx must
// be told to navigate rather than handed a login page to swap into the
// fragment it asked for.
func TestCalendarFragmentSendsHTMXToSignIn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	fragment := h.do(http.MethodGet, "/app/calendar?mode=week", nil, map[string]string{"HX-Request": "true"}).
		wantStatus(t, http.StatusNoContent)
	if got := fragment.header.Get("HX-Redirect"); got != "/login" {
		t.Errorf("HX-Redirect = %q, want /login", got)
	}
	if fragment.body != "" {
		t.Errorf("the fragment answered with a body: %q", fragment.body)
	}

	h.do(http.MethodGet, "/app/calendar?mode=week", nil, nil).
		wantStatus(t, http.StatusSeeOther).wantLocation(t, "/login")
}

// TestAuditTrailNamesActorAndSubject drives every audited change through the
// routes and reads the trail back: the action each one records, the login the
// catalogue rows name, the order the dashboard shows them in, and the htmx
// contract the calendar toolbar renders.
func TestAuditTrailNamesActorAndSubject(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fixture := h.seedBookingFixture("book")

	// The public flow has no login to name, so its rows carry no actor.
	created := h.post("/b/book/bookings", h.slotForm("book", fixture).values(), nil).
		wantStatus(t, http.StatusSeeOther)
	page := h.get(created.header.Get("Location")).wantStatus(t, http.StatusOK)
	h.cancelBooking(created.header.Get("Location"), page.body)

	h.seedTenant("app", "Tenant app", ownerEmail, ownerPassword)
	h.login("app", ownerEmail, ownerPassword)
	owner := h.userID("app", ownerEmail)

	service := h.createService("Haircut")
	h.updateService(service, "Haircut Deluxe")
	h.activateService(service, false)
	member := h.createStaff("Anna")
	h.updateStaff(member, "Anna Smith")
	h.activateStaff(member, false)

	tenant := h.tenantID("app")
	want := map[string]int{
		"service.created": 1, "service.updated": 1, "service.deactivated": 1,
		"staff.created": 1, "staff.updated": 1, "staff.deactivated": 1,
	}
	if got := h.auditActions(t, tenant); !maps.Equal(got, want) {
		t.Errorf("audit actions = %v, want %v", got, want)
	}
	actors := h.auditActors(t, tenant)
	if len(actors) != len(want) {
		t.Errorf("catalogue rows naming an actor = %d, want one per action (%d): %v", len(actors), len(want), actors)
	}
	for _, id := range actors {
		if id != owner {
			t.Errorf("a catalogue row names actor %v, want the signed-in owner %v", id, owner)
		}
	}
	if got := h.auditActors(t, fixture.Tenant); len(got) != 0 {
		t.Errorf("booking rows name actors %v, want none: the public flow has no login", got)
	}

	dashboard := h.get("/app").wantStatus(t, http.StatusOK)
	dashboard.wantContains(t, "Audit log")
	dashboard.wantContains(t, "Haircut Deluxe")
	dashboard.wantContains(t, "Failed deliveries")
	dashboard.wantContains(t, "No dead-lettered jobs")
	dashboard.wantContains(t, `id="calendar"`)
	// Newest first: the last catalogue change is the first row on the page.
	newest, oldest := strings.Index(dashboard.body, "staff.deactivated"), strings.Index(dashboard.body, "service.created")
	if newest < 0 || oldest < 0 || newest > oldest {
		t.Errorf("the audit list is not newest first: staff.deactivated at %d, service.created at %d", newest, oldest)
	}
	// The calendar navigates over htmx: a fragment request that pushes the page
	// URL, so a reload renders the whole page for the same window.
	for _, want := range []string{`hx-get="/app/calendar?`, `hx-target="#calendar"`, `hx-swap="outerHTML"`, `hx-push-url="/app?`} {
		if !strings.Contains(dashboard.body, want) {
			t.Errorf("the calendar toolbar does not carry %s", want)
		}
	}
}

// TestStaticAssetsAreServedFromTheBinary pins the vendored asset route: the
// allowlisted file is served to cache, anything else is not served at all, and
// no page reaches for a CDN.
func TestStaticAssetsAreServedFromTheBinary(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	asset := h.get("/static/htmx-2.0.4.min.js").wantStatus(t, http.StatusOK)
	if got := asset.header.Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/javascript", got)
	}
	if got := asset.header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want a one-year immutable policy", got)
	}
	if !strings.Contains(asset.body, "htmx") {
		t.Error("the served asset is not htmx")
	}

	for _, path := range []string{
		"/static/go.mod",
		"/static/htmx-2.0.4.LICENSE.txt",
		"/static/../go.mod",
		"/static/%2e%2e%2fgo.mod",
	} {
		if got := h.get(path); got.status == http.StatusOK {
			t.Errorf("GET %s answered 200, want it refused", path)
		}
	}

	page := h.get("/login").wantStatus(t, http.StatusOK)
	page.wantContains(t, "/static/htmx-2.0.4.min.js")
	for _, cdn := range []string{"unpkg.com", "cdn.jsdelivr", "cdnjs"} {
		if strings.Contains(page.body, cdn) {
			t.Errorf("a page loads a script from %s", cdn)
		}
	}
}

// assertCalendarFile parses the download the way a client does and checks the
// appointment's own instants are in it.
func assertCalendarFile(t *testing.T, body, startsAtUTC, endsAtUTC string) {
	t.Helper()

	properties := parseCalendarFile(t, body)
	if got, want := properties["DTSTART"], calendarInstant(t, startsAtUTC); got != want {
		t.Errorf("DTSTART = %q, want the booking's instant %q", got, want)
	}
	// The span is the occupied one the database stores, so it ends duration
	// plus buffer after the start rather than at the appointment's own end.
	if got, want := properties["DTEND"], calendarInstant(t, endsAtUTC); got != want {
		t.Errorf("DTEND = %q, want the occupied end %q", got, want)
	}
	for _, name := range []string{"UID", "DTSTAMP", "SUMMARY"} {
		if properties[name] == "" {
			t.Errorf("the file has no %s", name)
		}
	}
}

// occupiedEnd is the instant a fixture booking occupies: its 30 minute service
// plus the 10 minute buffer the fixture sets.
func occupiedEnd(t *testing.T, startsAtUTC string) string {
	t.Helper()
	start, err := time.Parse(time.RFC3339, startsAtUTC)
	if err != nil {
		t.Fatalf("parsing %q: %v", startsAtUTC, err)
	}
	return start.Add(40 * time.Minute).Format(time.RFC3339)
}

// parseCalendarFile unfolds the file the way a client does, checking the
// encoding rules on the way, and returns its properties.
func parseCalendarFile(t *testing.T, body string) map[string]string {
	t.Helper()

	if !strings.HasSuffix(body, "END:VCALENDAR\r\n") {
		t.Errorf("the file does not end with END:VCALENDAR and CRLF: %q", body[len(body)-40:])
	}
	if stray := strings.ReplaceAll(body, "\r\n", ""); strings.ContainsAny(stray, "\r\n") {
		t.Error("a line break in the file is not CRLF")
	}
	properties := map[string]string{}
	for _, logical := range unfoldCalendarLines(t, body) {
		if name, value, ok := strings.Cut(logical, ":"); ok {
			properties[name] = value
		}
	}
	return properties
}

// unfoldCalendarLines joins the physical lines back into content lines, with a
// leading space marking a continuation, and checks each physical line's length.
func unfoldCalendarLines(t *testing.T, body string) []string {
	t.Helper()

	logical := []string{}
	for _, physical := range strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n") {
		if len(physical) > 75 {
			t.Errorf("a physical line is %d octets, want at most 75", len(physical))
		}
		if strings.HasPrefix(physical, " ") && len(logical) > 0 {
			logical[len(logical)-1] += strings.TrimPrefix(physical, " ")
			continue
		}
		logical = append(logical, physical)
	}
	return logical
}

func calendarInstant(t *testing.T, rfc3339 string) string {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatalf("parsing %q: %v", rfc3339, err)
	}
	return parsed.UTC().Format("20060102T150405Z")
}

// splitBookingLink returns the booking's path without its query and the cancel
// token the public flow handed the customer.
func splitBookingLink(t *testing.T, location string) (string, string) {
	t.Helper()
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parsing %q: %v", location, err)
	}
	token := parsed.Query().Get("token")
	if parsed.Path == "" || token == "" {
		t.Fatalf("Location %q is not a booking link with a token", location)
	}
	return parsed.Path, token
}

// serviceLink finds the search link a landing page renders for one service and
// returns the local date it starts from.
func serviceLink(t *testing.T, body, serviceID string) (string, bool) {
	t.Helper()
	for _, match := range regexp.MustCompile(`href="(/b/[^"]+/slots\?[^"]+)"`).FindAllStringSubmatch(body, -1) {
		link, err := url.Parse(html.UnescapeString(match[1]))
		if err != nil {
			t.Fatalf("parsing %q: %v", match[1], err)
		}
		if link.Query().Get("service") != serviceID {
			continue
		}
		return link.Query().Get("from"), true
	}
	return "", false
}

// auditActions counts the tenant's recorded actions as the owner pool.
func (h *harness) auditActions(t *testing.T, tenantID uuid.UUID) map[string]int {
	t.Helper()
	rows, err := h.owner.Query(t.Context(),
		`SELECT action, count(*) FROM audit_log WHERE tenant_id = $1 GROUP BY action`, tenantID)
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var action string
		var count int
		if err := rows.Scan(&action, &count); err != nil {
			t.Fatalf("scanning the audit log: %v", err)
		}
		got[action] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	return got
}

// auditActors returns the actor each recorded action names, as the owner pool
// sees it. An action with no login to name is left out.
func (h *harness) auditActors(t *testing.T, tenantID uuid.UUID) map[string]uuid.UUID {
	t.Helper()
	rows, err := h.owner.Query(t.Context(),
		`SELECT action, actor_user_id FROM audit_log WHERE tenant_id = $1 AND actor_user_id IS NOT NULL`, tenantID)
	if err != nil {
		t.Fatalf("reading the audit actors: %v", err)
	}
	defer rows.Close()
	got := map[string]uuid.UUID{}
	for rows.Next() {
		var action string
		var actor uuid.UUID
		if err := rows.Scan(&action, &actor); err != nil {
			t.Fatalf("scanning the audit actors: %v", err)
		}
		got[action] = actor
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the audit actors: %v", err)
	}
	return got
}

// tenantID reads a tenant's id as the owner pool.
func (h *harness) tenantID(slug string) uuid.UUID {
	h.t.Helper()
	var id uuid.UUID
	if err := h.owner.QueryRow(h.t.Context(), `SELECT id FROM tenants WHERE slug = $1`, slug).Scan(&id); err != nil {
		h.t.Fatalf("reading the id of %s: %v", slug, err)
	}
	return id
}

// userID reads a login's id as the owner pool.
func (h *harness) userID(slug, email string) uuid.UUID {
	h.t.Helper()
	var id uuid.UUID
	err := h.owner.QueryRow(h.t.Context(), `
		SELECT u.id FROM users u
		 WHERE u.email = $2 AND u.tenant_id = (SELECT id FROM tenants WHERE slug = $1)`, slug, email).Scan(&id)
	if err != nil {
		h.t.Fatalf("reading the id of %s: %v", email, err)
	}
	return id
}

// rowID reads one row's id by name, which is how a test addresses what the
// routes just created.
func (h *harness) rowID(slug, table, name string) string {
	h.t.Helper()
	query := "SELECT id::text FROM " + table + " WHERE name = $2 AND tenant_id = (SELECT id FROM tenants WHERE slug = $1)"
	var id string
	if err := h.owner.QueryRow(h.t.Context(), query, slug, name).Scan(&id); err != nil {
		h.t.Fatalf("reading the id of %s %s: %v", table, name, err)
	}
	return id
}

// createService adds a service through the dashboard form and returns its id.
func (h *harness) createService(name string) string {
	h.t.Helper()
	h.post("/app/services", url.Values{
		"name": {name}, "duration_minutes": {"30"}, "buffer_minutes": {"10"}, "price_cents": {"2500"},
	}, nil).wantStatus(h.t, http.StatusSeeOther)
	return h.rowID("app", "services", name)
}

// updateService replaces a service through the dashboard form.
func (h *harness) updateService(id, name string) {
	h.t.Helper()
	h.post("/app/services/"+id, url.Values{
		"name": {name}, "duration_minutes": {"45"}, "buffer_minutes": {"5"}, "price_cents": {"3000"},
	}, nil).wantStatus(h.t, http.StatusSeeOther)
}

// activateService flips a service's active flag through the dashboard form.
func (h *harness) activateService(id string, active bool) {
	h.t.Helper()
	h.post("/app/services/"+id+"/active", url.Values{"active": {strconv.FormatBool(active)}}, nil).
		wantStatus(h.t, http.StatusSeeOther)
}

// createStaff adds a staff member through the dashboard form and returns its id.
func (h *harness) createStaff(name string) string {
	h.t.Helper()
	email := strings.ToLower(strings.ReplaceAll(name, " ", ".")) + "@example.com"
	h.post("/app/staff", url.Values{"name": {name}, "email": {email}}, nil).
		wantStatus(h.t, http.StatusSeeOther)
	return h.rowID("app", "staff", name)
}

// updateStaff replaces a staff member through the dashboard form.
func (h *harness) updateStaff(id, name string) {
	h.t.Helper()
	email := strings.ToLower(strings.ReplaceAll(name, " ", ".")) + "@example.com"
	h.post("/app/staff/"+id, url.Values{"name": {name}, "email": {email}}, nil).
		wantStatus(h.t, http.StatusSeeOther)
}

// activateStaff flips a staff member's active flag through the dashboard form.
func (h *harness) activateStaff(id string, active bool) {
	h.t.Helper()
	h.post("/app/staff/"+id+"/active", url.Values{"active": {strconv.FormatBool(active)}}, nil).
		wantStatus(h.t, http.StatusSeeOther)
}

// tokenOf returns the token a signed link carries.
func tokenOf(t *testing.T, link string) string {
	t.Helper()
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parsing %q: %v", link, err)
	}
	token := parsed.Query().Get("token")
	if token == "" {
		t.Fatalf("link %q carries no token", link)
	}
	return token
}

// downloadLink reads the calendar URL off the booking page, exactly as a
// customer would click it.
func downloadLink(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`href="(/b/[^"]+/ics\?token=[^"]+)"`).FindStringSubmatch(body)
	if match == nil {
		t.Fatal("the booking page offers no calendar download")
	}
	return html.UnescapeString(match[1])
}

// hideServiceRow withdraws a seeded service as the owner pool, which is how a
// test sets up a catalogue row the routes never touched.
func (h *harness) hideServiceRow(slug, name string) {
	h.t.Helper()
	_, err := h.owner.Exec(h.t.Context(), `
		UPDATE services SET active = false
		 WHERE name = $2 AND tenant_id = (SELECT id FROM tenants WHERE slug = $1)`, slug, name)
	if err != nil {
		h.t.Fatalf("withdrawing service %s: %v", name, err)
	}
}

// staffEmail reads the seeded staff member's address for a slug.
func (h *harness) staffEmail(slug string) string {
	h.t.Helper()
	var email string
	err := h.owner.QueryRow(h.t.Context(), `
		SELECT s.email FROM staff s
		 WHERE s.tenant_id = (SELECT id FROM tenants WHERE slug = $1) LIMIT 1`, slug).Scan(&email)
	if err != nil {
		return ""
	}
	return email
}

// cancelBooking posts the cancel form read off the booking page.
func (h *harness) cancelBooking(location, page string) {
	h.t.Helper()
	path, _ := splitBookingLink(h.t, location)
	h.post(path+"/cancel", url.Values{"token": {inputValue(h.t, page, "token")}}, nil).
		wantStatus(h.t, http.StatusSeeOther)
}
