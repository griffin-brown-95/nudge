// Package fixture builds valid event payloads for tests. Payloads are plain
// maps so tests can break one field at a time with Set.
package fixture

import (
	"encoding/json"
	"strings"

	"nudge/internal/assign"
)

const (
	VisitorID  = "1b4e28ba-2fa1-4d3b-8f5c-0123456789ab"
	SessionID  = "2c5f39cb-3ab2-4e4c-9a6d-123456789abc"
	ExposureID = "3d6a4adc-4bc3-4f5d-8b7e-23456789abcd"
)

// Delete, passed to Set, removes the key.
var Delete = &struct{}{}

func Ctx(version int) map[string]any {
	return map[string]any{
		"device_class":       "phone",
		"pointer":            "coarse",
		"viewport_w":         390,
		"viewport_h":         844,
		"orientation":        "portrait",
		"layout":             "stacked",
		"theme":              "dark",
		"referrer_domain":    "news.ycombinator.com",
		"local_hour":         21,
		"experiment_version": version,
	}
}

func SessionStart(a *assign.Assigner) map[string]any {
	return map[string]any{
		"type":             "session_start",
		"visitor_id":       VisitorID,
		"session_id":       SessionID,
		"session_number":   3,
		"days_since_first": 2,
		"fonts_ready":      true,
		"start_pointer":    nil,
		"ctx":              Ctx(a.Version()),
	}
}

// Exposure renders exactly what the assigner says for VisitorID's trial.
func Exposure(a *assign.Assigner, trialIndex int) map[string]any {
	t := a.Trial(VisitorID, trialIndex)
	return map[string]any{
		"type":        "exposure",
		"visitor_id":  VisitorID,
		"session_id":  SessionID,
		"exposure_id": ExposureID,
		"trial_index": t.Index,
		"block_index": t.Block,
		"shown":       roundTrip(map[string]any{"options": t.Options, "trial": t.Trial}),
		"rendered": map[string]any{
			"render_delay_ms": 640,
			"pointer":         map[string]any{"x": 0.5, "y": 0.8},
			"focus_applied":   "none",
		},
		"informed": false,
		"ctx":      Ctx(a.Version()),
	}
}

func Choice(a *assign.Assigner) map[string]any {
	return map[string]any{
		"type":        "choice",
		"visitor_id":  VisitorID,
		"session_id":  SessionID,
		"exposure_id": ExposureID,
		"chosen_slot": 2,
		"latency_ms":  2140,
		"input":       "mouse",
		"trace": map[string]any{
			"pointer": []any{
				map[string]any{"t": 310, "e": "enter", "slot": 1},
				map[string]any{"t": 820, "e": "leave", "slot": 1},
				map[string]any{"t": 900, "e": "enter", "slot": 2},
				map[string]any{"t": 2140, "e": "down", "slot": 2},
			},
			"dwell_ms":  []any{510, 1240},
			"focus_seq": []any{1, 2},
		},
		"ctx": Ctx(a.Version()),
	}
}

func TouchChoice(a *assign.Assigner) map[string]any {
	c := Choice(a)
	c["input"] = "touch"
	c["trace"] = map[string]any{
		"first_touch_ms": 1800,
		"scroll_px":      120,
		"tap":            map[string]any{"slot": 2, "x": 0.42, "y": 0.61},
	}
	return c
}

func BlockEnd(a *assign.Assigner) map[string]any {
	return map[string]any{
		"type":        "block_end",
		"visitor_id":  VisitorID,
		"session_id":  SessionID,
		"block_index": 0,
		"action":      "keep_going",
		"ctx":         Ctx(a.Version()),
	}
}

// Set assigns v at a dotted path like "ctx.local_hour" or
// "shown.options.0.color". It returns a deep copy; m is not modified.
func Set(m map[string]any, path string, v any) map[string]any {
	out := roundTrip(m).(map[string]any)
	keys := strings.Split(path, ".")
	var cur any = out
	for i, k := range keys {
		last := i == len(keys)-1
		switch c := cur.(type) {
		case map[string]any:
			if last {
				if v == Delete {
					delete(c, k)
				} else {
					c[k] = v
				}
				return out
			}
			cur = c[k]
		case []any:
			idx := 0
			for _, ch := range k {
				idx = idx*10 + int(ch-'0')
			}
			if last {
				c[idx] = v
				return out
			}
			cur = c[idx]
		default:
			panic("fixture.Set: bad path " + path)
		}
	}
	return out
}

func JSON(m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func roundTrip(v any) any {
	var out any
	if err := json.Unmarshal(mustMarshal(v), &out); err != nil {
		panic(err)
	}
	return out
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
