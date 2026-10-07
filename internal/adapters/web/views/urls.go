package views

import (
	"net/url"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

// The query parameters one calendar link carries. A mode and a local date are
// all the calendar needs to render a window, so a link stays shareable and a
// reload lands on the same week.
const (
	modeParam   = "mode"
	anchorParam = "anchor"
)

// stepDays is how far one toolbar step moves the window. The window already
// knows the answer: it holds one local day in day mode and seven in week mode,
// so a step is exactly the window's own length.
func stepDays(window app.CalendarWindow) int {
	return len(window.Days)
}

// calendarQuery builds the query string for one window.
func calendarQuery(mode app.CalendarMode, anchor domain.LocalDate) string {
	query := url.Values{}
	query.Set(modeParam, string(mode))
	query.Set(anchorParam, anchor.String())
	return "?" + query.Encode()
}

// calendarFragmentURL is the address of the calendar and booking list on their
// own, which is what htmx fetches and swaps in.
func calendarFragmentURL(mode app.CalendarMode, anchor domain.LocalDate) string {
	return "/app/calendar" + calendarQuery(mode, anchor)
}

// dashboardURL is the address of the whole page for one window, which is what
// htmx pushes into the address bar: a reload then renders the full page with
// the same window instead of a bare fragment.
func dashboardURL(mode app.CalendarMode, anchor domain.LocalDate) string {
	return "/app" + calendarQuery(mode, anchor)
}

// cancelPath is the form target that cancels one booking. The token travels in
// the form body, not the query. Every part of the path is escaped, so a slug
// cannot add a segment of its own.
func cancelPath(slug, id string) string {
	return "/b/" + url.PathEscape(slug) + "/bookings/" + url.PathEscape(id) + "/cancel"
}

// calendarPath is the signed download of one booking's calendar file.
func calendarPath(slug, id, token string) string {
	return "/b/" + url.PathEscape(slug) + "/bookings/" + url.PathEscape(id) + "/ics?token=" + url.QueryEscape(token)
}

// slotSearchPath is the public slot search for one service, starting on a local
// date. It is what a landing-page service links to.
func slotSearchPath(slug string, serviceID string, from domain.LocalDate) string {
	query := url.Values{}
	query.Set("service", serviceID)
	query.Set("from", from.String())
	return "/b/" + url.PathEscape(slug) + "/slots?" + query.Encode()
}
