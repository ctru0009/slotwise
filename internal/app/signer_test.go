package app

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSignerRoundTrip(t *testing.T) {
	t.Parallel()
	signer, err := NewSigner(bookingSecret)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	id := uuid.New()
	token := signer.Token(PurposeCancel, id)
	if token == "" {
		t.Fatal("Token returned an empty string")
	}
	if !signer.Verify(PurposeCancel, id, token) {
		t.Error("Verify rejected the token Token minted")
	}
	if signer.Verify(PurposeCancel, uuid.New(), token) {
		t.Error("Verify accepted one booking's token for another booking")
	}
	if signer.Verify(PurposeCancel, id, "") {
		t.Error("Verify accepted an empty token")
	}
	if signer.Verify(PurposeCancel, id, "%%%") {
		t.Error("Verify accepted a token that is not base64")
	}
	// The token is the raw MAC: a one-character change must not verify, and
	// neither must a prefix of it.
	if signer.Verify(PurposeCancel, id, token[:len(token)-1]) {
		t.Error("Verify accepted a truncated token")
	}
	other, err := NewSigner(strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	if other.Verify(PurposeCancel, id, token) {
		t.Error("a different secret verified this signer's token")
	}
}

func TestNewCancelSignerRejectsShortSecrets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		secret string
		want   bool
	}{
		{name: "empty", secret: "", want: false},
		{name: "one byte short", secret: strings.Repeat("x", 31), want: false},
		{name: "exactly 32 bytes", secret: strings.Repeat("x", 32), want: true},
		{name: "longer", secret: bookingSecret, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewSigner(tt.secret); (err == nil) != tt.want {
				t.Errorf("NewSigner(%d bytes) error = %v, want accepted = %v", len(tt.secret), err, tt.want)
			}
		})
	}
}

// TestSignerPurposesDoNotInterchange pins that the purpose is part of what the
// MAC covers: a cancel link cannot download the calendar file, and a calendar
// link cannot cancel the booking.
func TestSignerPurposesDoNotInterchange(t *testing.T) {
	t.Parallel()
	signer, err := NewSigner(bookingSecret)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	id := uuid.New()
	cancel := signer.Token(PurposeCancel, id)
	calendar := signer.Token(PurposeCalendar, id)

	if cancel == calendar {
		t.Fatal("Token minted the same token for PurposeCancel and PurposeCalendar")
	}
	if !signer.Verify(PurposeCancel, id, cancel) {
		t.Error("Verify rejected the token PurposeCancel minted")
	}
	if !signer.Verify(PurposeCalendar, id, calendar) {
		t.Error("Verify rejected the token PurposeCalendar minted")
	}
	if signer.Verify(PurposeCalendar, id, cancel) {
		t.Error("a cancel token verified as a calendar token")
	}
	if signer.Verify(PurposeCancel, id, calendar) {
		t.Error("a calendar token verified as a cancel token")
	}
}
