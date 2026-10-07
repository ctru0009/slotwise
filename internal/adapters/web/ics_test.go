package web

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// unfoldICS parses a calendar file the way a client does: physical lines split
// on CRLF, continuation lines joined back into one content line. It checks the
// encoding rules on the way, so every test that reads a file also asserts them.
func unfoldICS(t *testing.T, raw string) []string {
	t.Helper()
	if !strings.HasSuffix(raw, "\r\n") {
		t.Error("the file does not end with CRLF")
	}
	if stray := strings.ReplaceAll(raw, "\r\n", ""); strings.ContainsAny(stray, "\r\n") {
		t.Errorf("a line break is not CRLF: %q", stray)
	}
	logical := []string{}
	for _, physical := range strings.Split(strings.TrimSuffix(raw, "\r\n"), "\r\n") {
		// The limit is the RFC's 75 octets, spelled out here so raising
		// icsLineOctets cannot make the test agree with the code.
		if len(physical) > 75 {
			t.Errorf("physical line of %d octets exceeds the RFC's 75-octet limit: %q", len(physical), physical)
		}
		if !utf8.ValidString(physical) {
			t.Errorf("physical line is not valid UTF-8: %q", physical)
		}
		if strings.HasPrefix(physical, " ") && len(logical) > 0 {
			logical[len(logical)-1] += strings.TrimPrefix(physical, " ")
			continue
		}
		logical = append(logical, physical)
	}
	return logical
}

// property returns the value of one content line, or "" when the file has no
// such line.
func property(logical []string, name string) string {
	for _, line := range logical {
		if value, ok := strings.CutPrefix(line, name+":"); ok {
			return value
		}
	}
	return ""
}

func testCalendarEvent() calendarEvent {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		panic(err)
	}
	// 03:30 on the day Berlin moves to summer time: the wall clock is two hours
	// ahead of UTC from that morning on, and the appointment occupies forty
	// minutes: a thirty minute service plus its ten minute buffer.
	start := time.Date(2026, time.March, 29, 3, 30, 0, 0, berlin)
	return calendarEvent{
		UID:         calendarUID("2b0e1d0e-0000-4000-8000-000000000001", "http://localhost:8080"),
		Summary:     "Haircut at Salon A",
		Description: "Appointment with Anna",
		Start:       start,
		End:         start.Add(40 * time.Minute),
		Stamp:       time.Date(2026, time.March, 20, 9, 15, 0, 0, time.UTC),
	}
}

// TestRenderCalendarParses pins the structure a strict client needs: one
// VEVENT inside one VCALENDAR, the properties it requires, and the exact
// instants the booking occupies. The span covers duration plus buffer, which is
// the interval the database stores and the one the calendar must block.
func TestRenderCalendarParses(t *testing.T) {
	t.Parallel()

	logical := unfoldICS(t, renderCalendar(testCalendarEvent()))
	assertCalendarStructure(t, logical)
	assertCalendarProperties(t, logical, map[string]string{
		"VERSION":  "2.0",
		"UID":      testCalendarEvent().UID,
		"DTSTAMP":  "20260320T091500Z",
		"DTSTART":  "20260329T013000Z",
		"DTEND":    "20260329T021000Z",
		"SUMMARY":  "Haircut at Salon A",
		"LOCATION": "",
	})
}

// assertCalendarStructure checks the envelope a client needs before it reads
// any property.
func assertCalendarStructure(t *testing.T, logical []string) {
	t.Helper()

	if got := logical[0]; got != "BEGIN:VCALENDAR" {
		t.Errorf("first line = %q, want BEGIN:VCALENDAR", got)
	}
	if got := logical[len(logical)-1]; got != "END:VCALENDAR" {
		t.Errorf("last line = %q, want END:VCALENDAR", got)
	}
	events := 0
	for _, line := range logical {
		if line == "BEGIN:VEVENT" {
			events++
		}
	}
	if events != 1 {
		t.Errorf("the file holds %d events, want 1", events)
	}
	if got := property(logical, "PRODID"); got == "" {
		t.Error("PRODID is missing")
	}
}

// assertCalendarProperties compares the properties that carry the appointment.
func assertCalendarProperties(t *testing.T, logical []string, want map[string]string) {
	t.Helper()

	for name, value := range want {
		if got := property(logical, name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
}

// TestRenderCalendarFoldsWithoutSplittingRunes covers the two ways a long line
// breaks a client: more than 75 octets, or a fold landing inside a multi-byte
// character.
func TestRenderCalendarFoldsWithoutSplittingRunes(t *testing.T) {
	t.Parallel()

	ev := testCalendarEvent()
	ev.Summary = strings.Repeat("ü", 120)
	logical := unfoldICS(t, renderCalendar(ev))
	if got := property(logical, "SUMMARY"); got != ev.Summary {
		t.Errorf("unfolded SUMMARY has %d runes, want %d", len([]rune(got)), len([]rune(ev.Summary)))
	}
}

// TestRenderCalendarEscapesReservedCharacters pins the escaping a client
// reverses: a semicolon, a comma and a backslash inside a name must not read as
// property syntax, and a line break inside one must not write a second line.
func TestRenderCalendarEscapesReservedCharacters(t *testing.T) {
	t.Parallel()

	ev := testCalendarEvent()
	ev.Summary = "Cut, colour; wash \\ dry\nsecond line"
	logical := unfoldICS(t, renderCalendar(ev))
	if got, want := property(logical, "SUMMARY"), `Cut\, colour\; wash \\ dry\nsecond line`; got != want {
		t.Errorf("SUMMARY = %q, want %q", got, want)
	}
	if lines := strings.Count(renderCalendar(ev), "\r\n"); lines != strings.Count(renderCalendar(testCalendarEvent()), "\r\n") {
		t.Errorf("an embedded line break changed the file's line count: %d", lines)
	}
}

// TestRenderCalendarUIDIsStable pins the two properties the client relies on: a
// second download of one booking is the same event, and the id hangs off the
// public host.
func TestRenderCalendarUIDIsStable(t *testing.T) {
	t.Parallel()

	const id = "2b0e1d0e-0000-4000-8000-000000000001"
	if first, second := calendarUID(id, "https://book.example.com"), calendarUID(id, "https://book.example.com"); first != second {
		t.Errorf("UID is not stable: %q then %q", first, second)
	}
	if got, want := calendarUID(id, "https://book.example.com"), id+"@book.example.com"; got != want {
		t.Errorf("UID = %q, want %q", got, want)
	}
	if got, want := calendarUID(id, "not a url"), id+"@slotwise"; got != want {
		t.Errorf("UID without a usable host = %q, want %q", got, want)
	}
}
