//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ctru0009/slotwise/internal/adapters/postgres"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// The end-to-end tests drive the same composition production uses: wire() over
// a throwaway Postgres, served by httptest. Only the clock and the message
// sender are replaced, because the tests have to see the reset link.
const (
	// testBaseURL is not the test server's address: it only ends up inside the
	// reset link the email carries, which the tests parse for the token.
	testBaseURL      = "http://slotwise.test"
	ownerEmail       = "owner@example.com"
	ownerPassword    = "correct-horse-battery"
	resetPassword    = "brand-new-horse-battery"
	sessionCookie    = "slotwise_session"
	loginFailedText  = "email or password is incorrect"
	expiredLinkText  = "This link is invalid or has expired"
	tooManyText      = "Too many attempts"
	notFoundPageText = "Not Found"
)

func TestLoginFlow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedTenant("a", "Salon A", ownerEmail, ownerPassword)

	h.login("a", ownerEmail, ownerPassword)

	h.get("/app").wantStatus(t, http.StatusOK).wantContains(t, "Salon A")

	h.post("/app/services", url.Values{"name": {"Cut"}, "duration_minutes": {"30"}}, nil).
		wantStatus(t, http.StatusSeeOther).wantLocation(t, "/app")

	h.get("/app").wantStatus(t, http.StatusOK).wantContains(t, "Cut")

	h.post("/app/logout", url.Values{}, nil).
		wantStatus(t, http.StatusSeeOther).wantLocation(t, "/login")

	h.get("/app").wantStatus(t, http.StatusSeeOther).wantLocation(t, "/login")
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedTenant("a", "Salon A", ownerEmail, ownerPassword)
	h.seedTenant("b", "Salon B", "owner-b@example.com", ownerPassword)

	unknown := h.post("/app/a/login",
		url.Values{"email": {"nobody@example.com"}, "password": {ownerPassword}}, nil).
		wantStatus(t, http.StatusOK).wantNoCookie(t)
	wrong := h.post("/app/a/login",
		url.Values{"email": {ownerEmail}, "password": {"not-the-password"}}, nil).
		wantStatus(t, http.StatusOK).wantNoCookie(t)

	if unknown.body != wrong.body {
		t.Errorf("an unknown email and a wrong password rendered different pages:\nunknown: %s\nwrong:   %s",
			unknown.body, wrong.body)
	}
	if !strings.Contains(unknown.body, loginFailedText) {
		t.Errorf("the failure page does not carry %q", loginFailedText)
	}

	// Tenant b's key is untouched by the attempts above, so its counter starts
	// at zero: ten failures are allowed and the eleventh attempt is refused.
	for range 10 {
		h.post("/app/b/login",
			url.Values{"email": {"owner-b@example.com"}, "password": {"not-the-password"}}, nil).
			wantStatus(t, http.StatusOK)
	}
	h.post("/app/b/login",
		url.Values{"email": {"owner-b@example.com"}, "password": {"not-the-password"}}, nil).
		wantStatus(t, http.StatusTooManyRequests).wantContains(t, tooManyText)
	// The limiter is consulted before authentication, so the right password is
	// refused too while the window is open.
	h.post("/app/b/login",
		url.Values{"email": {"owner-b@example.com"}, "password": {ownerPassword}}, nil).
		wantStatus(t, http.StatusTooManyRequests).wantContains(t, tooManyText)
}

func TestCrossTenantServiceWrite(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedTenant("a", "Salon A", ownerEmail, ownerPassword)
	h.seedTenant("b", "Salon B", "owner-b@example.com", ownerPassword)

	foreign := h.seedService("b", "B cut")
	h.login("a", ownerEmail, ownerPassword)

	h.post("/app/services/"+foreign+"/active", url.Values{"active": {"false"}}, nil).
		wantStatus(t, http.StatusNotFound).wantContains(t, notFoundPageText)

	if !h.serviceActive("b", "B cut") {
		t.Error("tenant A deactivated tenant B's service")
	}
}

func TestResetFlowIsSingleUse(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedTenant("a", "Salon A", ownerEmail, ownerPassword)

	h.post("/app/a/forgot", url.Values{"email": {ownerEmail}}, nil).
		wantStatus(t, http.StatusSeeOther).wantLocation(t, "/app/a/forgot?sent=1")
	token := h.sender.token(t)

	h.post("/app/a/reset", url.Values{"token": {token}, "password": {resetPassword}}, nil).
		wantStatus(t, http.StatusSeeOther).wantLocation(t, "/app/a/login?reset=1")

	// The old password is gone and the new one signs in.
	h.post("/app/a/login", url.Values{"email": {ownerEmail}, "password": {ownerPassword}}, nil).
		wantStatus(t, http.StatusOK).wantContains(t, loginFailedText)
	h.login("a", ownerEmail, resetPassword)

	// The token is spent: replaying it cannot set a third password.
	h.post("/app/a/reset", url.Values{"token": {token}, "password": {"third-password-attempt"}}, nil).
		wantStatus(t, http.StatusOK).wantContains(t, expiredLinkText)
	h.post("/app/logout", url.Values{}, nil)
	h.login("a", ownerEmail, resetPassword)

	// Expiry is the database's clock, so the test moves the row instead of the
	// fake clock the server holds.
	h.post("/app/a/forgot", url.Values{"email": {ownerEmail}}, nil).wantStatus(t, http.StatusSeeOther)
	expired := h.sender.token(t)
	h.expireResetTokens()
	h.post("/app/a/reset", url.Values{"token": {expired}, "password": {"fourth-password-attempt"}}, nil).
		wantStatus(t, http.StatusOK).wantContains(t, expiredLinkText)
	h.post("/app/logout", url.Values{}, nil)
	h.login("a", ownerEmail, resetPassword)
}

func TestCrossOriginPostIsRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedTenant("a", "Salon A", ownerEmail, ownerPassword)

	login := url.Values{"email": {ownerEmail}, "password": {ownerPassword}}

	h.post("/app/a/login", login, map[string]string{
		"Origin":         "https://evil.example",
		"Sec-Fetch-Site": "cross-site",
	}).wantStatus(t, http.StatusForbidden).wantNoCookie(t)

	h.post("/app/a/login", login, map[string]string{"Sec-Fetch-Site": "same-origin"}).
		wantStatus(t, http.StatusSeeOther).wantLocation(t, "/app").wantCookie(t)
}

// TestSessionsSurviveRestart is the observable difference of a database-backed
// session store: a second server over the same database accepts the cookie the
// first one issued.
func TestSessionsSurviveRestart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.seedTenant("a", "Salon A", ownerEmail, ownerPassword)
	h.login("a", ownerEmail, ownerPassword)

	h.start()

	h.get("/app").wantStatus(t, http.StatusOK).wantContains(t, "Salon A")
}

// harness drives one wired server over a throwaway Postgres, keeping the
// cookie jar and the owner connection the assertions need.
type harness struct {
	t      *testing.T
	appDSN string
	owner  *pgxpool.Pool
	auth   *app.Auth
	sender *recordingSender
	clock  *clock.Fake
	client *http.Client
	ts     *httptest.Server
}

// newHarness starts Postgres and a server over it. Tenants are provisioned by
// the test through the same Bootstrap use case the deployment uses.
func newHarness(t *testing.T) *harness {
	t.Helper()
	appDSN, ownerDSN := pgtest.Start(t)
	h := &harness{
		t:      t,
		appDSN: appDSN,
		owner:  pgtest.Owner(t, ownerDSN),
		sender: &recordingSender{},
		// The fake stands still, but it starts at the wall clock: reset tokens
		// are stamped with it and the database compares them against its own
		// now(), so a fake in the past would issue dead links.
		clock: clock.NewFake(time.Now().UTC()),
	}

	db, err := postgres.New(t.Context(), appDSN)
	if err != nil {
		t.Fatalf("opening the app connection: %v", err)
	}
	t.Cleanup(db.Close)
	h.auth = app.NewAuth(db, db, db, h.sender, h.clock)

	h.start()
	return h
}

// start wires and serves a server over the harness's database. Calling it again
// simulates a restart: a new server with a new pool, sharing only the database
// and the cookie jar in the harness's client.
func (h *harness) start() {
	h.t.Helper()
	srv, cleanup, err := wire(h.t.Context(), config{
		databaseURL: h.appDSN,
		baseURL:     testBaseURL,
		clock:       h.clock,
		sender:      h.sender,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		h.t.Fatalf("wiring the server: %v", err)
	}
	h.t.Cleanup(cleanup)

	ts := httptest.NewServer(srv.Handler)
	h.t.Cleanup(ts.Close)
	h.ts = ts

	if h.client != nil {
		return
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		h.t.Fatalf("building the cookie jar: %v", err)
	}
	h.client = &http.Client{
		Jar: jar,
		// Redirects are the assertions, so never follow one.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// seedTenant provisions a tenant and its owner login.
func (h *harness) seedTenant(slug, name, email, password string) {
	h.t.Helper()
	_, err := h.auth.Bootstrap(h.t.Context(), app.BootstrapInput{
		Slug:          slug,
		Name:          name,
		Timezone:      "Europe/Berlin",
		OwnerEmail:    email,
		OwnerPassword: password,
	})
	if err != nil {
		h.t.Fatalf("bootstrapping tenant %s: %v", slug, err)
	}
}

// login signs in and insists on the session the dashboard needs.
func (h *harness) login(slug, email, password string) response {
	h.t.Helper()
	return h.post("/app/"+slug+"/login", url.Values{"email": {email}, "password": {password}}, nil).
		wantStatus(h.t, http.StatusSeeOther).wantLocation(h.t, "/app").wantCookie(h.t)
}

// seedService inserts a service for a slug as the owner pool and returns its id,
// bypassing the HTTP layer so a cross-tenant write can name a row the caller
// could never create.
func (h *harness) seedService(slug, name string) string {
	h.t.Helper()
	var id string
	err := h.owner.QueryRow(h.t.Context(), `
		INSERT INTO services (tenant_id, name, duration_minutes)
		SELECT id, $2, 30 FROM tenants WHERE slug = $1
		RETURNING id::text`, slug, name).Scan(&id)
	if err != nil {
		h.t.Fatalf("seeding a service for %s: %v", slug, err)
	}
	return id
}

// serviceActive reads a service's active flag as the owner pool.
func (h *harness) serviceActive(slug, name string) bool {
	h.t.Helper()
	var active bool
	err := h.owner.QueryRow(h.t.Context(), `
		SELECT s.active
		  FROM services s
		  JOIN tenants t ON t.id = s.tenant_id
		 WHERE t.slug = $1 AND s.name = $2`, slug, name).Scan(&active)
	if err != nil {
		h.t.Fatalf("reading service %s of %s: %v", name, slug, err)
	}
	return active
}

// expireResetTokens moves every reset token into the past as the owner pool.
func (h *harness) expireResetTokens() {
	h.t.Helper()
	_, err := h.owner.Exec(h.t.Context(),
		`UPDATE password_reset_tokens SET expires_at = now() - interval '1 hour'`)
	if err != nil {
		h.t.Fatalf("expiring reset tokens: %v", err)
	}
}

func (h *harness) get(path string) response {
	h.t.Helper()
	return h.do(http.MethodGet, path, nil, nil)
}

func (h *harness) post(path string, form url.Values, headers map[string]string) response {
	h.t.Helper()
	return h.do(http.MethodPost, path, form, headers)
}

// do performs one request and reads the whole body, so assertions can compare
// two responses after both have been closed.
func (h *harness) do(method, path string, form url.Values, headers map[string]string) response {
	h.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(h.t.Context(), method, h.ts.URL+path, body)
	if err != nil {
		h.t.Fatalf("building %s %s: %v", method, path, err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("reading %s %s: %v", method, path, err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: string(raw)}
}

// response is one finished HTTP response.
type response struct {
	status int
	header http.Header
	body   string
}

func (r response) wantStatus(t *testing.T, want int) response {
	t.Helper()
	if r.status != want {
		t.Errorf("status = %d, want %d\nbody: %s", r.status, want, r.body)
	}
	return r
}

func (r response) wantLocation(t *testing.T, want string) response {
	t.Helper()
	if got := r.header.Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	return r
}

func (r response) wantContains(t *testing.T, want string) response {
	t.Helper()
	if !strings.Contains(r.body, want) {
		t.Errorf("body does not contain %q\nbody: %s", want, r.body)
	}
	return r
}

// wantCookie insists the response started a session.
func (r response) wantCookie(t *testing.T) response {
	t.Helper()
	if !strings.Contains(r.header.Get("Set-Cookie"), sessionCookie+"=") {
		t.Errorf("no %s cookie in %q", sessionCookie, r.header.Values("Set-Cookie"))
	}
	return r
}

// wantNoCookie insists the response issued no session, which is what makes a
// failed login indistinguishable from a rejected one.
func (r response) wantNoCookie(t *testing.T) response {
	t.Helper()
	if strings.Contains(r.header.Get("Set-Cookie"), sessionCookie+"=") {
		t.Errorf("unexpected %s cookie in %q", sessionCookie, r.header.Values("Set-Cookie"))
	}
	return r
}

// recordingSender captures what the server would have emailed.
type recordingSender struct {
	mu       sync.Mutex
	messages []domain.Message
}

func (s *recordingSender) Send(_ context.Context, msg domain.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	return nil
}

// token returns the reset token from the most recent message, so the test can
// use the link exactly as its recipient would.
func (s *recordingSender) token(t *testing.T) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		t.Fatal("no message was sent, so there is no reset link")
	}
	body := s.messages[len(s.messages)-1].Body
	parsed, err := url.Parse(body)
	if err != nil {
		t.Fatalf("parsing the reset link %q: %v", body, err)
	}
	token := parsed.Query().Get("token")
	if token == "" {
		t.Fatalf("the reset link %q carries no token", body)
	}
	return token
}
