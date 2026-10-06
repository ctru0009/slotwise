package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/google/uuid"
)

// cancelTokenPrefix separates the cancel MAC from any other use of the same
// secret, so a token minted here cannot be replayed as something else.
const cancelTokenPrefix = "booking-cancel:"

// minCancelSecretBytes is the shortest signing secret NewCancelSigner accepts.
// A short secret is a guessable one, and the tokens it signs are the only
// credential the public cancel route accepts.
const minCancelSecretBytes = 32

// CancelSigner mints and verifies the signed link that authorises one booking's
// cancellation. The token is stateless — the booking id is the message — so
// nothing has to be stored to check one.
type CancelSigner struct {
	key []byte
}

// NewCancelSigner returns a signer keyed by secret, refusing a secret shorter
// than 32 bytes.
func NewCancelSigner(secret string) (*CancelSigner, error) {
	if len(secret) < minCancelSecretBytes {
		return nil, errors.New("cancel secret must be at least 32 bytes")
	}
	return &CancelSigner{key: []byte(secret)}, nil
}

// Token returns the cancel token for one booking id.
func (s *CancelSigner) Token(id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(s.mac(id))
}

// Verify reports whether token authorises cancelling the booking with id.
func (s *CancelSigner) Verify(id uuid.UUID, token string) bool {
	got, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	return hmac.Equal(got, s.mac(id))
}

// mac is the HMAC-SHA256 over the prefix and the booking id.
func (s *CancelSigner) mac(id uuid.UUID) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(cancelTokenPrefix))
	mac.Write(id[:])
	return mac.Sum(nil)
}
