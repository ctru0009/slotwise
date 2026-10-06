package web

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

// The messages the public booking pages render.
const (
	messageBadService     = "That does not look like a service."
	messageSlotTaken      = "That time was just taken. Pick another one."
	messageIdempotencyKey = "The booking could not be identified. Try again."
	messageCancelLink     = "That cancel link is not valid."
)

// slots serves the public slot list at GET /b/{slug}/slots?service=&from=&to=.
// A missing to defaults to from, so one day is the common case.
func (s *server) slots(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenantOr404(w, r)
	if !ok {
		return
	}
	serviceID, err := uuid.Parse(r.URL.Query().Get("service"))
	if err != nil {
		s.deps.Views.fail(w, r, http.StatusBadRequest, messageBadService)
		return
	}
	from, err := domain.ParseLocalDate(r.URL.Query().Get("from"))
	if err != nil {
		s.deps.Views.fail(w, r, http.StatusBadRequest, validationMessage(err))
		return
	}
	to := from
	if raw := r.URL.Query().Get("to"); raw != "" {
		if to, err = domain.ParseLocalDate(raw); err != nil {
			s.deps.Views.fail(w, r, http.StatusBadRequest, validationMessage(err))
			return
		}
	}
	// One search is a whole day of grid for every staff member, so the route
	// is throttled per business before any of that work.
	if !s.deps.SlotSearches.Allow(tenant.ID.String(), s.deps.Clock.Now()) {
		s.deps.Views.fail(w, r, http.StatusTooManyRequests, messageRateLimited)
		return
	}
	slots, err := s.deps.Availability.Search(r.Context(), tenant.ID, serviceID, from, to)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		s.deps.Views.fail(w, r, http.StatusNotFound, messageNoRecord)
		return
	case errors.Is(err, domain.ErrInvalidInput):
		s.deps.Views.fail(w, r, http.StatusBadRequest, validationMessage(err))
		return
	case err != nil:
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
		return
	}

	loc, ok := s.tenantLocation(w, r, tenant)
	if !ok {
		return
	}
	views := make([]SlotView, 0, len(slots))
	for _, slot := range slots {
		views = append(views, SlotView{
			StaffID:        slot.StaffID,
			StartsAt:       slot.Start,
			IdempotencyKey: newIdempotencyKey(),
		})
	}
	s.render(w, r, PageSlots, SlotsPage{
		Tenant:    tenant,
		Location:  loc,
		ServiceID: serviceID,
		From:      from.String(),
		To:        to.String(),
		Slots:     views,
	})
}

// createBooking books one slot at POST /b/{slug}/bookings and sends the
// customer to the page the cancel link points at, so a redirect (or a reload of
// that page) never books twice.
func (s *server) createBooking(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenantOr404(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		s.deps.Views.fail(w, r, http.StatusBadRequest, messageFormUnreadable)
		return
	}
	in, err := bookingForm(r)
	if err != nil {
		s.deps.Views.fail(w, r, http.StatusBadRequest, validationMessage(err))
		return
	}
	// The key is what makes a retry safe, so a request without one is refused
	// rather than booked unchecked.
	if in.IdempotencyKey == "" {
		s.deps.Views.fail(w, r, http.StatusBadRequest, messageIdempotencyKey)
		return
	}
	// Two buckets guard the public write: one per business, which no request can
	// rotate, and one per customer account, which matches the login forms. Both
	// are a guardrail against a runaway client, not a denial-of-service defence:
	// a distributed attacker with many businesses and addresses is bounded at
	// the edge, which is M7's job.
	if !s.deps.BookingWrites.Allow(tenant.ID.String(), s.deps.Clock.Now()) {
		s.deps.Views.fail(w, r, http.StatusTooManyRequests, messageRateLimited)
		return
	}
	if !s.deps.BookingPosts.Allow(accountKey(tenant.ID, in.CustomerEmail), s.deps.Clock.Now()) {
		s.deps.Views.fail(w, r, http.StatusTooManyRequests, messageRateLimited)
		return
	}
	booking, err := s.deps.Bookings.Create(r.Context(), tenant.ID, in)
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		s.deps.Views.fail(w, r, http.StatusBadRequest, validationMessage(err))
		return
	case errors.Is(err, domain.ErrNotFound):
		s.deps.Views.fail(w, r, http.StatusNotFound, messageNoRecord)
		return
	case errors.Is(err, domain.ErrSlotTaken):
		s.deps.Views.fail(w, r, http.StatusConflict, messageSlotTaken)
		return
	case err != nil:
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
		return
	}
	// Every piece of the target is escaped on the way in, so the redirect can
	// only ever land on a booking page under this business.
	token := s.deps.Bookings.CancelToken(booking.ID)
	target := bookingURL(url.PathEscape(tenant.Slug), url.PathEscape(booking.ID.String()), url.QueryEscape(token))
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// bookingPage serves the booking a signed link names at GET
// /b/{slug}/bookings/{id}?token=….
func (s *server) bookingPage(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenantOr404(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	token := r.URL.Query().Get("token")
	booking, err := s.deps.Bookings.Get(r.Context(), tenant.ID, id, token)
	switch {
	case errors.Is(err, domain.ErrForbidden):
		s.deps.Views.fail(w, r, http.StatusForbidden, messageCancelLink)
		return
	case errors.Is(err, domain.ErrNotFound):
		s.deps.Views.fail(w, r, http.StatusNotFound, messageNoRecord)
		return
	case err != nil:
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
		return
	}
	loc, ok := s.tenantLocation(w, r, tenant)
	if !ok {
		return
	}
	s.render(w, r, PageBooking, BookingPage{Tenant: tenant, Location: loc, Booking: booking, Token: token})
}

// cancelBooking cancels the booking a signed link names at POST
// /b/{slug}/bookings/{id}/cancel and redirects back to the page, which then
// shows the cancelled booking. Cancelling twice redirects the same way.
func (s *server) cancelBooking(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.tenantOr404(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r)
	if !ok {
		return
	}
	if err := parseForm(w, r); err != nil {
		s.deps.Views.fail(w, r, http.StatusBadRequest, messageFormUnreadable)
		return
	}
	token := r.FormValue("token")
	err := s.deps.Bookings.Cancel(r.Context(), tenant.ID, id, token)
	switch {
	case errors.Is(err, domain.ErrForbidden):
		s.deps.Views.fail(w, r, http.StatusForbidden, messageCancelLink)
	case errors.Is(err, domain.ErrNotFound):
		s.deps.Views.fail(w, r, http.StatusNotFound, messageNoRecord)
	case err != nil:
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
	default:
		// The id comes from the path and the token from the form, so both are
		// escaped: the target is still a booking page under this business.
		target := bookingURL(url.PathEscape(tenant.Slug), url.PathEscape(id.String()), url.QueryEscape(token))
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}

// bookingForm reads the booking fields. The key comes from the Idempotency-Key
// header, which a script or a retry library can set, and falls back to the
// hidden form field, which is all a plain HTML form can carry.
func bookingForm(r *http.Request) (app.BookingInput, error) {
	serviceID, err := formUUID(r, "service_id")
	if err != nil {
		return app.BookingInput{}, err
	}
	staffID, err := formUUID(r, "staff_id")
	if err != nil {
		return app.BookingInput{}, err
	}
	startsAt, err := formTime(r, "starts_at")
	if err != nil {
		return app.BookingInput{}, err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = r.FormValue("idempotency_key")
	}
	return app.BookingInput{
		ServiceID:      serviceID,
		StaffID:        staffID,
		StartsAt:       startsAt,
		CustomerName:   r.FormValue("customer_name"),
		CustomerEmail:  r.FormValue("customer_email"),
		IdempotencyKey: key,
	}, nil
}

// formUUID reads a required UUID field.
func formUUID(r *http.Request, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.FormValue(field))
	if err != nil {
		return uuid.Nil, domain.ValidationError{Field: field, Message: "must be a UUID"}
	}
	return id, nil
}

// formTime reads a required RFC3339 instant.
func formTime(r *http.Request, field string) (time.Time, error) {
	at, err := time.Parse(time.RFC3339, r.FormValue(field))
	if err != nil {
		return time.Time{}, domain.ValidationError{Field: field, Message: "must be an RFC3339 timestamp"}
	}
	return at, nil
}

// bookingURL is where a booking is shown: the cancel link's target carrying its
// token, so the page it lands on can offer the cancel form. Every piece arrives
// already escaped, which is what keeps the target from carrying a second path
// segment, a second query parameter or a fragment.
func bookingURL(slug, id, token string) string {
	return "/b/" + slug + "/bookings/" + id + "?token=" + token
}

// newIdempotencyKey mints the key one rendered booking form submits.
func newIdempotencyKey() string {
	return uuid.NewString()
}

// tenantLocation loads the tenant's timezone for rendering. A tenant whose
// timezone does not load is a server-side problem, not a page the customer can
// fix.
func (s *server) tenantLocation(w http.ResponseWriter, r *http.Request, tenant domain.Tenant) (*time.Location, bool) {
	loc, err := time.LoadLocation(tenant.Timezone)
	if err != nil {
		s.deps.Views.fail(w, r, http.StatusInternalServerError, messageServerError)
		return nil, false
	}
	return loc, true
}
