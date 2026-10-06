package domain_test

import (
	"testing"
	"time"

	"github.com/ctru0009/slotwise/internal/domain"
)

// TestServiceBlockIncludesTheBuffer pins the occupied length a booking stores:
// duration plus buffer. A block that is only the appointment would let the slot
// engine offer a start inside the buffer, which the exclusion constraint alone
// cannot catch.
func TestServiceBlockIncludesTheBuffer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		service domain.Service
		want    time.Duration
	}{
		{name: "duration only", service: domain.Service{DurationMinutes: 30}, want: 30 * time.Minute},
		{name: "duration and buffer", service: domain.Service{DurationMinutes: 30, BufferMinutes: 10}, want: 40 * time.Minute},
		{name: "zero buffer", service: domain.Service{DurationMinutes: 15, BufferMinutes: 0}, want: 15 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.service.Block(); got != tt.want {
				t.Errorf("Block() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBookingStatusValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status domain.BookingStatus
		want   bool
	}{
		{status: domain.BookingConfirmed, want: true},
		{status: domain.BookingCancelled, want: true},
		{status: domain.BookingStatus("pending"), want: false},
		{status: domain.BookingStatus(""), want: false},
	}
	for _, tt := range tests {
		if got := tt.status.Valid(); got != tt.want {
			t.Errorf("BookingStatus(%q).Valid() = %v, want %v", tt.status, got, tt.want)
		}
	}
}
