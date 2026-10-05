package domain_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ctru0009/slotwise/internal/domain"
)

// fuzzLocations are the zones the property test searches: the first two have a
// fixed offset, so the brute-force oracle applies there too; the rest exercise
// DST transitions.
var fuzzLocations = []string{
	"UTC",
	"Asia/Kathmandu",
	"America/New_York",
	"Europe/Berlin",
	"Australia/Lord_Howe",
	"America/Santiago",
}

// FuzzSlotTimes checks the engine invariants on bounded random searches, and,
// in a fixed-offset zone, that the emitted set equals a brute-force oracle that
// walks every wall minute of the range.
func FuzzSlotTimes(f *testing.F) {
	f.Add(uint8(0), uint16(10), uint8(3), uint64(1), uint8(45), uint64(2), int16(0))
	f.Add(uint8(1), uint16(365), uint8(31), uint64(7), uint8(30), uint64(9), int16(1440))
	f.Add(uint8(2), uint16(60), uint8(2), uint64(11), uint8(120), uint64(13), int16(240))
	f.Add(uint8(3), uint16(90), uint8(1), uint64(17), uint8(15), uint64(19), int16(-60))
	f.Add(uint8(5), uint16(250), uint8(7), uint64(23), uint8(60), uint64(29), int16(2880))

	f.Fuzz(func(t *testing.T, locIdx uint8, dayOffset uint16, spanDays uint8, ruleSeed uint64, blockMin uint8, panelSeed uint64, notBeforeShift int16) {
		name := fuzzLocations[int(locIdx)%len(fuzzLocations)]
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatalf("loading location %s: %v", name, err)
		}

		from := domain.LocalDate{Year: 2026, Month: time.January, Day: 1}
		for range int(dayOffset % 730) {
			from = from.Next()
		}
		to := from
		for range int(spanDays % 63) {
			to = to.Next()
		}

		block := time.Duration(1+int(blockMin)%120) * time.Minute
		windowStart := domain.DayStart(from, loc)
		query := domain.SlotQuery{
			Location:  loc,
			Rules:     fuzzRules(ruleSeed),
			From:      from,
			To:        to,
			Block:     block,
			NotBefore: windowStart.Add(time.Duration(int(notBeforeShift)%5760-1440) * time.Minute),
			Busy:      fuzzIntervals(panelSeed, windowStart),
			TimeOff:   fuzzIntervals(panelSeed^0x5deece66d, windowStart),
		}

		got := domain.SlotTimes(query)
		assertSlotProperties(t, query, got)

		if int(locIdx)%len(fuzzLocations) < 2 {
			oracle := bruteForceSlotTimes(query)
			if !slices.EqualFunc(got, oracle, func(a, b time.Time) bool { return a.Equal(b) }) {
				t.Fatalf("SlotTimes =\n  %v\noracle\n  %v\nfor %+v", got, oracle, query)
			}
		}
	})
}

// fuzzRandom derives bounded test data from a seed. It is a splitmix64
// generator, not crypto randomness: the inputs only need to be varied and
// reproducible.
type fuzzRandom struct{ state uint64 }

func (r *fuzzRandom) next() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// intN returns a value in [0, n) for the small, positive bounds the callers
// pass; the modulo makes both conversions value-safe.
func (r *fuzzRandom) intN(n int) int {
	return int(r.next() % uint64(n)) //nolint:gosec // the result is below n, well inside int
}

// fuzzRules builds up to three valid weekly rules from a seed.
func fuzzRules(seed uint64) []domain.WeeklyRule {
	rng := fuzzRandom{state: seed}
	rules := make([]domain.WeeklyRule, 0, 3)
	for range rng.intN(4) {
		start := rng.intN(1440)
		rules = append(rules, domain.WeeklyRule{
			Weekday:     time.Weekday(rng.intN(7)),
			StartMinute: start,
			EndMinute:   start + 1 + rng.intN(1440-start),
		})
	}
	return rules
}

// fuzzIntervals builds up to three intervals straddling a window of the
// calendar, so blocked spans overlap candidates from before and after.
func fuzzIntervals(seed uint64, windowStart time.Time) []domain.Interval {
	rng := fuzzRandom{state: seed}
	intervals := make([]domain.Interval, 0, 3)
	for range rng.intN(4) {
		start := windowStart.Add(time.Duration(rng.intN(4*24*60)-24*60) * time.Minute)
		intervals = append(intervals, domain.Interval{
			Start: start,
			End:   start.Add(time.Duration(1+rng.intN(120)) * time.Minute),
		})
	}
	return intervals
}

// assertSlotProperties checks the invariants every result must satisfy:
// ascending and unique, never before NotBefore, round-tripping in the zone, on
// the block grid of a matching rule whose whole block fits, and clear of every
// busy and time-off interval.
func assertSlotProperties(t *testing.T, q domain.SlotQuery, got []time.Time) {
	t.Helper()
	if got == nil {
		t.Fatal("SlotTimes returned nil, want a non-nil slice")
	}
	blockMin := int(q.Block / time.Minute)
	for i, slot := range got {
		if slot.Before(q.NotBefore) {
			t.Fatalf("slot %v is before NotBefore %v", slot, q.NotBefore)
		}
		if !roundTrips(slot, q.Location) {
			t.Fatalf("slot %v does not round-trip in %s", slot, q.Location)
		}
		if i > 0 && !got[i-1].Before(slot) {
			t.Fatalf("slots %v and %v are not strictly ascending and unique", got[i-1], slot)
		}
		y, m, d := slot.Date()
		day := domain.LocalDate{Year: y, Month: m, Day: d}
		if q.From.After(day) || day.After(q.To) {
			t.Fatalf("slot %v falls on %s, outside [%s, %s]", slot, day, q.From, q.To)
		}
		wall := slot.Hour()*60 + slot.Minute()
		if !onBlockGrid(day, wall, blockMin, q.Rules) {
			t.Fatalf("slot %v is not on the block grid of any matching rule for %s", slot, day)
		}
		candidate := domain.Interval{Start: slot, End: slot.Add(q.Block)}
		if overlapsAnyInterval(candidate, q.Busy) || overlapsAnyInterval(candidate, q.TimeOff) {
			t.Fatalf("slot %v overlaps a busy or time-off interval", slot)
		}
	}
}

// onBlockGrid reports whether wall minute m of day is a grid start of a rule
// that matches day and whose whole block fits, using plain wall arithmetic
// instead of the engine's stepping.
func onBlockGrid(day domain.LocalDate, m, blockMin int, rules []domain.WeeklyRule) bool {
	for _, rule := range rules {
		if rule.Weekday != day.Weekday() {
			continue
		}
		if m < rule.StartMinute || m+blockMin > rule.EndMinute {
			continue
		}
		if (m-rule.StartMinute)%blockMin == 0 {
			return true
		}
	}
	return false
}

// overlapsAnyInterval is the independent interval compare: half-open overlap,
// written out instead of reusing the interval type's method.
func overlapsAnyInterval(candidate domain.Interval, intervals []domain.Interval) bool {
	for _, o := range intervals {
		if candidate.Start.Before(o.End) && o.Start.Before(candidate.End) {
			return true
		}
	}
	return false
}

// bruteForceSlotTimes applies the spec directly to every wall minute of every
// local date in the range. It is only valid in a fixed-offset zone, where every
// wall time exists exactly once.
func bruteForceSlotTimes(q domain.SlotQuery) []time.Time {
	blockMin := int(q.Block / time.Minute)
	starts := []time.Time{}
	for d := q.From; !d.After(q.To); d = d.Next() {
		for m := range 1440 {
			if !onBlockGrid(d, m, blockMin, q.Rules) {
				continue
			}
			slot := time.Date(d.Year, d.Month, d.Day, m/60, m%60, 0, 0, q.Location)
			if slot.Before(q.NotBefore) {
				continue
			}
			candidate := domain.Interval{Start: slot, End: slot.Add(q.Block)}
			if overlapsAnyInterval(candidate, q.Busy) || overlapsAnyInterval(candidate, q.TimeOff) {
				continue
			}
			starts = append(starts, slot)
		}
	}
	slices.SortFunc(starts, func(a, b time.Time) int { return a.Compare(b) })
	return slices.CompactFunc(starts, time.Time.Equal)
}
