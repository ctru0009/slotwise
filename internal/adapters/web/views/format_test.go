package views

import (
	"testing"
	"time"
)

// TestMoney pins the price rendering, including the sign handling of amounts
// below one unit, where the whole part is zero.
func TestMoney(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cents int
		want  string
	}{
		{name: "a whole amount", cents: 1234, want: "12.34"},
		{name: "zero", cents: 0, want: "0.00"},
		{name: "one cent", cents: 1, want: "0.01"},
		{name: "just below a unit", cents: 99, want: "0.99"},
		{name: "exactly a unit", cents: 100, want: "1.00"},
		{name: "negative below a unit", cents: -5, want: "-0.05"},
		{name: "negative just above a unit", cents: -99, want: "-0.99"},
		{name: "negative unit", cents: -100, want: "-1.00"},
		{name: "negative whole amount", cents: -1234, want: "-12.34"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := money(tt.cents); got != tt.want {
				t.Errorf("money(%d) = %q, want %q", tt.cents, got, tt.want)
			}
		})
	}
}

// TestLocalTime pins that an instant renders on the tenant's clock, and that a
// page without a tenant falls back to UTC rather than panicking.
func TestLocalTime(t *testing.T) {
	t.Parallel()
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatalf("loading Europe/Berlin: %v", err)
	}
	instant := time.Date(2026, time.July, 15, 15, 4, 0, 0, time.UTC)
	tests := []struct {
		name string
		loc  *time.Location
		want string
	}{
		{name: "tenant clock", loc: berlin, want: "Wed 15 Jul 2026 17:04 CEST"},
		{name: "nil location renders UTC", loc: nil, want: "Wed 15 Jul 2026 15:04 UTC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := localTime(instant, tt.loc); got != tt.want {
				t.Errorf("localTime(%s, %v) = %q, want %q", instant, tt.loc, got, tt.want)
			}
		})
	}
}
