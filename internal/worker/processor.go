package worker

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const defaultOutboxRetention = 14 * 24 * time.Hour

var (
	ErrCheckpointConflict = errors.New("worker: checkpoint changed concurrently")
	ErrVersionGap         = errors.New("worker: aggregate version gap")
	ErrVersionConflict    = errors.New("worker: conflicting event at processed version")
)

type Checkpoint struct {
	Version   int64
	EventID   string
	EventType string
}

// Store persists the version checkpoint and acknowledges the corresponding
// outbox record. Commit must make those two changes atomically.
type Store interface {
	Load(context.Context, string) (Checkpoint, bool, error)
	Commit(context.Context, Event, int64, time.Time, time.Time) error
	Acknowledge(context.Context, Event, time.Time, time.Time) error
}

type Outcome string

const (
	OutcomeApplied   Outcome = "applied"
	OutcomeDuplicate Outcome = "duplicate"
)

type Result struct {
	Event   Event
	Outcome Outcome
}

type Processor struct {
	store     Store
	now       func() time.Time
	retention time.Duration
}

func NewProcessor(store Store) *Processor {
	return &Processor{store: store, now: time.Now, retention: defaultOutboxRetention}
}

// Process applies exactly the next aggregate version. Older reordered events
// are acknowledged without moving the checkpoint backwards; an exact repeat
// is idempotent; a same-version/different-identity event fails closed; and a
// gap is left unacknowledged so SQS can retry and eventually redrive it.
func (p *Processor) Process(ctx context.Context, body string) (Result, error) {
	event, err := DecodeStreamRecord(body)
	if err != nil {
		return Result{}, err
	}

	for attempts := 0; attempts < 8; attempts++ {
		checkpoint, found, err := p.store.Load(ctx, event.AggregateKey)
		if err != nil {
			return Result{Event: event}, fmt.Errorf("load checkpoint: %w", err)
		}
		currentVersion := int64(0)
		if found {
			currentVersion = checkpoint.Version
		}

		switch {
		case event.AggregateVersion > currentVersion+1:
			return Result{Event: event}, fmt.Errorf("%w: current=%d received=%d", ErrVersionGap, currentVersion, event.AggregateVersion)
		case event.AggregateVersion <= currentVersion:
			if event.AggregateVersion == currentVersion &&
				(checkpoint.EventID != event.EventID || checkpoint.EventType != event.EventType) {
				return Result{Event: event}, fmt.Errorf("%w: aggregate=%s version=%d", ErrVersionConflict, event.AggregateKey, event.AggregateVersion)
			}
			processedAt := p.now().UTC()
			if err := p.store.Acknowledge(ctx, event, processedAt, processedAt.Add(p.retention)); err != nil {
				return Result{Event: event}, fmt.Errorf("acknowledge duplicate: %w", err)
			}
			return Result{Event: event, Outcome: OutcomeDuplicate}, nil
		default:
			processedAt := p.now().UTC()
			err := p.store.Commit(ctx, event, currentVersion, processedAt, processedAt.Add(p.retention))
			if errors.Is(err, ErrCheckpointConflict) {
				continue
			}
			if err != nil {
				return Result{Event: event}, fmt.Errorf("commit checkpoint: %w", err)
			}
			return Result{Event: event, Outcome: OutcomeApplied}, nil
		}
	}
	return Result{Event: event}, ErrCheckpointConflict
}
