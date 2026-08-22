package domain

// Status is a Mapping's lifecycle state (DR-003: "Each Mapping shall
// expose an Active or Suspended lifecycle status; disposition state
// remains policy-driven ... no partial state exposed"). A closed set of
// two values, not an arbitrary string — nothing outside this package can
// construct a Status the type system doesn't already know is one of
// these two.
type Status string

const (
	StatusActive    Status = "Active"
	StatusSuspended Status = "Suspended"
)
