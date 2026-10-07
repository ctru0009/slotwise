package web

import (
	"errors"
	"net/http"

	"github.com/ctru0009/slotwise/internal/adapters/web/views"
	"github.com/ctru0009/slotwise/internal/domain"
)

// landing serves the public page of one business at GET /b/{slug}: its name and
// the services a customer can book, each linking into the slot search. An
// unknown slug is a 404, the same as everywhere else a slug addresses a
// business.
func (s *server) landing(w http.ResponseWriter, r *http.Request) {
	page, err := s.deps.Public.Landing(r.Context(), r.PathValue("slug"))
	switch {
	case errors.Is(err, domain.ErrTenantNotFound):
		fail(w, r, http.StatusNotFound, messageTenantGone)
		return
	case err != nil:
		fail(w, r, http.StatusInternalServerError, messageServerError)
		return
	}
	renderPage(r.Context(), w, views.Landing(views.LandingPage{
		Tenant:   page.Tenant,
		Services: page.Services,
		Today:    page.Today,
	}))
}
