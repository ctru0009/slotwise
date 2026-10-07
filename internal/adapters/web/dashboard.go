package web

import (
	"errors"
	"net/http"

	"github.com/ctru0009/slotwise/internal/adapters/web/views"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

// dashboard serves the authenticated overview at GET /app.
func (s *server) dashboard(w http.ResponseWriter, r *http.Request, user domain.User) {
	overview, ok := s.overview(w, r, user)
	if !ok {
		return
	}
	renderPage(r.Context(), w, views.Dashboard(views.DashboardPage{Overview: overview, User: user}))
}

// calendar serves the day or week fragment the dashboard swaps in at GET
// /app/calendar. It renders the component the full page embeds, so the two
// cannot disagree about what a window holds.
func (s *server) calendar(w http.ResponseWriter, r *http.Request, user domain.User) {
	overview, ok := s.overview(w, r, user)
	if !ok {
		return
	}
	renderPage(r.Context(), w, views.Calendar(overview))
}

// renderDashboard re-renders the dashboard with an error message and status
// 200, so a rejected write keeps the form's context instead of redirecting.
func (s *server) renderDashboard(w http.ResponseWriter, r *http.Request, user domain.User, errText string) {
	overview, ok := s.overview(w, r, user)
	if !ok {
		return
	}
	page := views.DashboardPage{Overview: overview, User: user, Error: errText}
	renderPage(r.Context(), w, views.Dashboard(page))
}

// overview loads the window the request names. It renders the failure itself
// and reports whether the caller should continue.
func (s *server) overview(w http.ResponseWriter, r *http.Request, user domain.User) (app.Overview, bool) {
	mode, anchor, ok := calendarRequest(w, r)
	if !ok {
		return app.Overview{}, false
	}
	overview, err := s.deps.Dashboard.Load(r.Context(), user, mode, anchor)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrNotFound):
			fail(w, r, http.StatusNotFound, messageTenantGone)
		case errors.Is(err, domain.ErrInvalidInput):
			fail(w, r, http.StatusBadRequest, validationMessage(err))
		default:
			fail(w, r, http.StatusInternalServerError, messageServerError)
		}
		return app.Overview{}, false
	}
	return overview, true
}

// calendarRequest reads the window a calendar request names: the view mode and
// the local date it is anchored on. A request that names no date gets the
// window holding today.
func calendarRequest(w http.ResponseWriter, r *http.Request) (app.CalendarMode, *domain.LocalDate, bool) {
	mode, err := app.ParseCalendarMode(r.URL.Query().Get("mode"))
	if err != nil {
		fail(w, r, http.StatusBadRequest, validationMessage(err))
		return "", nil, false
	}
	raw := r.URL.Query().Get("anchor")
	if raw == "" {
		return mode, nil, true
	}
	anchor, err := domain.ParseLocalDate(raw)
	if err != nil {
		fail(w, r, http.StatusBadRequest, validationMessage(err))
		return "", nil, false
	}
	return mode, &anchor, true
}

// writeFailure renders the outcome of a failed dashboard write: a validation
// problem re-renders the form with its message, a missing row and a forbidden
// write get their own page, anything else is a server error.
func (s *server) writeFailure(w http.ResponseWriter, r *http.Request, user domain.User, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		s.renderDashboard(w, r, user, validationMessage(err))
	case errors.Is(err, domain.ErrNotFound):
		fail(w, r, http.StatusNotFound, messageNoRecord)
	case errors.Is(err, domain.ErrForbidden):
		fail(w, r, http.StatusForbidden, messageForbidden)
	default:
		fail(w, r, http.StatusInternalServerError, messageServerError)
	}
}

// validationMessage returns the clean "field: message" of a validation
// failure. An unwrapped sentinel falls back to the error's own text; a
// validation error wrapped in more context would otherwise render that
// context too, which reads like a server fault to the person filling the form.
func validationMessage(err error) string {
	var invalid domain.ValidationError
	if errors.As(err, &invalid) {
		return invalid.Error()
	}
	return err.Error()
}
