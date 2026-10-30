// Package assign deterministically maps visitors and trials to factor levels.
//
// Every level is a pure function of (experiment version, factor salt,
// visitor ID, trial index, option slot), so:
//   - the same visitor always sees the same sequence of trials,
//   - the server can recompute any trial to check what a client reports,
//   - factors are independent because each has its own salt,
//   - bumping the experiment version reshuffles everyone.
package assign

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"

	"nudge/internal/config"
)

// Slots number the two options in render order: slot 1 is rendered first
// (left when side by side, top when stacked).
const NumSlots = 2

// Levels maps factor name to level name.
type Levels map[string]string

type Trial struct {
	Index   int              `json:"index"`
	Block   int              `json:"block"`
	Trial   Levels           `json:"trial"`   // trial-scoped factors
	Options [NumSlots]Levels `json:"options"` // option-scoped factors; [0] is slot 1
}

type Assigner struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Assigner {
	return &Assigner{cfg: cfg}
}

func (a *Assigner) Version() int { return a.cfg.Version }

// Visitor returns the visitor-scoped factor levels.
func (a *Assigner) Visitor(vid string) Levels {
	out := Levels{}
	for _, f := range a.cfg.ByScope(config.ScopeVisitor) {
		out[f.Name] = f.Levels[a.index(f, vid, 0, 0)]
	}
	return out
}

// Trial returns trial number index (0-based) for a visitor.
func (a *Assigner) Trial(vid string, index int) Trial {
	t := Trial{
		Index: index,
		Block: index / a.cfg.BlockSize,
		Trial: Levels{},
	}
	for s := range t.Options {
		t.Options[s] = Levels{}
	}
	for _, f := range a.cfg.Factors {
		switch f.Scope {
		case config.ScopeTrial:
			t.Trial[f.Name] = f.Levels[a.index(f, vid, index, 0)]
		case config.ScopeOption:
			for s := range t.Options {
				t.Options[s][f.Name] = f.Levels[a.index(f, vid, index, s+1)]
			}
		}
	}
	return t
}

// Trials returns count consecutive trials starting at start.
func (a *Assigner) Trials(vid string, start, count int) []Trial {
	out := make([]Trial, 0, count)
	for i := start; i < start+count; i++ {
		out = append(out, a.Trial(vid, i))
	}
	return out
}

// index picks a level for factor f. Inputs a factor's scope doesn't use are
// ignored: visitor factors hash neither trial nor slot, trial factors don't
// hash slot.
func (a *Assigner) index(f config.Factor, vid string, trial, slot int) int {
	switch f.Scope {
	case config.ScopeVisitor:
		trial, slot = -1, 0
	case config.ScopeTrial:
		slot = 0
	}
	return int(draw(a.cfg.Version, f.Salt, vid, trial, slot) % uint64(len(f.Levels)))
}

// draw hashes the inputs to a uniform uint64. Strings are length-prefixed so
// distinct inputs can never encode to the same bytes. Modulo bias from
// reducing 2^64 to a handful of levels is ~n/2^64, i.e. negligible.
func draw(version int, salt, vid string, trial, slot int) uint64 {
	h := sha256.New()
	writeInt(h, int64(version))
	writeStr(h, salt)
	writeStr(h, vid)
	writeInt(h, int64(trial))
	writeInt(h, int64(slot))
	var sum [sha256.Size]byte
	return binary.BigEndian.Uint64(h.Sum(sum[:0]))
}

func writeInt(h hash.Hash, v int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	h.Write(b[:])
}

func writeStr(h hash.Hash, s string) {
	writeInt(h, int64(len(s)))
	h.Write([]byte(s))
}
