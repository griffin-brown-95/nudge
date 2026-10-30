// Command simulate fills a LOCAL database with synthetic visitors, so the
// dashboard and analysis views can be checked against data with known biases.
//
// Every event goes through the real validator and store, exactly as if it had
// come over HTTP. Choices follow a simple model with deliberate effects:
//
//	first-rendered option     +0.25 (logit)
//	solid emphasis            +0.30 vs. soft/outline
//	size +8%                  +0.15
//	color: blue +0.25, red +0.10, gray -0.30
//
// If the dashboard shows those, the pipeline from event to chart is right.
//
//	go run ./cmd/simulate -visitors 300
//
// It refuses to run unless DATABASE_URL points at localhost.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"net/url"
	"os"

	"nudge/internal/assign"
	"nudge/internal/config"
	"nudge/internal/events"
	"nudge/internal/store"
)

func main() {
	visitors := flag.Int("visitors", 300, "number of synthetic visitors")
	seed := flag.Uint64("seed", 1, "random seed")
	cfgPath := flag.String("config", "experiments.yaml", "experiment config")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	u, err := url.Parse(dbURL)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		log.Fatal("simulate only runs against a local database (DATABASE_URL host must be localhost)")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	pg, err := store.NewPostgres(ctx, dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pg.Close()

	sim := &simulator{
		a:   assign.New(cfg),
		v:   &events.Validator{A: assign.New(cfg)},
		st:  pg,
		r:   rand.New(rand.NewPCG(*seed, 99)),
		cfg: cfg,
	}
	choices := 0
	for range *visitors {
		n, err := sim.visitor(ctx)
		if err != nil {
			log.Fatal(err)
		}
		choices += n
	}
	fmt.Printf("simulated %d visitors, %d choices\n", *visitors, choices)
}

type simulator struct {
	a   *assign.Assigner
	v   *events.Validator
	st  store.Store
	r   *rand.Rand
	cfg *config.Config
}

func (s *simulator) uuid() string {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(s.r.UintN(256))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// send round-trips an event through JSON and the validator, like the API does.
func (s *simulator) send(ctx context.Context, ev map[string]any) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	parsed, err := s.v.Parse(body)
	if err != nil {
		return fmt.Errorf("%s rejected: %w", ev["type"], err)
	}
	return store.Insert(ctx, s.st, parsed)
}

func (s *simulator) visitor(ctx context.Context) (int, error) {
	vid, sid := s.uuid(), s.uuid()
	mobile := s.r.Float64() < 0.55
	ctxFields := map[string]any{
		"device_class": "desktop", "pointer": "fine", "viewport_w": 1440, "viewport_h": 900,
		"orientation": "landscape", "layout": "side_by_side", "theme": pick(s.r, "light", "dark"),
		"referrer_domain": "", "local_hour": s.r.IntN(24), "experiment_version": s.cfg.Version,
	}
	if mobile {
		ctxFields["device_class"], ctxFields["pointer"] = "phone", "coarse"
		ctxFields["viewport_w"], ctxFields["viewport_h"] = 390, 844
		ctxFields["orientation"], ctxFields["layout"] = "portrait", "stacked"
	}
	err := s.send(ctx, map[string]any{
		"type": "session_start", "visitor_id": vid, "session_id": sid, "session_number": 1,
		"days_since_first": 0, "fonts_ready": true, "start_pointer": nil, "ctx": ctxFields,
	})
	if err != nil {
		return 0, err
	}

	trials := s.cfg.BlockSize * (1 + s.r.IntN(3))
	for i := range trials {
		t := s.a.Trial(vid, i)
		xid := s.uuid()
		err := s.send(ctx, map[string]any{
			"type": "exposure", "visitor_id": vid, "session_id": sid, "exposure_id": xid,
			"trial_index": t.Index, "block_index": t.Block,
			"shown":    map[string]any{"options": t.Options, "trial": t.Trial},
			"rendered": map[string]any{"render_delay_ms": 4 + s.r.IntN(10), "pointer": nil, "focus_applied": "none"},
			"informed": false, "ctx": ctxFields,
		})
		if err != nil {
			return 0, err
		}
		if s.r.Float64() < 0.03 { // abandoned trial
			return i, nil
		}

		chosen := s.choose(t)
		latency := int(math.Exp(6.8 + 0.5*s.r.NormFloat64())) // median ~900ms
		trace := map[string]any{}
		input := "touch"
		if !mobile {
			input = "mouse"
			first := 1 + s.r.IntN(2)
			ev := []map[string]any{{"t": latency / 5, "e": "enter", "slot": first}}
			dwell := []int{0, 0}
			if first != chosen || s.r.Float64() < 0.3 {
				ev = append(ev, map[string]any{"t": latency / 2, "e": "leave", "slot": first},
					map[string]any{"t": latency/2 + 20, "e": "enter", "slot": chosen})
				dwell[first-1] = latency/2 - latency/5
				dwell[chosen-1] += latency - latency/2 - 20
			} else {
				dwell[first-1] = latency - latency/5
			}
			ev = append(ev, map[string]any{"t": latency - 30, "e": "down", "slot": chosen})
			trace["pointer"], trace["dwell_ms"] = ev, dwell
		} else {
			trace["first_touch_ms"] = latency - 60
			trace["scroll_px"] = 0
			trace["tap"] = map[string]any{"slot": chosen, "x": 0.5, "y": 0.5}
		}
		err = s.send(ctx, map[string]any{
			"type": "choice", "visitor_id": vid, "session_id": sid, "exposure_id": xid,
			"chosen_slot": chosen, "latency_ms": latency, "input": input, "trace": trace, "ctx": ctxFields,
		})
		if err != nil {
			return 0, err
		}
		if (i+1)%s.cfg.BlockSize == 0 {
			action := "keep_going"
			if i+1 == trials {
				action = "done"
			}
			if err := s.send(ctx, map[string]any{
				"type": "block_end", "visitor_id": vid, "session_id": sid,
				"block_index": t.Block, "action": action, "ctx": ctxFields,
			}); err != nil {
				return 0, err
			}
		}
	}
	return trials, nil
}

// choose applies the built-in effects as a logistic model on option 1 vs 2.
func (s *simulator) choose(t assign.Trial) int {
	util := func(o assign.Levels) float64 {
		u := map[string]float64{"blue": 0.25, "red": 0.10, "gray": -0.30}[o["color"]]
		if o["emphasis"] == "solid" {
			u += 0.30
		}
		if o["size"] == "plus8" {
			u += 0.15
		}
		return u
	}
	logit := 0.25 + util(t.Options[0]) - util(t.Options[1])
	if s.r.Float64() < 1/(1+math.Exp(-logit)) {
		return 1
	}
	return 2
}

func pick(r *rand.Rand, xs ...string) string { return xs[r.IntN(len(xs))] }
