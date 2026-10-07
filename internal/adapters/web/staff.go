package web

import (
	"net/http"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

// createStaff adds a staff member from the dashboard form.
func (s *server) createStaff(w http.ResponseWriter, r *http.Request, user domain.User) {
	if err := parseForm(w, r); err != nil {
		fail(w, r, http.StatusBadRequest, messageFormUnreadable)
		return
	}
	in := staffForm(r)
	if err := s.deps.Staff.Create(r.Context(), user, in); err != nil {
		s.writeFailure(w, r, user, err)
		return
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// updateStaff replaces the staff member named by the {id} path value.
func (s *server) updateStaff(w http.ResponseWriter, r *http.Request, user domain.User) {
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		fail(w, r, http.StatusBadRequest, messageFormUnreadable)
		return
	}
	in := staffForm(r)
	if err := s.deps.Staff.Update(r.Context(), user, id, in); err != nil {
		s.writeFailure(w, r, user, err)
		return
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// setStaffActive activates or deactivates the staff member named by the {id}
// path value.
func (s *server) setStaffActive(w http.ResponseWriter, r *http.Request, user domain.User) {
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
	if err := s.deps.Staff.SetActive(r.Context(), user, id, active); err != nil {
		s.writeFailure(w, r, user, err)
		return
	}
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// staffForm reads the staff create/update fields.
func staffForm(r *http.Request) app.StaffInput {
	return app.StaffInput{Name: r.FormValue("name"), Email: r.FormValue("email")}
}
