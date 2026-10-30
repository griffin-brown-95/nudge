// Package events decodes and validates the events the browser posts.
//
// The server never trusts the client's idea of the condition: for exposures
// it recomputes the trial from (visitor ID, trial index) and rejects the event
// if what the client says it rendered doesn't match.
package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"nudge/internal/assign"
)

const (
	MaxBodyBytes   = 16 << 10
	MaxLatencyMS   = 60 * 60 * 1000 // one hour
	MaxTraceLen    = 200
	MaxScrollPX    = 1_000_000
	MaxViewportPX  = 20_000
	MaxSessionNum  = 1_000_000
	MaxDaysSince   = 36_500
	TraceSlackMS   = 1_000 // pointer events may trail the click slightly
	maxStringField = 253
)

type Kind string

const (
	KindSessionStart Kind = "session_start"
	KindExposure     Kind = "exposure"
	KindChoice       Kind = "choice"
	KindBlockEnd     Kind = "block_end"
)

// Event is one of *SessionStart, *Exposure, *Choice, *BlockEnd.
type Event interface{ Kind() Kind }

// Context is attached to every event.
type Context struct {
	DeviceClass       string `json:"device_class"`
	Pointer           string `json:"pointer"`
	ViewportW         int    `json:"viewport_w"`
	ViewportH         int    `json:"viewport_h"`
	Orientation       string `json:"orientation"`
	Layout            string `json:"layout"`
	Theme             string `json:"theme"`
	ReferrerDomain    string `json:"referrer_domain"`
	LocalHour         *int   `json:"local_hour"`
	ExperimentVersion int    `json:"experiment_version"`
}

// Point is a position as fractions of the viewport (or of an option's box).
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type SessionStart struct {
	Type           Kind    `json:"type"`
	VisitorID      string  `json:"visitor_id"`
	SessionID      string  `json:"session_id"`
	SessionNumber  int     `json:"session_number"`
	DaysSinceFirst *int    `json:"days_since_first"`
	FontsReady     bool    `json:"fonts_ready"`
	StartPointer   *Point  `json:"start_pointer"` // null on touch devices
	Ctx            Context `json:"ctx"`

	Visitor assign.Levels `json:"-"` // server-computed
}

// Shown is what the client says it rendered; it must match the assignment.
type Shown struct {
	Options [assign.NumSlots]assign.Levels `json:"options"`
	Trial   assign.Levels                  `json:"trial"`
}

type Rendered struct {
	RenderDelayMS *int   `json:"render_delay_ms"`
	Pointer       *Point `json:"pointer"`       // pointer position at render; null on touch
	FocusApplied  string `json:"focus_applied"` // none|first|second
}

type Exposure struct {
	Type       Kind     `json:"type"`
	VisitorID  string   `json:"visitor_id"`
	SessionID  string   `json:"session_id"`
	ExposureID string   `json:"exposure_id"`
	TrialIndex *int     `json:"trial_index"`
	BlockIndex *int     `json:"block_index"`
	Shown      Shown    `json:"shown"`
	Rendered   Rendered `json:"rendered"`
	Informed   bool     `json:"informed"`
	Ctx        Context  `json:"ctx"`

	Trial   assign.Trial  `json:"-"` // server-computed
	Visitor assign.Levels `json:"-"`
}

type PointerEvent struct {
	T    int    `json:"t"` // ms since render
	E    string `json:"e"` // enter|leave|down
	Slot int    `json:"slot"`
}

type Tap struct {
	Slot int     `json:"slot"`
	X    float64 `json:"x"` // fraction of the option's box
	Y    float64 `json:"y"`
}

type Trace struct {
	Pointer      []PointerEvent `json:"pointer,omitempty"`
	DwellMS      *[2]int        `json:"dwell_ms,omitempty"` // [slot1, slot2]
	FirstTouchMS *int           `json:"first_touch_ms,omitempty"`
	ScrollPX     *int           `json:"scroll_px,omitempty"`
	Tap          *Tap           `json:"tap,omitempty"`
	FocusSeq     []int          `json:"focus_seq,omitempty"` // slots focused via keyboard, in order
}

type Choice struct {
	Type       Kind    `json:"type"`
	VisitorID  string  `json:"visitor_id"`
	SessionID  string  `json:"session_id"`
	ExposureID string  `json:"exposure_id"`
	ChosenSlot int     `json:"chosen_slot"`
	LatencyMS  *int    `json:"latency_ms"`
	Input      string  `json:"input"`
	Trace      Trace   `json:"trace"`
	Ctx        Context `json:"ctx"`
}

type BlockEnd struct {
	Type       Kind    `json:"type"`
	VisitorID  string  `json:"visitor_id"`
	SessionID  string  `json:"session_id"`
	BlockIndex *int    `json:"block_index"`
	Action     string  `json:"action"` // keep_going|done
	Ctx        Context `json:"ctx"`
}

func (*SessionStart) Kind() Kind { return KindSessionStart }
func (*Exposure) Kind() Kind     { return KindExposure }
func (*Choice) Kind() Kind       { return KindChoice }
func (*BlockEnd) Kind() Kind     { return KindBlockEnd }

// ErrStaleVersion means the client is running an older experiment version.
// Its events can't be checked against the current config, so they're dropped.
var ErrStaleVersion = errors.New("stale experiment version")

// ValidationError lists every problem found in an event.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid event: " + strings.Join(e.Problems, "; ")
}

type Validator struct {
	A *assign.Assigner
}

// Parse decodes body into a typed event and validates it. Unknown fields are
// rejected so client/server drift shows up immediately.
func (v *Validator) Parse(body []byte) (Event, error) {
	// Unmarshal also rejects trailing data after the object.
	var head struct {
		Type Kind `json:"type"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return nil, &ValidationError{[]string{"body is not a JSON object: " + err.Error()}}
	}

	var ev Event
	switch head.Type {
	case KindSessionStart:
		ev = &SessionStart{}
	case KindExposure:
		ev = &Exposure{}
	case KindChoice:
		ev = &Choice{}
	case KindBlockEnd:
		ev = &BlockEnd{}
	default:
		return nil, &ValidationError{[]string{fmt.Sprintf("unknown event type %q", head.Type)}}
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(ev); err != nil {
		return nil, &ValidationError{[]string{"decode: " + err.Error()}}
	}

	p := &problems{}
	switch e := ev.(type) {
	case *SessionStart:
		v.checkSessionStart(p, e)
	case *Exposure:
		v.checkExposure(p, e)
	case *Choice:
		v.checkChoice(p, e)
	case *BlockEnd:
		v.checkBlockEnd(p, e)
	}
	if p.stale {
		return nil, ErrStaleVersion
	}
	if len(p.list) > 0 {
		return nil, &ValidationError{p.list}
	}
	return ev, nil
}

type problems struct {
	list  []string
	stale bool
}

func (p *problems) add(format string, a ...any) { p.list = append(p.list, fmt.Sprintf(format, a...)) }

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidUUID reports whether s is a lowercase hyphenated UUID, the form
// crypto.randomUUID() produces.
func ValidUUID(s string) bool { return uuidRe.MatchString(s) }

func (p *problems) uuid(field, s string) {
	if !ValidUUID(s) {
		p.add("%s must be a lowercase UUID", field)
	}
}

func (p *problems) oneOf(field, s string, allowed ...string) {
	for _, a := range allowed {
		if s == a {
			return
		}
	}
	p.add("%s must be one of %v, got %q", field, allowed, truncate(s))
}

func (p *problems) intRange(field string, n *int, lo, hi int) {
	switch {
	case n == nil:
		p.add("%s is required", field)
	case *n < lo || *n > hi:
		p.add("%s must be in [%d, %d], got %d", field, lo, hi, *n)
	}
}

func (p *problems) fraction(field string, pt *Point) {
	if pt != nil && (pt.X < 0 || pt.X > 1 || pt.Y < 0 || pt.Y > 1) {
		p.add("%s must have x and y in [0, 1]", field)
	}
}

func (p *problems) slot(field string, s int) {
	if s < 1 || s > assign.NumSlots {
		p.add("%s must be 1 or 2, got %d", field, s)
	}
}

var hostRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

func (v *Validator) checkCtx(p *problems, c Context) {
	switch {
	case c.ExperimentVersion <= 0:
		p.add("ctx.experiment_version is required")
	case c.ExperimentVersion != v.A.Version():
		p.stale = true
	}
	p.oneOf("ctx.device_class", c.DeviceClass, "phone", "tablet", "desktop")
	p.oneOf("ctx.pointer", c.Pointer, "coarse", "fine", "none")
	p.oneOf("ctx.orientation", c.Orientation, "portrait", "landscape")
	p.oneOf("ctx.layout", c.Layout, "side_by_side", "stacked")
	p.oneOf("ctx.theme", c.Theme, "light", "dark")
	if c.ViewportW < 1 || c.ViewportW > MaxViewportPX || c.ViewportH < 1 || c.ViewportH > MaxViewportPX {
		p.add("ctx viewport must be within [1, %d] px", MaxViewportPX)
	}
	if c.ReferrerDomain != "" && (len(c.ReferrerDomain) > maxStringField || !hostRe.MatchString(c.ReferrerDomain)) {
		p.add("ctx.referrer_domain must be a bare lowercase hostname")
	}
	p.intRange("ctx.local_hour", c.LocalHour, 0, 23)
}

func (v *Validator) checkSessionStart(p *problems, e *SessionStart) {
	p.uuid("visitor_id", e.VisitorID)
	p.uuid("session_id", e.SessionID)
	sn := e.SessionNumber
	p.intRange("session_number", &sn, 1, MaxSessionNum)
	p.intRange("days_since_first", e.DaysSinceFirst, 0, MaxDaysSince)
	p.fraction("start_pointer", e.StartPointer)
	v.checkCtx(p, e.Ctx)
	if len(p.list) == 0 {
		e.Visitor = v.A.Visitor(e.VisitorID)
	}
}

func (v *Validator) checkExposure(p *problems, e *Exposure) {
	p.uuid("visitor_id", e.VisitorID)
	p.uuid("session_id", e.SessionID)
	p.uuid("exposure_id", e.ExposureID)
	p.intRange("trial_index", e.TrialIndex, 0, assign.MaxTrialIndex)
	p.intRange("block_index", e.BlockIndex, 0, assign.MaxTrialIndex)
	p.intRange("rendered.render_delay_ms", e.Rendered.RenderDelayMS, 0, MaxLatencyMS)
	p.fraction("rendered.pointer", e.Rendered.Pointer)
	p.oneOf("rendered.focus_applied", e.Rendered.FocusApplied, "none", "first", "second")
	v.checkCtx(p, e.Ctx)
	if len(p.list) > 0 || !ValidUUID(e.VisitorID) {
		return
	}

	want := v.A.Trial(e.VisitorID, *e.TrialIndex)
	if *e.BlockIndex != want.Block {
		p.add("block_index %d does not match trial_index %d (want %d)", *e.BlockIndex, *e.TrialIndex, want.Block)
	}
	if !reflect.DeepEqual(e.Shown.Trial, want.Trial) {
		p.add("shown.trial does not match the assigned trial")
	}
	for s := range want.Options {
		if !reflect.DeepEqual(e.Shown.Options[s], want.Options[s]) {
			p.add("shown.options[%d] does not match the assigned option", s)
		}
	}
	e.Trial = want
	e.Visitor = v.A.Visitor(e.VisitorID)
}

func (v *Validator) checkChoice(p *problems, e *Choice) {
	p.uuid("visitor_id", e.VisitorID)
	p.uuid("session_id", e.SessionID)
	p.uuid("exposure_id", e.ExposureID)
	p.slot("chosen_slot", e.ChosenSlot)
	p.intRange("latency_ms", e.LatencyMS, 0, MaxLatencyMS)
	p.oneOf("input", e.Input, "mouse", "touch", "pen", "keyboard")
	v.checkCtx(p, e.Ctx)

	limit := MaxLatencyMS + TraceSlackMS
	if e.LatencyMS != nil {
		limit = *e.LatencyMS + TraceSlackMS
	}
	t := e.Trace
	if len(t.Pointer) > MaxTraceLen {
		p.add("trace.pointer has %d entries, max %d", len(t.Pointer), MaxTraceLen)
	} else {
		prev := 0
		for i, pe := range t.Pointer {
			if pe.T < prev || pe.T > limit {
				p.add("trace.pointer[%d].t must be non-decreasing and within [0, latency_ms+%d]", i, TraceSlackMS)
				break
			}
			prev = pe.T
			if pe.E != "enter" && pe.E != "leave" && pe.E != "down" {
				p.add("trace.pointer[%d].e must be enter, leave or down", i)
				break
			}
			if pe.Slot < 1 || pe.Slot > assign.NumSlots {
				p.add("trace.pointer[%d].slot must be 1 or 2", i)
				break
			}
		}
	}
	if len(t.FocusSeq) > MaxTraceLen {
		p.add("trace.focus_seq has %d entries, max %d", len(t.FocusSeq), MaxTraceLen)
	} else {
		for i, s := range t.FocusSeq {
			if s < 1 || s > assign.NumSlots {
				p.add("trace.focus_seq[%d] must be 1 or 2", i)
				break
			}
		}
	}
	if t.DwellMS != nil {
		for i, d := range t.DwellMS {
			if d < 0 || d > limit {
				p.add("trace.dwell_ms[%d] must be within [0, latency_ms+%d]", i, TraceSlackMS)
			}
		}
	}
	if t.FirstTouchMS != nil {
		p.intRange("trace.first_touch_ms", t.FirstTouchMS, 0, limit)
	}
	if t.ScrollPX != nil {
		p.intRange("trace.scroll_px", t.ScrollPX, 0, MaxScrollPX)
	}
	if t.Tap != nil {
		p.slot("trace.tap.slot", t.Tap.Slot)
		p.fraction("trace.tap", &Point{t.Tap.X, t.Tap.Y})
	}
}

func (v *Validator) checkBlockEnd(p *problems, e *BlockEnd) {
	p.uuid("visitor_id", e.VisitorID)
	p.uuid("session_id", e.SessionID)
	p.intRange("block_index", e.BlockIndex, 0, assign.MaxTrialIndex)
	p.oneOf("action", e.Action, "keep_going", "done")
	v.checkCtx(p, e.Ctx)
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
