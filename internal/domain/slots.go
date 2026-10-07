package domain

import (
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// LocalDate is a calendar date in a tenant's timezone, not an instant. An
// instant typed from/to silently searches the wrong day when local midnight is
// skipped, so the search range travels as wall dates instead.
type LocalDate struct {
	Year  int
	Month time.Month
	Day   int
}

// ParseLocalDate parses a YYYY-MM-DD calendar date. Anything else, including
// dates the calendar does not have such as February 30, is a ValidationError
// naming the date field.
func ParseLocalDate(s string) (LocalDate, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return LocalDate{}, ValidationError{Field: "date", Message: "must be a real calendar date in YYYY-MM-DD form"}
	}
	d := LocalDate{Year: t.Year(), Month: t.Month(), Day: t.Day()}
	if !d.Valid() {
		return LocalDate{}, ValidationError{Field: "date", Message: "must be a real calendar date in YYYY-MM-DD form"}
	}
	return d, nil
}

// Valid reports whether d is a date the calendar has. The fields round-trip
// through time.Date, so February 30 and month 0 are rejected.
func (d LocalDate) Valid() bool {
	t := d.atUTC()
	return t.Year() == d.Year && t.Month() == d.Month && t.Day() == d.Day
}

// Next returns the calendar date one day after d.
func (d LocalDate) Next() LocalDate {
	t := d.atUTC().AddDate(0, 0, 1)
	return LocalDate{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

// AddDays returns the calendar date n days after d, or n days before it when n
// is negative.
func (d LocalDate) AddDays(n int) LocalDate {
	t := d.atUTC().AddDate(0, 0, n)
	return LocalDate{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

// After reports whether d is later than o.
func (d LocalDate) After(o LocalDate) bool {
	return d.compare(o) > 0
}

// Equal reports whether d and o are the same calendar date.
func (d LocalDate) Equal(o LocalDate) bool {
	return d.compare(o) == 0
}

// Weekday returns the day of week d falls on. It is a calendar fact, so no
// timezone is needed.
func (d LocalDate) Weekday() time.Weekday {
	return d.atUTC().Weekday()
}

// String formats d as YYYY-MM-DD.
func (d LocalDate) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

func (d LocalDate) compare(o LocalDate) int {
	switch {
	case d.Year != o.Year:
		return d.Year - o.Year
	case d.Month != o.Month:
		return int(d.Month - o.Month)
	default:
		return d.Day - o.Day
	}
}

func (d LocalDate) atUTC() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

// Interval is a half-open absolute range [Start, End).
type Interval struct {
	Start time.Time
	End   time.Time
}

// Overlaps reports whether i and o share an instant. Half-open ranges that only
// touch, one ending exactly where the other starts, do not overlap.
func (i Interval) Overlaps(o Interval) bool {
	return i.Start.Before(o.End) && o.Start.Before(i.End)
}

// WeeklyRule is one recurring local-time window of a staff member's week.
type WeeklyRule struct {
	Weekday     time.Weekday // 0 = Sunday, the same value as EXTRACT(DOW) and time.Weekday
	StartMinute int          // wall minute of day, 0..1439
	EndMinute   int          // exclusive, 1..1440; 1440 is local midnight
}

// Valid reports whether r is a window the database checks accept and the
// engine can step through.
func (r WeeklyRule) Valid() bool {
	return r.Weekday >= time.Sunday && r.Weekday <= time.Saturday &&
		r.StartMinute >= 0 && r.StartMinute <= 1439 &&
		r.EndMinute >= 1 && r.EndMinute <= 1440 &&
		r.StartMinute < r.EndMinute
}

// TimeOff is one absence row.
type TimeOff struct {
	ID       uuid.UUID
	StaffID  uuid.UUID
	StartsAt time.Time
	EndsAt   time.Time
}

// StaffSchedule is one staff member's schedule for a search.
type StaffSchedule struct {
	Staff   Staff
	Rules   []WeeklyRule
	TimeOff []Interval
	Busy    []Interval
}

// SlotSnapshot is the store's answer for one search: the active service and the
// active staff.
type SlotSnapshot struct {
	Service Service
	Staff   []StaffSchedule
}

// Slot is one bookable start for one staff member, with the name the pages
// show: a customer cannot read a staff id.
type Slot struct {
	StaffID   uuid.UUID
	StaffName string
	Start     time.Time
}

// SlotQuery is the engine input for one staff member.
type SlotQuery struct {
	Location  *time.Location
	Rules     []WeeklyRule
	Busy      []Interval
	TimeOff   []Interval
	From, To  LocalDate
	Block     time.Duration // duration + buffer, a positive whole number of minutes
	NotBefore time.Time
}

// SlotTimes returns the bookable starts for one staff member: ascending,
// unique, never nil. Interval checks are half-open, so a booking ending exactly
// at a candidate start does not block it. Windows are not split at DST
// transitions.
func SlotTimes(q SlotQuery) []time.Time {
	starts := []time.Time{}
	if q.Block < time.Minute || q.Block%time.Minute != 0 {
		return starts
	}
	block := int(q.Block / time.Minute)
	for d := q.From; !d.After(q.To); d = d.Next() {
		for _, rule := range q.Rules {
			if rule.Weekday != d.Weekday() {
				continue
			}
			for m := rule.StartMinute; m+block <= rule.EndMinute; m += block {
				t, ok := wallTime(d, m, q.Location)
				if !ok || t.Before(q.NotBefore) {
					continue
				}
				candidate := Interval{Start: t, End: t.Add(q.Block)}
				if overlapsAny(candidate, q.Busy) || overlapsAny(candidate, q.TimeOff) {
					continue
				}
				starts = append(starts, t)
			}
		}
	}
	slices.SortFunc(starts, func(a, b time.Time) int { return a.Compare(b) })
	return slices.CompactFunc(starts, time.Time.Equal)
}

// DayStart returns the first instant of local date d in loc. When local
// midnight is nonexistent (America/Santiago 2026-09-06) it returns the day's
// first existing instant.
func DayStart(d LocalDate, loc *time.Location) time.Time {
	t := time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc)
	for range 24 * 60 {
		y, m, day := t.Date()
		local := LocalDate{Year: y, Month: m, Day: day}
		if local.compare(d) >= 0 {
			break
		}
		t = t.Add(time.Minute)
	}
	return t
}

// wallTime builds the instant for wall minute m of date d and reports whether
// the instant reads back as the same wall fields in loc. Nonexistent wall times
// (a spring-forward gap) fail the round-trip and must not be emitted.
func wallTime(d LocalDate, m int, loc *time.Location) (time.Time, bool) {
	hh, mm := m/60, m%60
	t := time.Date(d.Year, d.Month, d.Day, hh, mm, 0, 0, loc)
	y, mo, day := t.Date()
	h, mi, _ := t.Clock()
	if y != d.Year || mo != d.Month || day != d.Day || h != hh || mi != mm {
		return time.Time{}, false
	}
	return t, true
}

func overlapsAny(i Interval, intervals []Interval) bool {
	for _, o := range intervals {
		if i.Overlaps(o) {
			return true
		}
	}
	return false
}
