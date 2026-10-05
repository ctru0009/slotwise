package web

import (
	"errors"
	"net/http"

	"github.com/ctru0009/slotwise/internal/domain"
)

// dashboard serves the authenticated overview at GET /app.
func (s *server) dashboard(w http.ResponseWriter, r *http.Request, user domain.User) {
	page, ok := s.dashboardPage(w, r, user)
	if !ok {
		return
	}
	s.render(w, r, PageDashboard, page)
}

// renderDashboard re-renders the dashboard with an error message and status
// 200, so a rejected write keeps the form's context instead of redirecting.
func (s *server) renderDashboard(w http.ResponseWriter, r *http.Request, user domain.User, errText string) {
	page, ok := s.dashboardPage(w, r, user)
	if !ok {
		return
	}
	page.Error = errText
	s.render(w, r, PageDashboard, page)
}

// dashboardPage loads the business named by the session plus the actor's
// services and staff. It renders the failure itself and reports whether the
// caller should continue.
func (s *server) dashboardPage(w http.ResponseWriter, r *http.Request, user domain.User) (DashboardPage, bool) {
	ctx := r.Context()
	tenant, err := s.deps.Auth.TenantBySlug(ctx, s.deps.Sessions.GetString(ctx, sessionTenantSlug))
	switch {
	case errors.Is(err, domain.ErrTenantNotFound):
		s.deps.Views.fail(w, r, http.StatusNotFound, messageTenantGone)
		return DashboardPage{}, false
	case err != nil:
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
		return DashboardPage{}, false
	}
	services, err := s.deps.Services.List(ctx, user)
	if err != nil {
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
		return DashboardPage{}, false
	}
	staff, err := s.deps.Staff.List(ctx, user)
	if err != nil {
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
		return DashboardPage{}, false
	}
	return DashboardPage{Tenant: tenant, User: user, Services: services, Staff: staff}, true
}

// writeFailure renders the outcome of a failed dashboard write: a validation
// problem re-renders the form with its message, a missing row and a forbidden
// write get their own page, anything else is a server error.
func (s *server) writeFailure(w http.ResponseWriter, r *http.Request, user domain.User, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		s.renderDashboard(w, r, user, validationMessage(err))
	case errors.Is(err, domain.ErrNotFound):
		s.deps.Views.fail(w, r, http.StatusNotFound, messageNoRecord)
	case errors.Is(err, domain.ErrForbidden):
		s.deps.Views.fail(w, r, http.StatusForbidden, messageForbidden)
	default:
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
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
