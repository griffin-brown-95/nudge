package events_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"nudge/internal/assign"
	"nudge/internal/config"
	"nudge/internal/events"
	"nudge/internal/fixture"
)

func setup(t *testing.T) (*assign.Assigner, *events.Validator) {
	t.Helper()
	cfg, err := config.Load("../../experiments.yaml")
	if err != nil {
		t.Fatal(err)
	}
	a := assign.New(cfg)
	return a, &events.Validator{A: a}
}

func TestValidEventsPass(t *testing.T) {
	a, v := setup(t)
	for name, m := range map[string]map[string]any{
		"session_start":            fixture.SessionStart(a),
		"session_start with mouse": fixture.Set(fixture.SessionStart(a), "start_pointer", map[string]any{"x": 0.5, "y": 0.6}),
		"exposure trial 0":         fixture.Exposure(a, 0),
		"exposure trial 37":        fixture.Exposure(a, 37),
		"exposure no referrer":     fixture.Set(fixture.Exposure(a, 1), "ctx.referrer_domain", ""),
		"exposure midnight":        fixture.Set(fixture.Exposure(a, 1), "ctx.local_hour", 0),
		"mouse choice":             fixture.Choice(a),
		"touch choice":             fixture.TouchChoice(a),
		"keyboard choice":          fixture.Set(fixture.Set(fixture.Choice(a), "input", "keyboard"), "trace", map[string]any{"focus_seq": []any{1, 2, 1}}),
		"instant choice":           fixture.Set(fixture.Set(fixture.Choice(a), "latency_ms", 0), "trace", map[string]any{}),
		"block_end":                fixture.BlockEnd(a),
		"block_end done":           fixture.Set(fixture.BlockEnd(a), "action", "done"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Parse(fixture.JSON(m)); err != nil {
				t.Fatalf("valid event rejected: %v", err)
			}
		})
	}
}

func TestExposureGetsServerTrial(t *testing.T) {
	a, v := setup(t)
	ev, err := v.Parse(fixture.JSON(fixture.Exposure(a, 12)))
	if err != nil {
		t.Fatal(err)
	}
	e := ev.(*events.Exposure)
	if want := a.Trial(fixture.VisitorID, 12); !reflect.DeepEqual(e.Trial, want) {
		t.Errorf("server trial = %+v, want %+v", e.Trial, want)
	}
	if want := a.Visitor(fixture.VisitorID); !reflect.DeepEqual(e.Visitor, want) {
		t.Errorf("visitor levels = %+v, want %+v", e.Visitor, want)
	}
}

func TestInvalidEventsRejected(t *testing.T) {
	a, v := setup(t)
	exp := fixture.Exposure(a, 4)
	trial := a.Trial(fixture.VisitorID, 4)
	otherColor := "red"
	if trial.Options[0]["color"] == "red" {
		otherColor = "blue"
	}
	choice := fixture.Choice(a)
	longTrace := make([]any, events.MaxTraceLen+1)
	for i := range longTrace {
		longTrace[i] = map[string]any{"t": i, "e": "enter", "slot": 1}
	}
	longFocus := make([]any, events.MaxTraceLen+1)
	for i := range longFocus {
		longFocus[i] = 1
	}

	tests := []struct {
		name string
		body []byte
		want string // substring of the error
	}{
		// Envelope
		{"not json", []byte(`{"type":`), "not a JSON object"},
		{"array", []byte(`[1,2]`), "not a JSON object"},
		{"unknown type", []byte(`{"type":"pageview"}`), `unknown event type "pageview"`},
		{"missing type", []byte(`{}`), `unknown event type ""`},
		{"unknown field", fixture.JSON(fixture.Set(choice, "ip_address", "1.2.3.4")), "unknown field"},
		{"trailing data", append(fixture.JSON(choice), []byte(` {}`)...), "after top-level value"},

		// IDs
		{"bad visitor id", fixture.JSON(fixture.Set(choice, "visitor_id", "bob")), "visitor_id must be a lowercase UUID"},
		{"uppercase uuid", fixture.JSON(fixture.Set(choice, "session_id", strings.ToUpper(fixture.SessionID))), "session_id must be"},
		{"missing exposure id", fixture.JSON(fixture.Set(choice, "exposure_id", fixture.Delete)), "exposure_id must be"},

		// Context
		{"missing ctx", fixture.JSON(fixture.Set(choice, "ctx", fixture.Delete)), "ctx.experiment_version is required"},
		{"bad device class", fixture.JSON(fixture.Set(choice, "ctx.device_class", "fridge")), "ctx.device_class"},
		{"bad layout", fixture.JSON(fixture.Set(choice, "ctx.layout", "grid")), "ctx.layout"},
		{"bad theme", fixture.JSON(fixture.Set(choice, "ctx.theme", "sepia")), "ctx.theme"},
		{"zero viewport", fixture.JSON(fixture.Set(choice, "ctx.viewport_w", 0)), "viewport"},
		{"huge viewport", fixture.JSON(fixture.Set(choice, "ctx.viewport_h", 50_000)), "viewport"},
		{"missing hour", fixture.JSON(fixture.Set(choice, "ctx.local_hour", fixture.Delete)), "ctx.local_hour is required"},
		{"hour 24", fixture.JSON(fixture.Set(choice, "ctx.local_hour", 24)), "ctx.local_hour must be in [0, 23]"},
		{"referrer with path", fixture.JSON(fixture.Set(choice, "ctx.referrer_domain", "evil.com/track?id=1")), "referrer_domain"},
		{"referrer uppercase", fixture.JSON(fixture.Set(choice, "ctx.referrer_domain", "Evil.com")), "referrer_domain"},

		// Session start
		{"session number 0", fixture.JSON(fixture.Set(fixture.SessionStart(a), "session_number", 0)), "session_number"},
		{"negative days", fixture.JSON(fixture.Set(fixture.SessionStart(a), "days_since_first", -1)), "days_since_first"},
		{"pointer off screen", fixture.JSON(fixture.Set(fixture.SessionStart(a), "start_pointer", map[string]any{"x": 1.5, "y": 0})), "start_pointer"},

		// Exposure: the client must render exactly the assigned trial
		{"missing trial index", fixture.JSON(fixture.Set(exp, "trial_index", fixture.Delete)), "trial_index is required"},
		{"negative trial index", fixture.JSON(fixture.Set(exp, "trial_index", -1)), "trial_index must be"},
		{"absurd trial index", fixture.JSON(fixture.Set(exp, "trial_index", assign.MaxTrialIndex+1)), "trial_index must be"},
		{"wrong block", fixture.JSON(fixture.Set(exp, "block_index", 7)), "block_index 7 does not match"},
		{"tampered color", fixture.JSON(fixture.Set(exp, "shown.options.0.color", otherColor)), "shown.options[0] does not match"},
		{"unknown level", fixture.JSON(fixture.Set(exp, "shown.options.1.size", "huge")), "shown.options[1] does not match"},
		{"missing option factor", fixture.JSON(fixture.Set(exp, "shown.options.1.emphasis", fixture.Delete)), "shown.options[1] does not match"},
		{"extra option factor", fixture.JSON(fixture.Set(exp, "shown.options.0.font", "serif")), "shown.options[0] does not match"},
		{"tampered trial factor", fixture.JSON(fixture.Set(exp, "shown.trial.labels", "emoji")), "shown.trial does not match"},
		{"other visitor's trial", fixture.JSON(fixture.Set(exp, "visitor_id", "9b4e28ba-2fa1-4d3b-8f5c-0123456789ab")), "does not match"},
		{"negative render delay", fixture.JSON(fixture.Set(exp, "rendered.render_delay_ms", -5)), "render_delay_ms"},
		{"pointer outside", fixture.JSON(fixture.Set(exp, "rendered.pointer.x", -0.1)), "rendered.pointer"},
		{"bad focus", fixture.JSON(fixture.Set(exp, "rendered.focus_applied", "both")), "focus_applied"},

		// Choice
		{"slot 0", fixture.JSON(fixture.Set(choice, "chosen_slot", 0)), "chosen_slot must be 1 or 2"},
		{"slot 3", fixture.JSON(fixture.Set(choice, "chosen_slot", 3)), "chosen_slot must be 1 or 2"},
		{"missing latency", fixture.JSON(fixture.Set(choice, "latency_ms", fixture.Delete)), "latency_ms is required"},
		{"negative latency", fixture.JSON(fixture.Set(choice, "latency_ms", -1)), "latency_ms must be"},
		{"absurd latency", fixture.JSON(fixture.Set(choice, "latency_ms", events.MaxLatencyMS+1)), "latency_ms must be"},
		{"fractional latency", fixture.JSON(fixture.Set(choice, "latency_ms", 12.5)), "decode"},
		{"bad input", fixture.JSON(fixture.Set(choice, "input", "voice")), "input must be"},
		{"oversized trace", fixture.JSON(fixture.Set(choice, "trace.pointer", longTrace)), "trace.pointer has 201 entries"},
		{"oversized focus", fixture.JSON(fixture.Set(choice, "trace.focus_seq", longFocus)), "trace.focus_seq has 201 entries"},
		{"trace out of order", fixture.JSON(fixture.Set(choice, "trace.pointer.1.t", 100)), "non-decreasing"},
		{"trace after click", fixture.JSON(fixture.Set(choice, "trace.pointer.3.t", 2140+events.TraceSlackMS+1)), "non-decreasing and within"},
		{"trace bad kind", fixture.JSON(fixture.Set(choice, "trace.pointer.0.e", "wiggle")), "enter, leave or down"},
		{"trace bad slot", fixture.JSON(fixture.Set(choice, "trace.pointer.0.slot", 9)), "trace.pointer[0].slot"},
		{"trace unknown field", fixture.JSON(fixture.Set(choice, "trace.keystrokes", "secret")), "unknown field"},
		{"negative dwell", fixture.JSON(fixture.Set(choice, "trace.dwell_ms.0", -3)), "dwell_ms[0]"},
		{"dwell exceeds latency", fixture.JSON(fixture.Set(choice, "trace.dwell_ms.1", 99_999)), "dwell_ms[1]"},
		{"focus bad slot", fixture.JSON(fixture.Set(choice, "trace.focus_seq.0", 5)), "focus_seq[0]"},
		{"tap outside", fixture.JSON(fixture.Set(fixture.TouchChoice(a), "trace.tap.x", 2)), "trace.tap"},
		{"negative scroll", fixture.JSON(fixture.Set(fixture.TouchChoice(a), "trace.scroll_px", -1)), "scroll_px"},
		{"touch after click", fixture.JSON(fixture.Set(fixture.TouchChoice(a), "trace.first_touch_ms", 999_999)), "first_touch_ms"},

		// Block end
		{"missing block index", fixture.JSON(fixture.Set(fixture.BlockEnd(a), "block_index", fixture.Delete)), "block_index is required"},
		{"bad action", fixture.JSON(fixture.Set(fixture.BlockEnd(a), "action", "rage_quit")), "action must be"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.Parse(tc.body)
			var verr *events.ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("want ValidationError, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestStaleVersion(t *testing.T) {
	a, v := setup(t)
	for _, m := range []map[string]any{fixture.SessionStart(a), fixture.Exposure(a, 0), fixture.Choice(a), fixture.BlockEnd(a)} {
		_, err := v.Parse(fixture.JSON(fixture.Set(m, "ctx.experiment_version", a.Version()+1)))
		if !errors.Is(err, events.ErrStaleVersion) {
			t.Errorf("%s: want ErrStaleVersion, got %v", m["type"], err)
		}
	}
}

func TestValidationReportsAllProblems(t *testing.T) {
	a, v := setup(t)
	m := fixture.Set(fixture.Set(fixture.Set(fixture.Choice(a), "chosen_slot", 7), "latency_ms", -1), "input", "voice")
	_, err := v.Parse(fixture.JSON(m))
	var verr *events.ValidationError
	if !errors.As(err, &verr) || len(verr.Problems) < 3 {
		t.Fatalf("want ≥3 problems, got %v", err)
	}
}
