package views

import (
	"fmt"
	"time"
)

// money renders an integer number of cents as a decimal amount, for example
// 1234 as "12.34" and -5 as "-0.05". It stays in integer arithmetic so no
// rounding can creep in.
func money(cents int) string {
	whole, frac := cents/100, cents%100
	if frac < 0 {
		frac = -frac
	}
	if whole == 0 && cents < 0 {
		return fmt.Sprintf("-0.%02d", frac)
	}
	return fmt.Sprintf("%d.%02d", whole, frac)
}

// localTime renders an instant on a tenant's clock, so a page reads in the
// timezone the business works in. A nil location renders in UTC.
func localTime(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("Mon 2 Jan 2006 15:04 MST")
}

// localDay renders an instant as the calendar date it falls on in loc.
func localDay(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("Mon 2 Jan")
}

// localClock renders an instant as the wall time it falls on in loc.
func localClock(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("15:04")
}
