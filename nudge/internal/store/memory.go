package store

import (
	"context"
	"sync"

	"nudge/internal/events"
)

// Memory is an in-process Store for tests and for running the frontend
// without a database. It enforces the same parent/idempotency rules as
// Postgres. Data is lost on restart.
type Memory struct {
	mu        sync.Mutex
	visitors  map[string]bool
	sessions  map[string]string    // session ID -> visitor ID
	exposures map[string][2]string // exposure ID -> {visitor ID, session ID}
	choices   map[string]bool
	blockEnds map[[2]any]bool

	SessionStarts []*events.SessionStart
	Exposures     []*events.Exposure
	Choices       []*events.Choice
	BlockEnds     []*events.BlockEnd
}

func NewMemory() *Memory {
	return &Memory{
		visitors:  map[string]bool{},
		sessions:  map[string]string{},
		exposures: map[string][2]string{},
		choices:   map[string]bool{},
		blockEnds: map[[2]any]bool{},
	}
}

func (m *Memory) InsertSessionStart(_ context.Context, e *events.SessionStart) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.visitors[e.VisitorID] = true
	if _, ok := m.sessions[e.SessionID]; ok {
		return nil
	}
	m.sessions[e.SessionID] = e.VisitorID
	m.SessionStarts = append(m.SessionStarts, e)
	return nil
}

func (m *Memory) InsertExposure(_ context.Context, e *events.Exposure) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.exposures[e.ExposureID]; ok {
		return nil
	}
	if m.sessions[e.SessionID] != e.VisitorID {
		return ErrMissingParent
	}
	m.exposures[e.ExposureID] = [2]string{e.VisitorID, e.SessionID}
	m.Exposures = append(m.Exposures, e)
	return nil
}

func (m *Memory) InsertChoice(_ context.Context, e *events.Choice) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.choices[e.ExposureID] {
		return nil
	}
	if m.exposures[e.ExposureID] != [2]string{e.VisitorID, e.SessionID} {
		return ErrMissingParent
	}
	m.choices[e.ExposureID] = true
	m.Choices = append(m.Choices, e)
	return nil
}

func (m *Memory) InsertBlockEnd(_ context.Context, e *events.BlockEnd) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := [2]any{e.SessionID, *e.BlockIndex}
	if m.blockEnds[key] {
		return nil
	}
	if m.sessions[e.SessionID] != e.VisitorID {
		return ErrMissingParent
	}
	m.blockEnds[key] = true
	m.BlockEnds = append(m.BlockEnds, e)
	return nil
}

func (m *Memory) Ping(context.Context) error { return nil }
