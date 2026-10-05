package web

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/domain"
)

// Session keys. The ids are stored as UUID strings; the slug is kept so the
// dashboard can name the tenant it renders, since tenants are addressed
// publicly by slug and a session only holds the tenant's id otherwise.
const (
	sessionTenantID   = "tenant_id"
	sessionUserID     = "user_id"
	sessionTenantSlug = "tenant_slug"
)

// The messages shared by the error pages, so the same failure reads the same
// wherever it is rendered.
const (
	messageServerError    = "Something went wrong."
	messageFormUnreadable = "The form could not be read."
	messageRateLimited    = "Too many attempts. Try again later."
	messageForbidden      = "You do not have permission to change this."
	messageNoRecord       = "That record does not exist."
	messageTenantGone     = "No business is served at this address."
	messageActiveFlag     = "The active flag must be true or false."
)

// handlerFunc is a handler that runs behind withUser and receives the login
// the session resolves to.
type handlerFunc func(w http.ResponseWriter, r *http.Request, user domain.User)

// withUser resolves the session's login on every request, so a removed login
// loses access immediately and role checks are authoritative. The resolved
// user is passed as an argument, never smuggled through the request context.
func (s *server) withUser(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tenantID, tenantErr := uuid.Parse(s.deps.Sessions.GetString(ctx, sessionTenantID))
		userID, userErr := uuid.Parse(s.deps.Sessions.GetString(ctx, sessionUserID))
		if tenantErr != nil || userErr != nil {
			s.endSession(w, r)
			return
		}
		user, err := s.deps.Auth.CurrentUser(ctx, tenantID, userID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			s.endSession(w, r)
			return
		case err != nil:
			s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
			return
		}
		next(w, r, user)
	}
}

// endSession destroys the session and sends the visitor to the sign-in page.
// It serves POST /app/logout and the sessions withUser can no longer resolve.
// A failed destroy is logged, not surfaced: the redirect is still correct.
func (s *server) endSession(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Sessions.Destroy(r.Context()); err != nil {
		slog.ErrorContext(r.Context(), "destroying session", "err", err)
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// noStore marks every response uncacheable: each page carries a session, a
// login form or a reset token.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// render writes a page. A render failure can only be logged: the response has
// already started by then.
func (s *server) render(w http.ResponseWriter, r *http.Request, page string, data any) {
	if err := s.deps.Views.Render(w, page, data); err != nil {
		slog.ErrorContext(r.Context(), "rendering page", "page", page, "err", err)
	}
}

// pathID parses the {id} path value, rendering the 404 page when it is not a
// UUID. A malformed id, an unknown id and another tenant's id are all the same
// 404: row level security hides the last two, and a 500 would tell them apart.
func (s *server) pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		s.deps.Views.fail(w, r, http.StatusNotFound, messageNoRecord)
		return uuid.Nil, false
	}
	return id, true
}
