package web

import (
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// icsLineOctets is the longest physical line RFC 5545 allows, excluding the
// CRLF that ends it. A continuation line's leading space counts toward it.
const icsLineOctets = 75

// calendarEvent is one appointment as the downloaded calendar file describes
// it. Start and End are instants: the file carries them in UTC, so no viewer
// has to know the business's timezone or trust a timezone table this program
// wrote by hand.
type calendarEvent struct {
	UID         string
	Summary     string
	Description string
	Start       time.Time
	End         time.Time
	Stamp       time.Time
}

// renderCalendar writes one appointment as a complete iCalendar object. Every
// line ends with CRLF, long lines are folded at 75 octets without splitting a
// rune, and the reserved characters of a text value are escaped. The file has
// no trailing content after END:VCALENDAR, which is what makes strict parsers
// accept it.
func renderCalendar(ev calendarEvent) string {
	var out strings.Builder
	text := func(name, value string) {
		out.WriteString(foldICSLine(name + ":" + escapeICSText(value)))
	}
	instant := func(name string, at time.Time) {
		out.WriteString(foldICSLine(name + ":" + icsInstant(at)))
	}

	out.WriteString("BEGIN:VCALENDAR\r\n")
	out.WriteString("VERSION:2.0\r\n")
	out.WriteString("PRODID:-//Slotwise//Booking//EN\r\n")
	out.WriteString("BEGIN:VEVENT\r\n")
	text("UID", ev.UID)
	instant("DTSTAMP", ev.Stamp)
	instant("DTSTART", ev.Start)
	instant("DTEND", ev.End)
	text("SUMMARY", ev.Summary)
	text("DESCRIPTION", ev.Description)
	out.WriteString("END:VEVENT\r\n")
	out.WriteString("END:VCALENDAR\r\n")
	return out.String()
}

// icsInstant formats an instant as the UTC DATE-TIME form RFC 5545 wants, for
// example 20260329T073000Z.
func icsInstant(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// calendarUID names the event for the calendar client. It is derived from the
// booking id, so a second download of the same booking is the same event rather
// than a duplicate, and it hangs off the public host when the configured base
// URL names one.
func calendarUID(bookingID, baseURL string) string {
	host := ""
	if parsed, err := url.Parse(baseURL); err == nil {
		host = parsed.Host
	}
	if host == "" {
		host = "slotwise"
	}
	return bookingID + "@" + host
}

// escapeICSText escapes the characters RFC 5545 reserves inside a text value:
// the backslash, the semicolon, the comma, and the line breaks.
func escapeICSText(value string) string {
	return icsEscaper.Replace(value)
}

var icsEscaper = strings.NewReplacer(
	`\`, `\\`,
	";", `\;`,
	",", `\,`,
	"\r\n", `\n`,
	"\n", `\n`,
	"\r", `\n`,
)

// foldICSLine splits one content line into physical lines of at most 75 octets
// joined by CRLF, each continuation marked with a leading space, and returns
// them with the line's own CRLF. A rune that would straddle the limit moves
// whole to the next line: splitting it would write bytes that are not text.
func foldICSLine(line string) string {
	var out strings.Builder
	for rest := line; ; {
		width := icsLineOctets
		if out.Len() > 0 {
			out.WriteByte(' ')
			width--
		}
		cut := octetPrefix(rest, width)
		out.WriteString(rest[:cut])
		rest = rest[cut:]
		if rest == "" {
			break
		}
		out.WriteString("\r\n")
	}
	out.WriteString("\r\n")
	return out.String()
}

// octetPrefix returns the length of the longest prefix of s that is at most
// limit octets and ends on a rune boundary.
func octetPrefix(s string, limit int) int {
	if len(s) <= limit {
		return len(s)
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		// One rune wider than the limit: emit it whole.
		_, size := utf8.DecodeRuneInString(s)
		return size
	}
	return cut
}
