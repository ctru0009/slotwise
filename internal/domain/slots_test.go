package domain_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ctru0009/slotwise/internal/domain"
)

// inst builds a UTC instant for the expected-slot lists.
func inst(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func location(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("loading location %s: %v", name, err)
	}
	return loc
}

func localDate(t *testing.T, s string) domain.LocalDate {
	t.Helper()
	d, err := domain.ParseLocalDate(s)
	if err != nil {
		t.Fatalf("parsing date %s: %v", s, err)
	}
	return d
}

func assertTimes(t *testing.T, got, want []time.Time) {
	t.Helper()
	if got == nil {
		t.Fatal("SlotTimes returned nil, want a non-nil slice")
	}
	if !slices.EqualFunc(got, want, func(a, b time.Time) bool { return a.Equal(b) }) {
		t.Fatalf("SlotTimes =\n  %v\nwant\n  %v", got, want)
	}
}

// roundTrips reports whether t reads as the same wall time it was built from,
// the invariant every emitted slot must satisfy in its zone.
func roundTrips(t time.Time, loc *time.Location) bool {
	y, m, d := t.Date()
	h, mi, s := t.Clock()
	return s == 0 && time.Date(y, m, d, h, mi, 0, 0, loc).Equal(t)
}

func TestParseLocalDate(t *testing.T) {
	t.Parallel()

	parsed, err := domain.ParseLocalDate("2026-01-05")
	if err != nil {
		t.Fatalf("ParseLocalDate: %v", err)
	}
	if parsed != (domain.LocalDate{Year: 2026, Month: time.January, Day: 5}) {
		t.Errorf("ParseLocalDate = %+v, want 2026-01-05", parsed)
	}
	if parsed.String() != "2026-01-05" {
		t.Errorf("String = %q, want %q", parsed.String(), "2026-01-05")
	}
	if got := parsed.Weekday(); got != time.Monday {
		t.Errorf("Weekday = %v, want Monday", got)
	}
	if got, err := domain.ParseLocalDate("2026-11-01"); err != nil || got.Weekday() != time.Sunday {
		t.Errorf("2026-11-01 = %v, %v, want a Sunday", got, err)
	}
}

func TestParseLocalDateRejects(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"2026-02-30", "2026-13-01", "2026-01-32", "not-a-date", "2026-1-5", ""} {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			got, err := domain.ParseLocalDate(in)
			if err == nil {
				t.Fatalf("ParseLocalDate(%q) = %v, want an error", in, got)
			}
			var invalid domain.ValidationError
			if !errors.As(err, &invalid) || invalid.Field != "date" {
				t.Errorf("ParseLocalDate(%q) = %v, want a ValidationError naming date", in, err)
			}
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Errorf("ParseLocalDate(%q) = %v, want it to match ErrInvalidInput", in, err)
			}
		})
	}
}

func TestLocalDateValid(t *testing.T) {
	t.Parallel()

	if (domain.LocalDate{Year: 2026, Month: time.February, Day: 29}).Valid() {
		t.Error("2026-02-29 is valid, want it rejected: 2026 is not a leap year")
	}
	if !(domain.LocalDate{Year: 2024, Month: time.February, Day: 29}).Valid() {
		t.Error("2024-02-29 is invalid, want it accepted")
	}
	if (domain.LocalDate{Year: 2026, Month: time.January, Day: 0}).Valid() {
		t.Error("day 0 is valid, want it rejected")
	}
}

func TestLocalDateArithmetic(t *testing.T) {
	t.Parallel()

	next := []struct {
		from string
		want string
	}{
		{"2026-01-05", "2026-01-06"},
		{"2026-01-31", "2026-02-01"},
		{"2026-12-31", "2027-01-01"},
		{"2024-02-28", "2024-02-29"},
	}
	for _, tt := range next {
		if got := localDate(t, tt.from).Next().String(); got != tt.want {
			t.Errorf("%s.Next() = %s, want %s", tt.from, got, tt.want)
		}
	}

	if !localDate(t, "2026-01-05").After(localDate(t, "2026-01-04")) {
		t.Error("Jan 5 is not after Jan 4")
	}
	if localDate(t, "2026-01-05").After(localDate(t, "2026-01-05")) {
		t.Error("Jan 5 is after itself")
	}
	if localDate(t, "2026-01-05").After(localDate(t, "2026-01-06")) {
		t.Error("Jan 5 is after Jan 6")
	}
}

func TestIntervalOverlaps(t *testing.T) {
	t.Parallel()
	base := inst(2026, time.January, 5, 9, 0)

	tests := []struct {
		name string
		a, b domain.Interval
		want bool
	}{
		{"identical", domain.Interval{base, base.Add(time.Hour)}, domain.Interval{base, base.Add(time.Hour)}, true},
		{"contained", domain.Interval{base, base.Add(time.Hour)}, domain.Interval{base.Add(10 * time.Minute), base.Add(20 * time.Minute)}, true},
		{"partial", domain.Interval{base, base.Add(time.Hour)}, domain.Interval{base.Add(30 * time.Minute), base.Add(90 * time.Minute)}, true},
		{"touching at the end", domain.Interval{base, base.Add(time.Hour)}, domain.Interval{base.Add(time.Hour), base.Add(2 * time.Hour)}, false},
		{"touching at the start", domain.Interval{base.Add(time.Hour), base.Add(2 * time.Hour)}, domain.Interval{base, base.Add(time.Hour)}, false},
		{"disjoint", domain.Interval{base, base.Add(time.Hour)}, domain.Interval{base.Add(3 * time.Hour), base.Add(4 * time.Hour)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.a.Overlaps(tt.b); got != tt.want {
				t.Errorf("Overlaps = %v, want %v", got, tt.want)
			}
			if got := tt.b.Overlaps(tt.a); got != tt.want {
				t.Errorf("Overlaps reversed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWeeklyRuleValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rule domain.WeeklyRule
		want bool
	}{
		{"monday morning", domain.WeeklyRule{Weekday: time.Monday, StartMinute: 540, EndMinute: 720}, true},
		{"sunday midnight", domain.WeeklyRule{Weekday: time.Sunday, StartMinute: 0, EndMinute: 1}, true},
		{"all day", domain.WeeklyRule{Weekday: time.Saturday, StartMinute: 0, EndMinute: 1440}, true},
		{"zero length", domain.WeeklyRule{Weekday: time.Monday, StartMinute: 600, EndMinute: 600}, false},
		{"end before start", domain.WeeklyRule{Weekday: time.Monday, StartMinute: 600, EndMinute: 540}, false},
		{"start past the day", domain.WeeklyRule{Weekday: time.Monday, StartMinute: 1440, EndMinute: 1441}, false},
		{"negative start", domain.WeeklyRule{Weekday: time.Monday, StartMinute: -1, EndMinute: 60}, false},
		{"end past midnight", domain.WeeklyRule{Weekday: time.Monday, StartMinute: 0, EndMinute: 1441}, false},
		{"weekday out of range", domain.WeeklyRule{Weekday: 7, StartMinute: 0, EndMinute: 60}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.rule.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSlotTimesTable(t *testing.T) {
	t.Parallel()
	never := inst(2000, time.January, 1, 0, 0)
	monday := localDate(t, "2026-01-05")

	allDay := make([]time.Time, 0, 24)
	for h := range 24 {
		allDay = append(allDay, inst(2026, time.January, 5, h, 0))
	}

	tests := []struct {
		name  string
		query domain.SlotQuery
		want  []time.Time
	}{
		{
			name: "block fits at the window edge",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 720}},
				From:      monday,
				To:        monday,
				Block:     90 * time.Minute,
				NotBefore: never,
			},
			want: []time.Time{inst(2026, time.January, 5, 9, 0), inst(2026, time.January, 5, 10, 30)},
		},
		{
			name: "window shorter than the block",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}},
				From:      monday,
				To:        monday,
				Block:     90 * time.Minute,
				NotBefore: never,
			},
			want: []time.Time{},
		},
		{
			name:  "no rules yields an empty slice",
			query: domain.SlotQuery{Location: time.UTC, From: monday, To: monday, Block: 30 * time.Minute, NotBefore: never},
			want:  []time.Time{},
		},
		{
			name: "non-positive block yields an empty slice",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}},
				From:      monday,
				To:        monday,
				Block:     0,
				NotBefore: never,
			},
			want: []time.Time{},
		},
		{
			name: "sub-minute block yields an empty slice",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}},
				From:      monday,
				To:        monday,
				Block:     90 * time.Second,
				NotBefore: never,
			},
			want: []time.Time{},
		},
		{
			name: "rules for another weekday are ignored",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Tuesday, StartMinute: 540, EndMinute: 600}},
				From:      monday,
				To:        monday,
				Block:     30 * time.Minute,
				NotBefore: never,
			},
			want: []time.Time{},
		},
		{
			name: "only matching weekdays over a range",
			query: domain.SlotQuery{
				Location: time.UTC,
				Rules: []domain.WeeklyRule{
					{Weekday: time.Monday, StartMinute: 540, EndMinute: 600},
					{Weekday: time.Wednesday, StartMinute: 540, EndMinute: 600},
				},
				From:      monday,
				To:        localDate(t, "2026-01-07"),
				Block:     30 * time.Minute,
				NotBefore: never,
			},
			want: []time.Time{
				inst(2026, time.January, 5, 9, 0), inst(2026, time.January, 5, 9, 30),
				inst(2026, time.January, 7, 9, 0), inst(2026, time.January, 7, 9, 30),
			},
		},
		{
			name: "from after to is empty",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}},
				From:      localDate(t, "2026-01-06"),
				To:        monday,
				Block:     30 * time.Minute,
				NotBefore: never,
			},
			want: []time.Time{},
		},
		{
			name: "busy blocks a candidate it overlaps",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}},
				From:      monday,
				To:        monday,
				Block:     30 * time.Minute,
				NotBefore: never,
				Busy:      []domain.Interval{{Start: inst(2026, time.January, 5, 9, 30), End: inst(2026, time.January, 5, 10, 0)}},
			},
			want: []time.Time{inst(2026, time.January, 5, 9, 0)},
		},
		{
			name: "half-open touching on both sides does not block",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}},
				From:      monday,
				To:        monday,
				Block:     30 * time.Minute,
				NotBefore: never,
				Busy: []domain.Interval{
					{Start: inst(2026, time.January, 5, 8, 0), End: inst(2026, time.January, 5, 9, 0)},
					{Start: inst(2026, time.January, 5, 10, 0), End: inst(2026, time.January, 5, 10, 30)},
				},
			},
			want: []time.Time{inst(2026, time.January, 5, 9, 0), inst(2026, time.January, 5, 9, 30)},
		},
		{
			name: "off-grid busy drops only the candidates it covers",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}},
				From:      monday,
				To:        monday,
				Block:     30 * time.Minute,
				NotBefore: never,
				Busy:      []domain.Interval{{Start: inst(2026, time.January, 5, 9, 10), End: inst(2026, time.January, 5, 9, 20)}},
			},
			want: []time.Time{inst(2026, time.January, 5, 9, 30)},
		},
		{
			name: "time off covering the whole day leaves nothing",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 720}},
				From:      monday,
				To:        monday,
				Block:     60 * time.Minute,
				NotBefore: never,
				TimeOff:   []domain.Interval{{Start: inst(2026, time.January, 5, 0, 0), End: inst(2026, time.January, 6, 0, 0)}},
			},
			want: []time.Time{},
		},
		{
			name: "partial time off drops the candidates it covers",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 720}},
				From:      monday,
				To:        monday,
				Block:     60 * time.Minute,
				NotBefore: never,
				TimeOff:   []domain.Interval{{Start: inst(2026, time.January, 5, 10, 0), End: inst(2026, time.January, 5, 11, 0)}},
			},
			want: []time.Time{inst(2026, time.January, 5, 9, 0), inst(2026, time.January, 5, 11, 0)},
		},
		{
			name: "not before is inclusive",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 720}},
				From:      monday,
				To:        monday,
				Block:     60 * time.Minute,
				NotBefore: inst(2026, time.January, 5, 10, 0),
			},
			want: []time.Time{inst(2026, time.January, 5, 10, 0), inst(2026, time.January, 5, 11, 0)},
		},
		{
			name: "duplicate rules emit one slot per instant",
			query: domain.SlotQuery{
				Location: time.UTC,
				Rules: []domain.WeeklyRule{
					{Weekday: time.Monday, StartMinute: 540, EndMinute: 600},
					{Weekday: time.Monday, StartMinute: 540, EndMinute: 600},
				},
				From:      monday,
				To:        monday,
				Block:     30 * time.Minute,
				NotBefore: never,
			},
			want: []time.Time{inst(2026, time.January, 5, 9, 0), inst(2026, time.January, 5, 9, 30)},
		},
		{
			name: "overlapping rules keep their distinct starts",
			query: domain.SlotQuery{
				Location: time.UTC,
				Rules: []domain.WeeklyRule{
					{Weekday: time.Monday, StartMinute: 540, EndMinute: 600},
					{Weekday: time.Monday, StartMinute: 555, EndMinute: 615},
				},
				From:      monday,
				To:        monday,
				Block:     30 * time.Minute,
				NotBefore: never,
			},
			want: []time.Time{
				inst(2026, time.January, 5, 9, 0), inst(2026, time.January, 5, 9, 15),
				inst(2026, time.January, 5, 9, 30), inst(2026, time.January, 5, 9, 45),
			},
		},
		{
			name: "all-day rule emits every grid start",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 0, EndMinute: 1440}},
				From:      monday,
				To:        monday,
				Block:     60 * time.Minute,
				NotBefore: never,
			},
			want: allDay,
		},
		{
			name: "without buffer the last start sits at the window edge",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 660}},
				From:      monday,
				To:        monday,
				Block:     30 * time.Minute,
				NotBefore: never,
			},
			want: []time.Time{
				inst(2026, time.January, 5, 9, 0), inst(2026, time.January, 5, 9, 30),
				inst(2026, time.January, 5, 10, 0), inst(2026, time.January, 5, 10, 30),
			},
		},
		{
			name: "buffer pushes the last start earlier",
			query: domain.SlotQuery{
				Location:  time.UTC,
				Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 660}},
				From:      monday,
				To:        monday,
				Block:     45 * time.Minute,
				NotBefore: never,
			},
			want: []time.Time{inst(2026, time.January, 5, 9, 0), inst(2026, time.January, 5, 9, 45)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertTimes(t, domain.SlotTimes(tt.query), tt.want)
		})
	}
}

func TestSlotTimesDSTSpringForward(t *testing.T) {
	t.Parallel()
	never := inst(2000, time.January, 1, 0, 0)

	t.Run("New York spring forward drops the gap", func(t *testing.T) {
		t.Parallel()
		got := domain.SlotTimes(domain.SlotQuery{
			Location:  location(t, "America/New_York"),
			Rules:     []domain.WeeklyRule{{Weekday: time.Sunday, StartMinute: 60, EndMinute: 240}},
			From:      localDate(t, "2026-03-08"),
			To:        localDate(t, "2026-03-08"),
			Block:     30 * time.Minute,
			NotBefore: never,
		})
		assertTimes(t, got, []time.Time{
			inst(2026, time.March, 8, 6, 0), inst(2026, time.March, 8, 6, 30),
			inst(2026, time.March, 8, 7, 0), inst(2026, time.March, 8, 7, 30),
		})
	})

	t.Run("Berlin spring forward drops the whole window", func(t *testing.T) {
		t.Parallel()
		berlin := location(t, "Europe/Berlin")
		got := domain.SlotTimes(domain.SlotQuery{
			Location:  berlin,
			Rules:     []domain.WeeklyRule{{Weekday: time.Sunday, StartMinute: 120, EndMinute: 240}},
			From:      localDate(t, "2026-03-29"),
			To:        localDate(t, "2026-03-29"),
			Block:     30 * time.Minute,
			NotBefore: never,
		})
		assertTimes(t, got, []time.Time{
			inst(2026, time.March, 29, 1, 0), inst(2026, time.March, 29, 1, 30),
		})

		empty := domain.SlotTimes(domain.SlotQuery{
			Location:  berlin,
			Rules:     []domain.WeeklyRule{{Weekday: time.Sunday, StartMinute: 120, EndMinute: 180}},
			From:      localDate(t, "2026-03-29"),
			To:        localDate(t, "2026-03-29"),
			Block:     30 * time.Minute,
			NotBefore: never,
		})
		assertTimes(t, empty, []time.Time{})
	})

	t.Run("Lord Howe half-hour gap drops 02:00", func(t *testing.T) {
		t.Parallel()
		got := domain.SlotTimes(domain.SlotQuery{
			Location:  location(t, "Australia/Lord_Howe"),
			Rules:     []domain.WeeklyRule{{Weekday: time.Sunday, StartMinute: 90, EndMinute: 210}},
			From:      localDate(t, "2026-10-04"),
			To:        localDate(t, "2026-10-04"),
			Block:     30 * time.Minute,
			NotBefore: never,
		})
		assertTimes(t, got, []time.Time{
			inst(2026, time.October, 3, 15, 0), inst(2026, time.October, 3, 15, 30),
			inst(2026, time.October, 3, 16, 0),
		})
	})

	t.Run("Santiago skipped midnight", func(t *testing.T) {
		t.Parallel()
		santiago := location(t, "America/Santiago")
		got := domain.SlotTimes(domain.SlotQuery{
			Location:  santiago,
			Rules:     []domain.WeeklyRule{{Weekday: time.Sunday, StartMinute: 0, EndMinute: 120}},
			From:      localDate(t, "2026-09-06"),
			To:        localDate(t, "2026-09-06"),
			Block:     30 * time.Minute,
			NotBefore: never,
		})
		assertTimes(t, got, []time.Time{
			inst(2026, time.September, 6, 4, 0), inst(2026, time.September, 6, 4, 30),
		})
	})

	t.Run("Kathmandu quarter-hour offset", func(t *testing.T) {
		t.Parallel()
		got := domain.SlotTimes(domain.SlotQuery{
			Location:  location(t, "Asia/Kathmandu"),
			Rules:     []domain.WeeklyRule{{Weekday: time.Monday, StartMinute: 540, EndMinute: 600}},
			From:      localDate(t, "2026-11-02"),
			To:        localDate(t, "2026-11-02"),
			Block:     30 * time.Minute,
			NotBefore: never,
		})
		assertTimes(t, got, []time.Time{
			inst(2026, time.November, 2, 3, 15), inst(2026, time.November, 2, 3, 45),
		})
	})
}

// TestSlotTimesDSTFallBack pins the ambiguous-time rules: a wall time that
// happens twice is emitted exactly once, on whichever pass time.Date picks, so
// these cases assert invariants and the unambiguous neighbours instead of the
// pass.
func TestSlotTimesDSTFallBack(t *testing.T) {
	t.Parallel()
	never := inst(2000, time.January, 1, 0, 0)

	ny := location(t, "America/New_York")
	assertNewYorkFallBack(t, ny, domain.SlotTimes(domain.SlotQuery{
		Location:  ny,
		Rules:     []domain.WeeklyRule{{Weekday: time.Sunday, StartMinute: 60, EndMinute: 120}},
		From:      localDate(t, "2026-11-01"),
		To:        localDate(t, "2026-11-01"),
		Block:     30 * time.Minute,
		NotBefore: never,
	}))

	lord := location(t, "Australia/Lord_Howe")
	assertLordHoweRepeat(t, lord, domain.SlotTimes(domain.SlotQuery{
		Location:  lord,
		Rules:     []domain.WeeklyRule{{Weekday: time.Sunday, StartMinute: 60, EndMinute: 150}},
		From:      localDate(t, "2026-04-05"),
		To:        localDate(t, "2026-04-05"),
		Block:     30 * time.Minute,
		NotBefore: never,
	}))
}

// assertNewYorkFallBack checks the two repeated wall times of 2026-11-01:
// exactly two slots, one per wall time, ascending, round-tripping, and each
// either the EDT or the EST pass of 01:00/01:30.
func assertNewYorkFallBack(t *testing.T, loc *time.Location, got []time.Time) {
	t.Helper()
	if len(got) != 2 {
		t.Fatalf("SlotTimes = %v, want exactly two slots for the two wall times", got)
	}
	if !got[0].Before(got[1]) {
		t.Errorf("SlotTimes = %v, want ascending", got)
	}
	walls := []string{got[0].Format("15:04"), got[1].Format("15:04")}
	if walls[0] != "01:00" || walls[1] != "01:30" {
		t.Errorf("slot wall times = %v, want one slot per wall time, 01:00 and 01:30", walls)
	}
	for _, slot := range got {
		if !roundTrips(slot, loc) {
			t.Errorf("slot %v does not round-trip in %s", slot, loc)
		}
	}
	wantFirst := got[0].Equal(inst(2026, time.November, 1, 5, 0)) || got[0].Equal(inst(2026, time.November, 1, 6, 0))
	wantSecond := got[1].Equal(inst(2026, time.November, 1, 5, 30)) || got[1].Equal(inst(2026, time.November, 1, 6, 30))
	if !wantFirst || !wantSecond {
		t.Errorf("SlotTimes = %v, want 01:00/01:30 local in either pass", got)
	}
}

// assertLordHoweRepeat checks the 2026-04-05 fall back, where only the half
// hour between 01:30 and 02:00 repeats: three slots, the once-only wall times
// pinned, the middle one an acceptable pass of 01:30, ascending and unique.
func assertLordHoweRepeat(t *testing.T, loc *time.Location, got []time.Time) {
	t.Helper()
	if len(got) != 3 {
		t.Fatalf("SlotTimes = %v, want three slots", got)
	}
	if !got[0].Equal(inst(2026, time.April, 4, 14, 0)) || !got[2].Equal(inst(2026, time.April, 4, 15, 30)) {
		t.Errorf("SlotTimes = %v, want the once-only wall times pinned and the repeat in the middle", got)
	}
	if !got[1].Equal(inst(2026, time.April, 4, 14, 30)) && !got[1].Equal(inst(2026, time.April, 4, 15, 0)) {
		t.Errorf("middle slot = %v, want either pass of 01:30 local", got[1])
	}
	for i, slot := range got {
		if !roundTrips(slot, loc) {
			t.Errorf("slot %d (%v) does not round-trip in %s", i, slot, loc)
		}
		if i > 0 && !got[i-1].Before(slot) {
			t.Errorf("SlotTimes = %v, want strictly ascending", got)
		}
	}
}

func TestDayStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		date string
		zone string
		want time.Time
	}{
		{"New York spring forward day", "2026-03-08", "America/New_York", inst(2026, time.March, 8, 5, 0)},
		{"New York fall back day", "2026-11-01", "America/New_York", inst(2026, time.November, 1, 4, 0)},
		{"Berlin spring forward day", "2026-03-29", "Europe/Berlin", inst(2026, time.March, 28, 23, 0)},
		{"Santiago day before the skip", "2026-09-05", "America/Santiago", inst(2026, time.September, 5, 4, 0)},
		{"Santiago skipped midnight", "2026-09-06", "America/Santiago", inst(2026, time.September, 6, 4, 0)},
		{"Kathmandu quarter-hour offset", "2026-11-02", "Asia/Kathmandu", inst(2026, time.November, 1, 18, 15)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loc := location(t, tt.zone)
			d := localDate(t, tt.date)
			got := domain.DayStart(d, loc)
			if !got.Equal(tt.want) {
				t.Errorf("DayStart(%s, %s) = %v, want %v", d, tt.zone, got.UTC(), tt.want)
			}
			y, m, day := got.Date()
			if dayOf := (domain.LocalDate{Year: y, Month: m, Day: day}); dayOf != d {
				t.Errorf("DayStart(%s, %s) reads back as %s, want the same local date", d, tt.zone, dayOf)
			}
		})
	}

	// Santiago's skipped midnight means the day starts at 01:00 local, the
	// first instant that exists.
	santiago := location(t, "America/Santiago")
	got := domain.DayStart(localDate(t, "2026-09-06"), santiago)
	if h, m, _ := got.Clock(); h != 1 || m != 0 {
		t.Errorf("DayStart(2026-09-06, Santiago) = %v local, want 01:00", got.Format("15:04"))
	}
}
