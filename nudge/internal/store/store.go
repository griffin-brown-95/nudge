// Package store persists validated events.
package store

import (
	"context"
	"errors"

	"nudge/internal/events"
)

// ErrMissingParent means an event arrived before the event it belongs to
// (e.g. a choice before its exposure, or one whose IDs don't line up). The
// client keeps its queue in order, so this is usually transient: it retries.
var ErrMissingParent = errors.New("parent event not found")

// Store writes events. Every insert is idempotent: re-sending an event the
// store already has succeeds without writing a duplicate, so clients can
// retry freely.
type Store interface {
	InsertSessionStart(ctx context.Context, e *events.SessionStart) error
	InsertExposure(ctx context.Context, e *events.Exposure) error
	InsertChoice(ctx context.Context, e *events.Choice) error
	InsertBlockEnd(ctx context.Context, e *events.BlockEnd) error
	Ping(ctx context.Context) error
}

// Insert dispatches an event to the matching method.
func Insert(ctx context.Context, s Store, ev events.Event) error {
	switch e := ev.(type) {
	case *events.SessionStart:
		return s.InsertSessionStart(ctx, e)
	case *events.Exposure:
		return s.InsertExposure(ctx, e)
	case *events.Choice:
		return s.InsertChoice(ctx, e)
	case *events.BlockEnd:
		return s.InsertBlockEnd(ctx, e)
	}
	return errors.New("store: unknown event type")
}
