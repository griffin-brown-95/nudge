package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"nudge/internal/assign"
	"nudge/internal/config"
	"nudge/internal/events"
	"nudge/internal/fixture"
	"nudge/internal/store"
)

// Runs against a real database with the migrations applied, e.g. the local
// Supabase DB:
//
//	NUDGE_TEST_DATABASE_URL=postgresql://postgres:postgres@127.0.0.1:54322/postgres go test ./internal/store
//
// It writes rows with random IDs and never deletes anything, but don't point
// it at production — use the local DB and `supabase db reset` afterwards.
func TestPostgres(t *testing.T) {
	url := os.Getenv("NUDGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("NUDGE_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := config.Load("../../experiments.yaml")
	if err != nil {
		t.Fatal(err)
	}
	a := assign.New(cfg)
	v := &events.Validator{A: a}
	pg, err := store.NewPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err := pg.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	// Fresh IDs per run so reruns don't collide.
	ids := freshIDs(t, ctx, url, 3)
	vid, sid, xid := ids[0], ids[1], ids[2]
	withIDs := func(m map[string]any) map[string]any {
		m = fixture.Set(m, "visitor_id", vid)
		m = fixture.Set(m, "session_id", sid)
		if _, ok := m["exposure_id"]; ok {
			m = fixture.Set(m, "exposure_id", xid)
		}
		return m
	}
	parse := func(m map[string]any) events.Event {
		t.Helper()
		ev, err := v.Parse(fixture.JSON(m))
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	insert := func(m map[string]any) error {
		t.Helper()
		return store.Insert(ctx, pg, parse(m))
	}

	// The exposure fixture is built for fixture.VisitorID, so rebuild the
	// shown trial for this run's visitor.
	tr := a.Trial(vid, 3)
	exposure := withIDs(fixture.Exposure(a, 3))
	exposure = fixture.Set(exposure, "shown", map[string]any{"options": tr.Options, "trial": tr.Trial})

	if err := insert(exposure); !errors.Is(err, store.ErrMissingParent) {
		t.Fatalf("exposure before session: want ErrMissingParent, got %v", err)
	}
	for _, m := range []map[string]any{
		withIDs(fixture.SessionStart(a)), exposure, withIDs(fixture.Choice(a)), withIDs(fixture.BlockEnd(a)),
	} {
		if err := insert(m); err != nil {
			t.Fatalf("%s: %v", m["type"], err)
		}
		if err := insert(m); err != nil {
			t.Fatalf("%s retry: %v", m["type"], err)
		}
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	var n int
	conn.QueryRow(ctx, `select count(*) from choices where visitor_id = $1`, vid).Scan(&n)
	if n != 1 {
		t.Errorf("choices for visitor = %d, want 1 (retries must not duplicate)", n)
	}

	var color1, prime string
	var conds map[string]any
	conn.QueryRow(ctx, `select opt1_color, prime, conditions from exposures where id = $1`, xid).Scan(&color1, &prime, &conds)
	if color1 != tr.Options[0]["color"] || prime != a.Visitor(vid)["prime"] {
		t.Errorf("stored columns opt1_color=%q prime=%q, want %q %q", color1, prime, tr.Options[0]["color"], a.Visitor(vid)["prime"])
	}
	if conds["options"] == nil || conds["trial"] == nil || conds["visitor"] == nil {
		t.Errorf("conditions jsonb incomplete: %v", conds)
	}

	conn.QueryRow(ctx, `select count(*) from v_appearances where exposure_id = $1`, xid).Scan(&n)
	if n != 6 {
		t.Errorf("v_appearances rows for exposure = %d, want 6 (3 attributes × 2 options)", n)
	}
	for _, view := range []string{"v_trials", "v_position_bias", "v_attribute_win_rates", "v_matchups"} {
		if _, err := conn.Exec(ctx, "select * from "+view+" limit 1"); err != nil {
			t.Errorf("%s: %v", view, err)
		}
	}

	// The Supabase API roles must not see anything.
	if _, err := conn.Exec(ctx, `set role anon`); err == nil {
		_, err := conn.Exec(ctx, `select 1 from choices limit 1`)
		conn.Exec(ctx, `reset role`)
		if err == nil {
			t.Error("anon role can read choices")
		}
	}
}

func freshIDs(t *testing.T, ctx context.Context, url string, n int) []string {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	ids := make([]string, n)
	for i := range ids {
		if err := conn.QueryRow(ctx, `select gen_random_uuid()::text`).Scan(&ids[i]); err != nil {
			t.Fatal(fmt.Errorf("gen uuid: %w", err))
		}
	}
	return ids
}
