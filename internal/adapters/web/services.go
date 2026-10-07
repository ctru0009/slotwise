package web

import (
	"net/http"
	"strconv"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

// createService adds a service from the dashboard form.
func (s *server) createService(w http.ResponseWriter, r *http.Request, user domain.User) {
	if err := parseForm(w, r); err != nil {
		fail(w, r, http.StatusBadRequest, messageFormUnreadable)
		return
	}
	in, err := serviceForm(r)
	if err != nil {
		s.writeFailure(w, r, user, err)
		return
	}
	if err := s.deps.Services.Create(r.Context(), user, in); err != nil {
		s.writeFailure(w, r, user, err)
		return
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// updateService replaces the service named by the {id} path value.
func (s *server) updateService(w http.ResponseWriter, r *http.Request, user domain.User) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		fail(w, r, http.StatusBadRequest, messageFormUnreadable)
		return
	}
	in, err := serviceForm(r)
	if err != nil {
		s.writeFailure(w, r, user, err)
		return
	}
	if err := s.deps.Services.Update(r.Context(), user, id, in); err != nil {
		s.writeFailure(w, r, user, err)
		return
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// setServiceActive activates or deactivates the service named by the {id}
// path value.
func (s *server) setServiceActive(w http.ResponseWriter, r *http.Request, user domain.User) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		fail(w, r, http.StatusBadRequest, messageFormUnreadable)
		return
	}
	active, err := parseActive(r)
	if err != nil {
		fail(w, r, http.StatusBadRequest, messageActiveFlag)
		return
	}
	if err := s.deps.Services.SetActive(r.Context(), user, id, active); err != nil {
		s.writeFailure(w, r, user, err)
		return
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// serviceForm reads the service create/update fields.
func serviceForm(r *http.Request) (app.ServiceInput, error) {
	duration, err := formInt(r, "duration_minutes")
	if err != nil {
		return app.ServiceInput{}, err
	}
	buffer, err := formInt(r, "buffer_minutes")
	if err != nil {
		return app.ServiceInput{}, err
	}
	price, err := formInt(r, "price_cents")
	if err != nil {
		return app.ServiceInput{}, err
	}
	return app.ServiceInput{
		Name:            r.FormValue("name"),
		DurationMinutes: duration,
		BufferMinutes:   buffer,
		PriceCents:      price,
	}, nil
}

// parseActive reads the hidden active flag. A missing or unparsable value is
// an error, never a silent deactivation.
func parseActive(r *http.Request) (bool, error) {
	active, err := strconv.ParseBool(r.FormValue("active"))
	if err != nil {
		return false, domain.ValidationError{Field: "active", Message: "must be true or false"}
	}
	return active, nil
}

// formInt reads an optional whole-number field. An empty field is zero, so
// the domain's range rules decide; a present, non-numeric field is an error
// rather than a silent zero.
func formInt(r *http.Request, field string) (int, error) {
	raw := r.FormValue(field)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, domain.ValidationError{Field: field, Message: "must be a whole number"}
	}
	return value, nil
}
