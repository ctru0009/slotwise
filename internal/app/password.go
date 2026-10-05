package app

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/ctru0009/slotwise/internal/domain"
)

// Argon2id parameters, pinned so a hash written today keeps verifying after a
// later release changes a default. m=19456 KiB, t=2, p=1 is the OWASP
// recommendation for interactive logins.
const (
	argonVersion    = argon2.Version
	argonMemoryKiB  = 19456
	argonIterations = 2
	argonThreads    = 1
	argonSaltBytes  = 16
	argonKeyBytes   = 32

	// The bounds a stored hash may ask verification for. They stop a corrupted
	// or foreign row from making VerifyPassword allocate gigabytes, and keep
	// the values inside the range x/crypto/argon2 accepts without panicking.
	maxArgonMemoryKiB  = 1 << 20
	maxArgonIterations = 10

	minPasswordBytes = 12
	maxPasswordBytes = 128
)

// dummyPasswordHash is a PHC string with the pinned parameters. Authenticate
// verifies it when no login exists, so an unknown email costs exactly one
// Argon2id verification like a wrong password does. Its salt and key were
// generated once and are deliberately not secret: the constant exists only to
// be hashed against, and never authenticates anyone.
//
//nolint:gosec // a deliberately public decoy hash, not a credential
const dummyPasswordHash = "$argon2id$v=19$m=19456,t=2,p=1$c2xvdHdpc2UtZHVtbXkxIQ$tgR+TYmFcYavzmwPtCs/WHSQuMjqv924crqCijOdWu8"

// HashPassword hashes plain with Argon2id and returns a PHC string carrying
// the salt and cost parameters needed to verify it later.
func HashPassword(plain string) (string, error) {
	if err := checkPasswordPolicy("password", plain); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating password salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, argonIterations, argonMemoryKiB, argonThreads, argonKeyBytes)
	return encodePasswordHash(salt, key), nil
}

// VerifyPassword reports whether plain produced encoded. A malformed or
// foreign encoding reports false instead of an error, so a broken stored hash
// is a failed login and never a crash.
func VerifyPassword(encoded, plain string) bool {
	parsed, ok := decodePasswordHash(encoded)
	if !ok {
		return false
	}
	got := argon2.IDKey([]byte(plain), parsed.salt, parsed.params.iterations,
		parsed.params.memoryKiB, parsed.params.threads, argonKeyBytes)
	return subtle.ConstantTimeCompare(got, parsed.key) == 1
}

// checkPasswordPolicy enforces the byte-length policy the hasher and the reset
// flow share, naming field in the failure so a form can point at the right
// input.
func checkPasswordPolicy(field, plain string) error {
	switch {
	case len(plain) < minPasswordBytes:
		return domain.ValidationError{Field: field, Message: "must be at least 12 bytes"}
	case len(plain) > maxPasswordBytes:
		return domain.ValidationError{Field: field, Message: "must be at most 128 bytes"}
	}
	return nil
}

// argonParams are the cost parameters a PHC string carries.
type argonParams struct {
	memoryKiB  uint32
	iterations uint32
	threads    uint8
}

// parsedHash is a decoded PHC string.
type parsedHash struct {
	params argonParams
	salt   []byte
	key    []byte
}

// encodePasswordHash renders salt and key as
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<key>, both halves in unpadded
// standard base64 as the PHC format specifies.
func encodePasswordHash(salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argonVersion,
		argonMemoryKiB, argonIterations, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
}

// decodePasswordHash parses a PHC argon2id string, rejecting anything that is
// not one: a wrong algorithm or version, undecodable halves, or parameters
// outside the bounds argon2 itself accepts.
func decodePasswordHash(encoded string) (parsedHash, bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argonVersion) {
		return parsedHash{}, false
	}
	params, ok := parseArgonParams(parts[3])
	if !ok {
		return parsedHash{}, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return parsedHash{}, false
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) != argonKeyBytes {
		return parsedHash{}, false
	}
	return parsedHash{params: params, salt: salt, key: key}, true
}

// parseArgonParams parses the m=,t=,p= field of a PHC string. Values that
// argon2 would reject or that would make it allocate unreasonably are refused
// here, before the library can panic or exhaust memory.
func parseArgonParams(field string) (argonParams, bool) {
	fields := strings.Split(field, ",")
	if len(fields) != 3 {
		return argonParams{}, false
	}
	memory, ok := parseUintField(fields[0], "m=")
	if !ok || memory < 8 || memory > maxArgonMemoryKiB {
		return argonParams{}, false
	}
	iterations, ok := parseUintField(fields[1], "t=")
	if !ok || iterations < 1 || iterations > maxArgonIterations {
		return argonParams{}, false
	}
	threads, ok := parseUintField(fields[2], "p=")
	if !ok || threads < 1 || threads > 255 {
		return argonParams{}, false
	}
	return argonParams{memoryKiB: uint32(memory), iterations: uint32(iterations), threads: uint8(threads)}, true
}

// parseUintField parses the decimal value behind prefix, e.g. 19456 out of
// m=19456.
func parseUintField(field, prefix string) (uint64, bool) {
	value, found := strings.CutPrefix(field, prefix)
	if !found || value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, false
	}
	return parsed, true
}
