package domain

import "errors"

// ErrSlotTaken reports that the requested slot overlaps a confirmed booking
// for the same staff member.
var ErrSlotTaken = errors.New("slot taken")
