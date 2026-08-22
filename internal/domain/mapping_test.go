package domain

import (
	"errors"
	"testing"
)

func testMapping(t *testing.T) Mapping {
	t.Helper()
	key, err := NewShortKey("kx7fq2b")
	if err != nil {
		t.Fatalf("NewShortKey: %v", err)
	}
	dest, err := NewDestination("https://example.com/target")
	if err != nil {
		t.Fatalf("NewDestination: %v", err)
	}
	return NewMapping(key, dest)
}

// TestNewMapping_StartsActive proves every newly constructed Mapping
// starts in exactly one, unambiguous state: Active, version 1 — never
// partially initialized, never Suspended by default.
func TestNewMapping_StartsActive(t *testing.T) {
	m := testMapping(t)
	if m.Status() != StatusActive {
		t.Fatalf("expected new Mapping to start Active, got %q", m.Status())
	}
	if m.Version() != 1 {
		t.Fatalf("expected new Mapping to start at version 1, got %d", m.Version())
	}
}

// TestMapping_SuspendThenReinstate proves the two authorized
// transitions (DR-003) both work and advance the version.
func TestMapping_SuspendThenReinstate(t *testing.T) {
	m := testMapping(t)

	if err := m.Suspend(); err != nil {
		t.Fatalf("Suspend from Active: unexpected error: %v", err)
	}
	if m.Status() != StatusSuspended {
		t.Fatalf("expected Suspended after Suspend, got %q", m.Status())
	}
	if m.Version() != 2 {
		t.Fatalf("expected version 2 after one transition, got %d", m.Version())
	}

	if err := m.Reinstate(); err != nil {
		t.Fatalf("Reinstate from Suspended: unexpected error: %v", err)
	}
	if m.Status() != StatusActive {
		t.Fatalf("expected Active after Reinstate, got %q", m.Status())
	}
	if m.Version() != 3 {
		t.Fatalf("expected version 3 after two transitions, got %d", m.Version())
	}
}

// TestMapping_SuspendTwice proves an unauthorized transition
// (Suspended -> Suspended) is rejected, not silently accepted as a
// no-op — DR-003's "only authorized transitions", not "any transition
// that happens to land on a valid state".
func TestMapping_SuspendTwice(t *testing.T) {
	m := testMapping(t)
	if err := m.Suspend(); err != nil {
		t.Fatalf("first Suspend: unexpected error: %v", err)
	}
	if err := m.Suspend(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second Suspend: expected ErrInvalidTransition, got %v", err)
	}
	// The rejected transition must not have mutated state.
	if m.Status() != StatusSuspended {
		t.Fatalf("expected status to remain Suspended after a rejected transition, got %q", m.Status())
	}
	if m.Version() != 2 {
		t.Fatalf("expected version to remain 2 after a rejected transition, got %d", m.Version())
	}
}

// TestMapping_ReinstateWithoutSuspend proves the same for the reverse
// unauthorized transition (Active -> Active via Reinstate).
func TestMapping_ReinstateWithoutSuspend(t *testing.T) {
	m := testMapping(t)
	if err := m.Reinstate(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}
	if m.Status() != StatusActive {
		t.Fatalf("expected status to remain Active after a rejected transition, got %q", m.Status())
	}
	if m.Version() != 1 {
		t.Fatalf("expected version to remain 1 after a rejected transition, got %d", m.Version())
	}
}

// TestMapping_DestinationImmutable documents BR-004 as a compile-time
// property, not a runtime one: Destination has no setter at all, so
// there is no method call this test could make to mutate it. The
// assertion here is only that the same Destination value survives a
// Suspend/Reinstate cycle unchanged.
func TestMapping_DestinationImmutable(t *testing.T) {
	m := testMapping(t)
	before := m.Destination()
	_ = m.Suspend()
	_ = m.Reinstate()
	if m.Destination() != before {
		t.Fatalf("expected Destination to survive status transitions unchanged: before=%q after=%q", before, m.Destination())
	}
}

// TestMapping_ZeroValueIsNotAnyValidStatus proves an unrepresentable
// state doesn't quietly present as Active: the zero Mapping's Status is
// empty, distinguishable from both StatusActive and StatusSuspended.
func TestMapping_ZeroValueIsNotAnyValidStatus(t *testing.T) {
	var zero Mapping
	if zero.Status() == StatusActive || zero.Status() == StatusSuspended {
		t.Fatalf("expected the zero Mapping's Status to be neither Active nor Suspended, got %q", zero.Status())
	}
}
