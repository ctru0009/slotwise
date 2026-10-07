package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/google/uuid"
)

// minSigningSecretBytes is the shortest signing secret NewSigner accepts. A
// short secret is a guessable one, and the tokens it signs are the only
// credential the public booking routes accept.
const minSigningSecretBytes = 32

// TokenPurpose is the job a signed link does. Each purpose gets its own MAC
// prefix over the same secret, so a token minted for one job cannot be replayed
// as another: a cancel link cannot download a calendar file, and a calendar
// link cannot cancel a booking.
type TokenPurpose string

// The links one booking's secret signs.
const (
	// PurposeCancel authorises the public cancel route.
	PurposeCancel TokenPurpose = "booking-cancel"
	// PurposeCalendar authorises the booking's ICS download.
	PurposeCalendar TokenPurpose = "booking-ics"
)

// Signer mints and verifies the signed links that authorise one booking's
// routes. The tokens are stateless: the booking id is the message, so nothing
// has to be stored to check one, and a token does not expire — the link in a
// confirmation email has to keep working months later.
type Signer struct {
	key []byte
}

// NewSigner returns a Signer keyed by secret, refusing a secret shorter than 32
// bytes.
func NewSigner(secret string) (*Signer, error) {
	if len(secret) < minSigningSecretBytes {
		return nil, errors.New("signing secret must be at least 32 bytes")
	}
	return &Signer{key: []byte(secret)}, nil
}

// Token returns the token that authorises purpose for the booking with id.
func (s *Signer) Token(purpose TokenPurpose, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(s.mac(purpose, id))
}

// Verify reports whether token authorises purpose for the booking with id.
func (s *Signer) Verify(purpose TokenPurpose, id uuid.UUID, token string) bool {
	got, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	return hmac.Equal(got, s.mac(purpose, id))
}

// mac is the HMAC-SHA256 over the purpose, a separator and the booking id.
func (s *Signer) mac(purpose TokenPurpose, id uuid.UUID) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(purpose))
	mac.Write([]byte(":"))
	mac.Write(id[:])
	return mac.Sum(nil)
}
