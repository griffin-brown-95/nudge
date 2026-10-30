// Package httpapi serves the nudge HTTP API:
//
//	GET  /healthz                       liveness (?deep=1 also pings the DB)
//	GET  /trials?vid=&start=&count=     a visitor's next block of trials
//	POST /event                         one event (session_start, exposure, choice, block_end)
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nudge/internal/assign"
	"nudge/internal/events"
	"nudge/internal/store"
)

const maxTrialsPerRequest = 50

type Options struct {
	Assigner       *assign.Assigner
	BlockSize      int
	Store          store.Store
	AllowedOrigins []string
	Limiter        *Limiter // nil disables rate limiting
	Logger         *slog.Logger
}

type Server struct {
	opts      Options
	validator *events.Validator
	allowed   map[string]bool
}

func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	allowed := map[string]bool{}
	for _, o := range opts.AllowedOrigins {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			allowed[o] = true
		}
	}
	return &Server{opts: opts, validator: &events.Validator{A: opts.Assigner}, allowed: allowed}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET /trials", s.limit(http.HandlerFunc(s.trials)))
	mux.Handle("POST /event", s.limit(http.HandlerFunc(s.event)))
	mux.HandleFunc("OPTIONS /", func(w http.ResponseWriter, r *http.Request) {}) // answered by cors
	return s.logRequests(s.cors(mux))
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("deep") == "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := s.opts.Store.Ping(ctx); err != nil {
			s.opts.Logger.Warn("health: store ping failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, errBody("store_unavailable"))
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, "ok\n")
}

type trialsResponse struct {
	Version   int            `json:"version"`
	BlockSize int            `json:"block_size"`
	Visitor   assign.Levels  `json:"visitor"`
	Trials    []assign.Trial `json:"trials"`
}

func (s *Server) trials(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	vid := q.Get("vid")
	if !events.ValidUUID(vid) {
		writeJSON(w, http.StatusBadRequest, errBody("invalid_vid"))
		return
	}
	start, ok := intParam(q.Get("start"), 0, 0, assign.MaxTrialIndex)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errBody("invalid_start"))
		return
	}
	count, ok := intParam(q.Get("count"), s.opts.BlockSize, 1, maxTrialsPerRequest)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errBody("invalid_count"))
		return
	}
	count = min(count, assign.MaxTrialIndex-start+1)

	a := s.opts.Assigner
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, trialsResponse{
		Version:   a.Version(),
		BlockSize: s.opts.BlockSize,
		Visitor:   a.Visitor(vid),
		Trials:    a.Trials(vid, start, count),
	})
}

// event responds:
//
//	202 stored (or already stored — retries are safe)
//	400 invalid or from a stale experiment version — client drops it
//	409 parent event not stored yet — client retries later
//	413 body too large
//	503 database unavailable — client retries later
func (s *Server) event(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") && !strings.HasPrefix(ct, "text/plain") {
		writeJSON(w, http.StatusUnsupportedMediaType, errBody("unsupported_content_type"))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, events.MaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody("too_large"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errBody("unreadable_body"))
		return
	}

	ev, err := s.validator.Parse(body)
	var verr *events.ValidationError
	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid", "problems": verr.Problems})
		return
	case errors.Is(err, events.ErrStaleVersion):
		writeJSON(w, http.StatusBadRequest, errBody("stale_version"))
		return
	case err != nil:
		writeJSON(w, http.StatusBadRequest, errBody("invalid"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	switch err := store.Insert(ctx, s.opts.Store, ev); {
	case errors.Is(err, store.ErrMissingParent):
		writeJSON(w, http.StatusConflict, errBody("missing_parent"))
	case err != nil:
		s.opts.Logger.Error("store insert failed", "kind", ev.Kind(), "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errBody("store_unavailable"))
	default:
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	}
}

// cors allows only configured origins. Requests without an Origin header
// (health checks, curl) pass through without CORS headers; browsers always
// send Origin on cross-origin fetches, so a disallowed site is refused.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		w.Header().Add("Vary", "Origin")
		if origin != "" {
			if !s.allowed[origin] {
				writeJSON(w, http.StatusForbidden, errBody("origin_not_allowed"))
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		if r.Method == http.MethodOptions {
			if origin == "" {
				writeJSON(w, http.StatusForbidden, errBody("origin_required"))
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) limit(next http.Handler) http.Handler {
	if s.opts.Limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.opts.Limiter.Allow(clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, errBody("rate_limited"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// logRequests logs method, path, status and duration. Never the client IP
// or query string (which carries the visitor ID).
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.opts.Logger.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "ms", time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func intParam(s string, def, lo, hi int) (int, bool) {
	if s == "" {
		return def, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, false
	}
	return n, true
}

func errBody(code string) map[string]string { return map[string]string{"error": code} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
