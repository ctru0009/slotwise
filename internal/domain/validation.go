package domain

import "errors"

// ErrInvalidInput reports that a request failed validation.
var ErrInvalidInput = errors.New("invalid input")

// ValidationError names the field that failed validation, so a form can point
// at the offending input.
type ValidationError struct {
	Field   string
	Message string
}

// Error implements error.
func (e ValidationError) Error() string {
	return e.Field + ": " + e.Message
}

// Is reports that every validation failure matches ErrInvalidInput, so callers
// can use errors.Is without knowing which field failed.
func (e ValidationError) Is(target error) bool {
	return target == ErrInvalidInput
}
