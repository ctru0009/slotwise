package app

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/ctru0009/slotwise/internal/domain"
)

// testPassword passes the policy and is short enough to keep Argon2id cheap.
const testPassword = "correct-horse-battery"

func TestHashPasswordRoundTrip(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(hash, testPassword) {
		t.Error("VerifyPassword rejected the password that produced the hash")
	}
	if VerifyPassword(hash, "wrong-horse-battery") {
		t.Error("VerifyPassword accepted a wrong password")
	}
}

func TestHashPasswordUsesFreshSalt(t *testing.T) {
	t.Parallel()
	first, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Error("two hashes of the same password are identical, so the salt is not random")
	}
	if !VerifyPassword(first, testPassword) || !VerifyPassword(second, testPassword) {
		t.Error("a freshly hashed password failed to verify")
	}
}

func TestHashPasswordEncodesPinnedParameters(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	parts := strings.Split(hash, "$")
	if len(parts) != 6 {
		t.Fatalf("PHC string %q has %d segments, want 6", hash, len(parts))
	}
	if parts[1] != "argon2id" || parts[2] != "v=19" {
		t.Errorf("algorithm/version = %q/%q, want argon2id/v=19", parts[1], parts[2])
	}
	if parts[3] != "m=19456,t=2,p=1" {
		t.Errorf("cost parameters = %q, want m=19456,t=2,p=1", parts[3])
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatalf("salt is not unpadded base64: %v", err)
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		t.Fatalf("key is not unpadded base64: %v", err)
	}
	if len(salt) != 16 || len(key) != 32 {
		t.Errorf("salt/key length = %d/%d, want 16/32", len(salt), len(key))
	}
}

func TestHashPasswordEnforcesBytePolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		plain   string
		wantErr bool
	}{
		{name: "empty", plain: "", wantErr: true},
		{name: "eleven bytes", plain: strings.Repeat("a", 11), wantErr: true},
		{name: "twelve bytes", plain: strings.Repeat("a", 12)},
		{name: "128 bytes", plain: strings.Repeat("a", 128)},
		{name: "129 bytes", plain: strings.Repeat("a", 129), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hash, err := HashPassword(tt.plain)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("HashPassword(%d bytes): %v", len(tt.plain), err)
				}
				if !VerifyPassword(hash, tt.plain) {
					t.Error("hash does not verify")
				}
				return
			}
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("HashPassword(%d bytes) error = %v, want domain.ErrInvalidInput", len(tt.plain), err)
			}
			var invalid domain.ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want a domain.ValidationError", err)
			}
			if invalid.Field != "password" {
				t.Errorf("field = %q, want %q", invalid.Field, "password")
			}
			if hash != "" {
				t.Errorf("HashPassword returned hash %q alongside its error", hash)
			}
		})
	}
}

func TestVerifyPasswordRejectsForeignEncodings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		encoded string
	}{
		{name: "empty"},
		{name: "not a hash", encoded: "hunter2"},
		{name: "wrong algorithm", encoded: "$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$a2V5"},
		{name: "wrong version", encoded: "$argon2id$v=16$m=19456,t=2,p=1$c2FsdA$a2V5"},
		{name: "missing segments", encoded: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA"},
		{name: "zero iterations", encoded: "$argon2id$v=19$m=19456,t=0,p=1$c2FsdA$a2V5"},
		{name: "zero threads", encoded: "$argon2id$v=19$m=19456,t=2,p=0$c2FsdA$a2V5"},
		{name: "absurd memory", encoded: "$argon2id$v=19$m=4000000000,t=2,p=1$c2FsdA$a2V5"},
		{name: "undecodable salt", encoded: "$argon2id$v=19$m=19456,t=2,p=1$***$a2V5"},
		{name: "empty salt", encoded: "$argon2id$v=19$m=19456,t=2,p=1$$a2V5"},
		{name: "short key", encoded: "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$a2V5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if VerifyPassword(tt.encoded, testPassword) {
				t.Errorf("VerifyPassword accepted %q", tt.encoded)
			}
		})
	}
}

func TestDummyHashMatchesPinnedParameters(t *testing.T) {
	t.Parallel()
	if !VerifyPassword(dummyPasswordHash, "slotwise-dummy-password") {
		t.Fatal("dummyPasswordHash does not verify the password it was built from")
	}
	if VerifyPassword(dummyPasswordHash, "another-password") {
		t.Fatal("dummyPasswordHash verified an unrelated password")
	}
	parts := strings.Split(dummyPasswordHash, "$")
	if len(parts) != 6 || parts[3] != "m=19456,t=2,p=1" {
		t.Fatalf("dummy hash %q does not carry the pinned parameters", dummyPasswordHash)
	}
}
