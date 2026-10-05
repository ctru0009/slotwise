package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/ctru0009/slotwise/internal/domain"
)

// assertValidationError requires err to be a domain.ErrInvalidInput naming
// field.
func assertValidationError(t *testing.T, err error, field string) {
	t.Helper()
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("error = %v, want domain.ErrInvalidInput", err)
	}
	var invalid domain.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want a domain.ValidationError", err)
	}
	if invalid.Field != field {
		t.Errorf("field = %q, want %q", invalid.Field, field)
	}
}

// validService is a ServiceInput every field of which is in bounds.
func validService() ServiceInput {
	return ServiceInput{Name: "Cut and blow dry", DurationMinutes: 30, BufferMinutes: 10, PriceCents: 4_500}
}

// validStaff is a StaffInput every field of which is in bounds.
func validStaff() StaffInput {
	return StaffInput{Name: "Ana", Email: "ana@example.com"}
}

func TestServiceInputValidateAccepts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   ServiceInput
		want ServiceInput
	}{
		{name: "typical", in: validService(), want: validService()},
		{
			name: "name is trimmed",
			in:   ServiceInput{Name: "  Cut  ", DurationMinutes: 1, BufferMinutes: 0, PriceCents: 0},
			want: ServiceInput{Name: "Cut", DurationMinutes: 1, BufferMinutes: 0, PriceCents: 0},
		},
		{
			name: "name at 200 runes",
			in:   ServiceInput{Name: strings.Repeat("é", 200), DurationMinutes: 1440, BufferMinutes: 240, PriceCents: 10_000_000},
			want: ServiceInput{Name: strings.Repeat("é", 200), DurationMinutes: 1440, BufferMinutes: 240, PriceCents: 10_000_000},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.in.validate(); err != nil {
				t.Fatalf("validate() = %v, want nil", err)
			}
			if tt.in != tt.want {
				t.Errorf("normalised input = %#v, want %#v", tt.in, tt.want)
			}
		})
	}
}

func TestServiceInputValidateRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		in    ServiceInput
		field string
	}{
		{name: "empty name", in: ServiceInput{Name: "", DurationMinutes: 30}, field: "name"},
		{name: "blank name", in: ServiceInput{Name: "   ", DurationMinutes: 30}, field: "name"},
		{
			name:  "name at 201 runes",
			in:    ServiceInput{Name: strings.Repeat("a", 201), DurationMinutes: 30},
			field: "name",
		},
		{
			name:  "duration zero",
			in:    ServiceInput{Name: "Cut", DurationMinutes: 0, BufferMinutes: 0, PriceCents: 0},
			field: "duration_minutes",
		},
		{
			name:  "duration above the day",
			in:    ServiceInput{Name: "Cut", DurationMinutes: 1441, BufferMinutes: 0, PriceCents: 0},
			field: "duration_minutes",
		},
		{
			name:  "buffer negative",
			in:    ServiceInput{Name: "Cut", DurationMinutes: 30, BufferMinutes: -1, PriceCents: 0},
			field: "buffer_minutes",
		},
		{
			name:  "buffer above the cap",
			in:    ServiceInput{Name: "Cut", DurationMinutes: 30, BufferMinutes: 241, PriceCents: 0},
			field: "buffer_minutes",
		},
		{
			name:  "price negative",
			in:    ServiceInput{Name: "Cut", DurationMinutes: 30, BufferMinutes: 0, PriceCents: -1},
			field: "price_cents",
		},
		{
			name:  "price above the cap",
			in:    ServiceInput{Name: "Cut", DurationMinutes: 30, BufferMinutes: 0, PriceCents: 10_000_001},
			field: "price_cents",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertValidationError(t, tt.in.validate(), tt.field)
		})
	}
}

func TestStaffInputValidateAccepts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   StaffInput
		want StaffInput
	}{
		{name: "typical", in: validStaff(), want: validStaff()},
		{
			name: "name and email are normalised",
			in:   StaffInput{Name: "  Ana  ", Email: "  Ana@Example.COM  "},
			want: StaffInput{Name: "Ana", Email: "ana@example.com"},
		},
		{
			name: "long name is still within bounds",
			in:   StaffInput{Name: strings.Repeat("a", 200), Email: "ana@example.com"},
			want: StaffInput{Name: strings.Repeat("a", 200), Email: "ana@example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.in.validate(); err != nil {
				t.Fatalf("validate() = %v, want nil", err)
			}
			if tt.in != tt.want {
				t.Errorf("normalised input = %#v, want %#v", tt.in, tt.want)
			}
		})
	}
}

func TestStaffInputValidateRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		in    StaffInput
		field string
	}{
		{name: "empty name", in: StaffInput{Name: "", Email: "ana@example.com"}, field: "name"},
		{name: "blank name", in: StaffInput{Name: "  ", Email: "ana@example.com"}, field: "name"},
		{name: "name at 201 runes", in: StaffInput{Name: strings.Repeat("a", 201), Email: "ana@example.com"}, field: "name"},
		{name: "empty email", in: StaffInput{Name: "Ana", Email: ""}, field: "email"},
		{name: "blank email", in: StaffInput{Name: "Ana", Email: "   "}, field: "email"},
		{name: "malformed email", in: StaffInput{Name: "Ana", Email: "not-an-email"}, field: "email"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertValidationError(t, tt.in.validate(), tt.field)
		})
	}
}
