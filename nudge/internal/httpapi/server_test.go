package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"nudge/internal/assign"
	"nudge/internal/config"
	"nudge/internal/events"
	"nudge/internal/fixture"
	"nudge/internal/store"
)

const pagesOrigin = "https://example.github.io"

type harness struct {
	t   *testing.T
	a   *assign.Assigner
	cfg *config.Config
	mem *store.Memory
	h   http.Handler
}

func newHarness(t *testing.T, opts ...func(*Options)) *harness {
	t.Helper()
	cfg, err := config.Load("../../experiments.yaml")
	if err != nil {
		t.Fatal(err)
	}
	a := assign.New(cfg)
	mem := store.NewMemory()
	o := Options{Assigner: a, BlockSize: cfg.BlockSize, Store: mem, AllowedOrigins: []string{pagesOrigin, "http://localhost:8000"}}
	for _, f := range opts {
		f(&o)
	}
	return &harness{t: t, a: a, cfg: cfg, mem: mem, h: New(o).Handler()}
}

func (h *harness) do(method, path, origin string, body []byte, ct string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

func (h *harness) post(m map[string]any) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do("POST", "/event", pagesOrigin, fixture.JSON(m), "application/json")
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, want, rec.Body)
	}
}

func errCode(rec *httptest.ResponseRecorder) string {
	var b struct{ Error string }
	json.Unmarshal(rec.Body.Bytes(), &b)
	return b.Error
}

func TestHealthz(t *testing.T) {
	h := newHarness(t)
	wantStatus(t, h.do("GET", "/healthz", "", nil, ""), 200)
	wantStatus(t, h.do("GET", "/healthz?deep=1", "", nil, ""), 200)

	down := newHarness(t, func(o *Options) { o.Store = failingStore{} })
	wantStatus(t, down.do("GET", "/healthz", "", nil, ""), 200) // liveness doesn't need the DB
	wantStatus(t, down.do("GET", "/healthz?deep=1", "", nil, ""), 503)
}

func TestTrials(t *testing.T) {
	h := newHarness(t)

	rec := h.do("GET", "/trials?vid="+fixture.VisitorID, pagesOrigin, nil, "")
	wantStatus(t, rec, 200)
	var got trialsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != h.cfg.Version || got.BlockSize != h.cfg.BlockSize {
		t.Errorf("version/block_size = %d/%d", got.Version, got.BlockSize)
	}
	if !reflect.DeepEqual(got.Trials, h.a.Trials(fixture.VisitorID, 0, h.cfg.BlockSize)) {
		t.Error("trials differ from the assigner's")
	}
	if !reflect.DeepEqual(got.Visitor, h.a.Visitor(fixture.VisitorID)) {
		t.Error("visitor levels differ from the assigner's")
	}

	rec = h.do("GET", "/trials?vid="+fixture.VisitorID+"&start=20&count=5", pagesOrigin, nil, "")
	wantStatus(t, rec, 200)
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Trials) != 5 || got.Trials[0].Index != 20 || got.Trials[0].Block != 2 {
		t.Errorf("start/count not honored: %d trials, first index %d", len(got.Trials), got.Trials[0].Index)
	}

	for _, q := range []string{
		"", "?vid=nope", "?vid=" + fixture.VisitorID + "&start=-1", "?vid=" + fixture.VisitorID + "&start=x",
		"?vid=" + fixture.VisitorID + "&count=0", "?vid=" + fixture.VisitorID + "&count=51",
		"?vid=" + fixture.VisitorID + "&start=100001",
	} {
		if rec := h.do("GET", "/trials"+q, pagesOrigin, nil, ""); rec.Code != 400 {
			t.Errorf("GET /trials%s = %d, want 400", q, rec.Code)
		}
	}
}

func TestEventFlow(t *testing.T) {
	h := newHarness(t)
	wantStatus(t, h.post(fixture.SessionStart(h.a)), 202)
	wantStatus(t, h.post(fixture.Exposure(h.a, 0)), 202)
	wantStatus(t, h.post(fixture.Choice(h.a)), 202)
	wantStatus(t, h.post(fixture.BlockEnd(h.a)), 202)

	// Retries are safe and don't duplicate.
	wantStatus(t, h.post(fixture.Exposure(h.a, 0)), 202)
	wantStatus(t, h.post(fixture.Choice(h.a)), 202)
	wantStatus(t, h.post(fixture.BlockEnd(h.a)), 202)

	if n := [4]int{len(h.mem.SessionStarts), len(h.mem.Exposures), len(h.mem.Choices), len(h.mem.BlockEnds)}; n != [4]int{1, 1, 1, 1} {
		t.Errorf("stored counts = %v, want one of each", n)
	}
	if got, want := h.mem.Exposures[0].Trial, h.a.Trial(fixture.VisitorID, 0); !reflect.DeepEqual(got, want) {
		t.Error("stored exposure lacks the server-computed trial")
	}
}

func TestEventOrdering(t *testing.T) {
	h := newHarness(t)

	// Children before parents: 409 so the client retries.
	wantStatus(t, h.post(fixture.Exposure(h.a, 0)), 409)
	wantStatus(t, h.post(fixture.Choice(h.a)), 409)
	wantStatus(t, h.post(fixture.BlockEnd(h.a)), 409)

	wantStatus(t, h.post(fixture.SessionStart(h.a)), 202)
	wantStatus(t, h.post(fixture.Choice(h.a)), 409) // still no exposure
	wantStatus(t, h.post(fixture.Exposure(h.a, 0)), 202)

	// A choice can't point at an exposure in a different session.
	other := fixture.Set(fixture.Choice(h.a), "session_id", "4e7b5bed-5cd4-4a6e-9c8f-3456789abcde")
	wantStatus(t, h.post(other), 409)

	wantStatus(t, h.post(fixture.Choice(h.a)), 202)
}

func TestEventRejections(t *testing.T) {
	h := newHarness(t)

	rec := h.post(fixture.Set(fixture.Choice(h.a), "latency_ms", -5))
	wantStatus(t, rec, 400)
	if errCode(rec) != "invalid" || !strings.Contains(rec.Body.String(), "latency_ms") {
		t.Errorf("want invalid with problems, got %s", rec.Body)
	}

	rec = h.post(fixture.Set(fixture.SessionStart(h.a), "ctx.experiment_version", 99))
	wantStatus(t, rec, 400)
	if errCode(rec) != "stale_version" {
		t.Errorf("error = %q, want stale_version", errCode(rec))
	}

	wantStatus(t, h.do("POST", "/event", pagesOrigin, fixture.JSON(fixture.SessionStart(h.a)), "application/x-www-form-urlencoded"), 415)

	huge := fixture.Set(fixture.Choice(h.a), "input", strings.Repeat("x", events.MaxBodyBytes))
	wantStatus(t, h.post(huge), 413)

	wantStatus(t, h.do("GET", "/event", pagesOrigin, nil, ""), 405)
	wantStatus(t, h.do("DELETE", "/trials", pagesOrigin, nil, ""), 405)

	if len(h.mem.SessionStarts)+len(h.mem.Choices) != 0 {
		t.Error("rejected events were stored")
	}
}

// text/plain avoids a CORS preflight (and its extra cold-start round trip).
func TestEventAcceptsTextPlain(t *testing.T) {
	h := newHarness(t)
	wantStatus(t, h.do("POST", "/event", pagesOrigin, fixture.JSON(fixture.SessionStart(h.a)), "text/plain;charset=UTF-8"), 202)
}

func TestStoreDownReturns503(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Store = failingStore{} })
	rec := h.post(fixture.SessionStart(h.a))
	wantStatus(t, rec, 503)
	if errCode(rec) != "store_unavailable" {
		t.Errorf("error = %q", errCode(rec))
	}
}

func TestCORS(t *testing.T) {
	h := newHarness(t)
	trials := "/trials?vid=" + fixture.VisitorID

	t.Run("allowed origin", func(t *testing.T) {
		rec := h.do("GET", trials, pagesOrigin, nil, "")
		wantStatus(t, rec, 200)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != pagesOrigin {
			t.Errorf("ACAO = %q", got)
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Origin") {
			t.Error("missing Vary: Origin")
		}
	})
	t.Run("disallowed origins", func(t *testing.T) {
		for _, o := range []string{
			"https://evil.example", "https://example.github.io.evil.example",
			"http://example.github.io", "https://other.github.io", "null",
		} {
			rec := h.do("GET", trials, o, nil, "")
			if rec.Code != 403 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("origin %q: status %d, ACAO %q", o, rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
			}
		}
	})
	t.Run("disallowed origin can't write", func(t *testing.T) {
		wantStatus(t, h.do("POST", "/event", "https://evil.example", fixture.JSON(fixture.SessionStart(h.a)), "text/plain"), 403)
		if len(h.mem.SessionStarts) != 0 {
			t.Error("event from disallowed origin was stored")
		}
	})
	t.Run("preflight", func(t *testing.T) {
		rec := h.do("OPTIONS", "/event", pagesOrigin, nil, "")
		wantStatus(t, rec, 204)
		if !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "POST") {
			t.Errorf("Allow-Methods = %q", rec.Header().Get("Access-Control-Allow-Methods"))
		}
		wantStatus(t, h.do("OPTIONS", "/event", "https://evil.example", nil, ""), 403)
		wantStatus(t, h.do("OPTIONS", "/event", "", nil, ""), 403)
	})
	t.Run("no origin (curl, health checks)", func(t *testing.T) {
		rec := h.do("GET", trials, "", nil, "")
		wantStatus(t, rec, 200)
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Error("ACAO set without Origin")
		}
	})
}

func TestRateLimit(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Limiter = NewLimiter(0.001, 3) })
	path := "/trials?vid=" + fixture.VisitorID
	for range 3 {
		wantStatus(t, h.do("GET", path, pagesOrigin, nil, ""), 200)
	}
	rec := h.do("GET", path, pagesOrigin, nil, "")
	wantStatus(t, rec, 429)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After")
	}
	wantStatus(t, h.do("GET", "/healthz", "", nil, ""), 200) // health checks are never limited
}

func TestLimiterRefillsAndForgets(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(2, 2)
	l.now = func() time.Time { return now }

	if !l.Allow("a") || !l.Allow("a") || l.Allow("a") {
		t.Fatal("burst of 2 not enforced")
	}
	if !l.Allow("b") {
		t.Fatal("clients must have separate buckets")
	}
	now = now.Add(500 * time.Millisecond) // +1 token at 2/s
	if !l.Allow("a") || l.Allow("a") {
		t.Fatal("refill rate wrong")
	}
	now = now.Add(10 * time.Minute)
	l.Allow("c") // triggers a sweep
	if _, ok := l.buckets["a"]; ok {
		t.Error("idle client IP not forgotten")
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:5555"
	if got := clientIP(r); got != "10.0.0.1" {
		t.Errorf("RemoteAddr: got %q", got)
	}
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Errorf("spoofable leftmost entry used: got %q", got)
	}
}

type failingStore struct{}

var errDown = errors.New("db down")

func (failingStore) InsertSessionStart(context.Context, *events.SessionStart) error { return errDown }
func (failingStore) InsertExposure(context.Context, *events.Exposure) error         { return errDown }
func (failingStore) InsertChoice(context.Context, *events.Choice) error             { return errDown }
func (failingStore) InsertBlockEnd(context.Context, *events.BlockEnd) error         { return errDown }
func (failingStore) Ping(context.Context) error                                     { return errDown }
