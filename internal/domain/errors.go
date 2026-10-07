package domain

import "errors"

// ErrSlotTaken reports that the requested slot overlaps a confirmed booking
// for the same staff member.
var ErrSlotTaken = errors.New("slot taken")

// ErrNotFound reports that the requested row does not exist, or is not visible
// in the caller's tenant.
var ErrNotFound = errors.New("not found")

// ErrForbidden reports that the actor may not perform the requested change.
var ErrForbidden = errors.New("forbidden")

// ErrInvalidCredentials reports a login attempt with an unknown email or a
// wrong password. Both cases are deliberately indistinguishable.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrTenantNotFound reports an unknown tenant slug.
var ErrTenantNotFound = errors.New("tenant not found")

// ErrResetTokenInvalid reports a password reset token that is unknown, already
// used, or expired.
var ErrResetTokenInvalid = errors.New("reset token invalid")

// ErrJobSkipped reports that a job handler deliberately delivered nothing: the
// booking was cancelled, or a reminder's appointment had already started. The
// worker completes the job and logs the reason instead of retrying it.
var ErrJobSkipped = errors.New("job skipped")
