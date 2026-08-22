// Package domain implements the Mapping aggregate and its value types
// (BR-002 to BR-004/BR-007; DR-001 to DR-003): a short key identifies at
// most one destination at a time (BR-003), an active mapping's
// destination is immutable (BR-004), and a mapping exposes only Active
// or Suspended, reached solely through authorized transitions (DR-003).
// Unknown, malformed, suspended, or otherwise invalid states are made
// unrepresentable by construction (validating constructors, unexported
// fields, no destination setter) rather than merely checked for at
// read time (BR-007).
//
// Deliberately has no AWS SDK or persistence dependency at all
// (docs/TECH_STACK.md Section 6: "Keep domain rules independent of AWS
// SDK types. Cloud services are adapters behind narrow interfaces") —
// wiring this into internal/mapping's DynamoDB repository and the
// creation/redirect application flow is T6-03's job
// ("creation application use case and repository port"), not this
// task's.
package domain

import "errors"

// ErrInvalidTransition is returned by Suspend/Reinstate when the
// Mapping is not currently in the state that transition requires —
// DR-003's "only authorized transitions" enforced as a return value,
// not a state a caller could silently no-op past.
var ErrInvalidTransition = errors.New("domain: invalid status transition")

// Mapping is the short key -> destination aggregate. The zero value is
// not a valid Mapping — only NewMapping produces one, always starting
// Active at version 1, matching every other committed Mapping's
// starting state.
type Mapping struct {
	shortKey    ShortKey
	destination Destination
	status      Status
	version     int64
}

// NewMapping constructs a new, Active Mapping. shortKey and destination
// are already-validated values by the time they reach here (that's the
// point of ShortKey/Destination being distinct types with validating
// constructors) — there is nothing left for NewMapping itself to
// reject.
func NewMapping(shortKey ShortKey, destination Destination) Mapping {
	return Mapping{
		shortKey:    shortKey,
		destination: destination,
		status:      StatusActive,
		version:     1,
	}
}

func (m Mapping) ShortKey() ShortKey       { return m.shortKey }
func (m Mapping) Destination() Destination { return m.destination }
func (m Mapping) Status() Status           { return m.status }
func (m Mapping) Version() int64           { return m.version }

// Suspend transitions Active -> Suspended. Called on an already-
// Suspended Mapping, it returns ErrInvalidTransition and leaves the
// Mapping unchanged — suspending is not idempotent, matching DR-003's
// "only authorized transitions" (Suspended -> Suspended is not one of
// the two authorized transitions this type recognizes).
func (m *Mapping) Suspend() error {
	if m.status != StatusActive {
		return ErrInvalidTransition
	}
	m.status = StatusSuspended
	m.version++
	return nil
}

// Reinstate transitions Suspended -> Active. Called on an Active
// Mapping, it returns ErrInvalidTransition and leaves the Mapping
// unchanged, for the same reason as Suspend.
func (m *Mapping) Reinstate() error {
	if m.status != StatusSuspended {
		return ErrInvalidTransition
	}
	m.status = StatusActive
	m.version++
	return nil
}
