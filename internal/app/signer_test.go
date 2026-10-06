package app

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCancelSignerRoundTrip(t *testing.T) {
	t.Parallel()
	signer, err := NewCancelSigner(bookingSecret)
	if err != nil {
		t.Fatalf("NewCancelSigner: %v", err)
	}

	id := uuid.New()
	token := signer.Token(id)
	if token == "" {
		t.Fatal("Token returned an empty string")
	}
	if !signer.Verify(id, token) {
		t.Error("Verify rejected the token Token minted")
	}
	if signer.Verify(uuid.New(), token) {
		t.Error("Verify accepted one booking's token for another booking")
	}
	if signer.Verify(id, "") {
		t.Error("Verify accepted an empty token")
	}
	if signer.Verify(id, "%%%") {
		t.Error("Verify accepted a token that is not base64")
	}
	// The token is the raw MAC: a one-character change must not verify, and
	// neither must a prefix of it.
	if signer.Verify(id, token[:len(token)-1]) {
		t.Error("Verify accepted a truncated token")
	}
	other, err := NewCancelSigner(strings.Repeat("x", 32))
	if err != nil {
		t.Fatalf("NewCancelSigner: %v", err)
	}
	if other.Verify(id, token) {
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
			if _, err := NewCancelSigner(tt.secret); (err == nil) != tt.want {
				t.Errorf("NewCancelSigner(%d bytes) error = %v, want accepted = %v", len(tt.secret), err, tt.want)
			}
		})
	}
}
