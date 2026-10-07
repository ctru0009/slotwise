package web

import (
	"errors"
	"net/http"

	"github.com/ctru0009/slotwise/internal/adapters/web/views"
	"github.com/ctru0009/slotwise/internal/domain"
)

// messageResetLinkDead is what the reset form shows for a token that is
// unknown, already used or expired.
const messageResetLinkDead = "This link is invalid or has expired"

// forgotForm serves the reset-request form at GET /app/{slug}/forgot.
func (s *server) forgotForm(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenantOr404(w, r)
	if !ok {
		return
	}
	page := views.ForgotPage{Tenant: tenant, Sent: r.URL.Query().Get("sent") == "1"}
	renderPage(r.Context(), w, views.Forgot(page))
}

// forgotSubmit mails a reset link at POST /app/{slug}/forgot. It throttles
// accepted requests rather than failures, because the response is identical
// either way: an unknown email reports success too, so nobody can probe which
// addresses have an account.
func (s *server) forgotSubmit(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenantOr404(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		fail(w, r, http.StatusBadRequest, messageFormUnreadable)
		return
	}
	email := r.FormValue("email")
	// Allow records the request: this endpoint is throttled on requests rather
	// than failures, because the response is identical either way.
	if !s.deps.Reset.Allow(accountKey(tenant.ID, email), s.deps.Clock.Now()) {
		fail(w, r, http.StatusTooManyRequests, messageRateLimited)
		return
	}
	if err := s.deps.Auth.RequestPasswordReset(r.Context(), tenant, email, s.deps.BaseURL); err != nil {
		fail(w, r, http.StatusInternalServerError, messageServerError)
		return
	}
	http.Redirect(w, r, "/app/"+tenant.Slug+"/forgot?sent=1", http.StatusSeeOther)
}

// resetForm serves the new-password form at GET /app/{slug}/reset, carrying
// the token from the link into a hidden field.
func (s *server) resetForm(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenantOr404(w, r)
	if !ok {
		return
	}
	page := views.ResetPage{Tenant: tenant, Token: r.URL.Query().Get("token")}
	renderPage(r.Context(), w, views.Reset(page))
}

// resetSubmit spends a reset token and stores the new password at
// POST /app/{slug}/reset.
func (s *server) resetSubmit(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenantOr404(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		fail(w, r, http.StatusBadRequest, messageFormUnreadable)
		return
	}
	// Verifying a password costs a hash whatever the token is, so this
	// unauthenticated route is throttled per business before any hashing: the
	// window is wide enough for someone retrying a link, and narrow enough that
	// garbage tokens cannot pin the CPU.
	if !s.deps.ResetSubmit.Allow(tenant.ID.String(), s.deps.Clock.Now()) {
		fail(w, r, http.StatusTooManyRequests, messageRateLimited)
		return
	}
	token := r.FormValue("token")
	page := views.ResetPage{Tenant: tenant, Token: token}
	err := s.deps.Auth.ResetPassword(r.Context(), tenant.ID, token, r.FormValue("password"))
	switch {
	case errors.Is(err, domain.ErrResetTokenInvalid):
		page.Error = messageResetLinkDead
		renderPage(r.Context(), w, views.Reset(page))
	case errors.Is(err, domain.ErrInvalidInput):
		page.Error = err.Error()
		renderPage(r.Context(), w, views.Reset(page))
	case err != nil:
		fail(w, r, http.StatusInternalServerError, messageServerError)
	default:
		http.Redirect(w, r, "/app/"+tenant.Slug+"/login?reset=1", http.StatusSeeOther)
	}
}
