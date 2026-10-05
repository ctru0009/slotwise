package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

// The messages the sign-in flow renders in its forms.
const (
	messageBadSlug   = "That does not look like a business slug."
	messageBadLogin  = "email or password is incorrect"
	messageResetDone = "Your password has been reset. Sign in with your new password."
)

// startRedirect sends the site root to the slug form.
func (s *server) startRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// loginStart serves the slug form at GET /login and forwards a valid slug to
// that business's sign-in page.
func (s *server) loginStart(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	switch {
	case slug == "":
		s.render(w, r, PageStart, StartPage{})
	case !app.ValidSlug(slug):
		s.render(w, r, PageStart, StartPage{Slug: slug, Error: messageBadSlug})
	default:
		// PathEscape is a no-op for a slug that passed ValidSlug; it keeps
		// the redirect target from ever carrying a scheme, an authority or a
		// query, even if the validation above were bypassed.
		http.Redirect(w, r, "/app/"+url.PathEscape(slug)+"/login", http.StatusSeeOther)
	}
}

// tenantOr404 resolves the {slug} path value, rendering the 404 page when no
// business is served there. It reports whether the caller should continue.
func (s *server) tenantOr404(w http.ResponseWriter, r *http.Request) (domain.Tenant, bool) {
	tenant, err := s.deps.Auth.TenantBySlug(r.Context(), r.PathValue("slug"))
	switch {
	case errors.Is(err, domain.ErrTenantNotFound):
		s.deps.Views.fail(w, r, http.StatusNotFound, messageTenantGone)
		return domain.Tenant{}, false
	case err != nil:
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
		return domain.Tenant{}, false
	}
	return tenant, true
}

// loginForm serves the sign-in form for one business at GET /app/{slug}/login.
func (s *server) loginForm(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenantOr404(w, r)
	if !ok {
		return
	}
	var notice string
	if r.URL.Query().Get("reset") == "1" {
		notice = messageResetDone
	}
	s.render(w, r, PageLogin, LoginPage{Tenant: tenant, Notice: notice})
}

// loginSubmit signs a login in at POST /app/{slug}/login. A wrong password and
// an unknown email re-render the form with one message, so the page cannot be
// used to enumerate accounts.
func (s *server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenantOr404(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		s.deps.Views.fail(w, r, http.StatusBadRequest, messageFormUnreadable)
		return
	}
	email := r.FormValue("email")
	key := accountKey(tenant.ID, email)
	// Allow records the attempt, so simultaneous guesses cannot all pass the
	// check before any of them is counted.
	if !s.deps.Login.Allow(key, s.deps.Clock.Now()) {
		s.deps.Views.fail(w, r, http.StatusTooManyRequests, messageRateLimited)
		return
	}
	user, err := s.deps.Auth.Authenticate(r.Context(), tenant.ID, email, r.FormValue("password"))
	switch {
	case errors.Is(err, domain.ErrInvalidCredentials):
		s.render(w, r, PageLogin, LoginPage{Tenant: tenant, Error: messageBadLogin})
		return
	case err != nil:
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
		return
	}
	// Renew the token before storing the login, so a fixated session id cannot
	// survive an authentication.
	if err := s.deps.Sessions.RenewToken(r.Context()); err != nil {
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
		return
	}
	ctx := r.Context()
	s.deps.Sessions.Put(ctx, sessionTenantID, tenant.ID.String())
	s.deps.Sessions.Put(ctx, sessionUserID, user.ID.String())
	s.deps.Sessions.Put(ctx, sessionTenantSlug, tenant.Slug)
	// The session carries the hash it was created against, so changing the
	// password retires every session that predates the change (withUser
	// compares them).
	s.deps.Sessions.Put(ctx, sessionPasswordHash, user.PasswordHash)
	s.deps.Login.Reset(key)
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// accountKey identifies the account a limiter window belongs to. Keying on the
// account rather than the client address is deliberate: X-Forwarded-For is
// client-controlled behind an unknown proxy.
func accountKey(tenantID uuid.UUID, email string) string {
	return tenantID.String() + "|" + strings.ToLower(strings.TrimSpace(email))
}
