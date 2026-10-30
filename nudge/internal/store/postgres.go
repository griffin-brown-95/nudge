package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nudge/internal/assign"
	"nudge/internal/events"
)

// Postgres stores events in Supabase Postgres.
//
// It connects through the Supabase pooler in transaction mode, which can hand
// each statement to a different backend connection. Prepared statements don't
// survive that, so every query uses the simple protocol, and every write is a
// single statement (no multi-statement transactions).
type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(ctx context.Context, url string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("store: parse DATABASE_URL: %w", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.MaxConns = 4 // per Cloud Run instance; the pooler multiplexes
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

// ctxCols are the context columns shared by sessions, exposures and choices,
// in the order ctxArgs returns them.
const ctxCols = `device_class, pointer_type, viewport_w, viewport_h, orientation, layout, theme, referrer_domain, local_hour`

func ctxArgs(c events.Context) []any {
	var ref any
	if c.ReferrerDomain != "" {
		ref = c.ReferrerDomain
	}
	return []any{c.DeviceClass, c.Pointer, c.ViewportW, c.ViewportH, c.Orientation, c.Layout, c.Theme, ref, *c.LocalHour}
}

// placeholders returns "$from, $from+1, ..." for n parameters.
func placeholders(from, n int) string {
	s := ""
	for i := range n {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("$%d", from+i)
	}
	return s
}

func jsonArg(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// level returns a factor's level, or nil (SQL NULL) if the factor isn't in
// the current config.
func level(l assign.Levels, name string) any {
	if v, ok := l[name]; ok {
		return v
	}
	return nil
}

var sessionSQL = `
with v as (
  insert into visitors (visitor_id, experiment_version)
  values ($1::uuid, $3) on conflict do nothing
)
insert into sessions (id, visitor_id, experiment_version, prime, conditions,
  session_number, days_since_first, fonts_ready, start_pointer, ` + ctxCols + `)
values ($2::uuid, $1::uuid, $3, $4, $5::jsonb, $6, $7, $8, $9::jsonb, ` + placeholders(10, 9) + `)
on conflict (id) do nothing`

func (p *Postgres) InsertSessionStart(ctx context.Context, e *events.SessionStart) error {
	conds, err := jsonArg(e.Visitor)
	if err != nil {
		return err
	}
	var start any
	if e.StartPointer != nil {
		if start, err = jsonArg(e.StartPointer); err != nil {
			return err
		}
	}
	args := append([]any{e.VisitorID, e.SessionID, e.Ctx.ExperimentVersion,
		level(e.Visitor, "prime"), conds, e.SessionNumber, *e.DaysSinceFirst, e.FontsReady, start},
		ctxArgs(e.Ctx)...)
	_, err = p.pool.Exec(ctx, sessionSQL, args...)
	return err
}

// Child inserts select from their parent row so a mismatched visitor or
// session inserts nothing. Zero rows then means "duplicate" or "missing
// parent"; existsSQL tells the two apart.

var exposureSQL = `
insert into exposures (id, session_id, visitor_id, experiment_version, trial_index, block_index,
  opt1_color, opt1_emphasis, opt1_size, opt2_color, opt2_emphasis, opt2_size,
  labels, label_order, default_focus, prime, conditions, rendered, informed, ` + ctxCols + `)
select $1::uuid, s.id, s.visitor_id, $4, $5, $6,
  $7, $8, $9, $10, $11, $12,
  $13, $14, $15, $16, $17::jsonb, $18::jsonb, $19, ` + placeholders(20, 9) + `
from sessions s where s.id = $2::uuid and s.visitor_id = $3::uuid
on conflict (id) do nothing`

func (p *Postgres) InsertExposure(ctx context.Context, e *events.Exposure) error {
	conds, err := jsonArg(map[string]any{
		"options": e.Trial.Options, "trial": e.Trial.Trial, "visitor": e.Visitor,
	})
	if err != nil {
		return err
	}
	rendered, err := jsonArg(e.Rendered)
	if err != nil {
		return err
	}
	o1, o2, tr := e.Trial.Options[0], e.Trial.Options[1], e.Trial.Trial
	args := append([]any{e.ExposureID, e.SessionID, e.VisitorID, e.Ctx.ExperimentVersion,
		e.Trial.Index, e.Trial.Block,
		level(o1, "color"), level(o1, "emphasis"), level(o1, "size"),
		level(o2, "color"), level(o2, "emphasis"), level(o2, "size"),
		level(tr, "labels"), level(tr, "label_order"), level(tr, "default_focus"),
		level(e.Visitor, "prime"), conds, rendered, e.Informed},
		ctxArgs(e.Ctx)...)
	return p.insertChild(ctx, exposureSQL, args,
		`select exists (select 1 from exposures where id = $1::uuid)`, e.ExposureID)
}

var choiceSQL = `
insert into choices (exposure_id, session_id, visitor_id, chosen_slot, latency_ms, input,
  interaction_trace, ` + ctxCols + `)
select x.id, x.session_id, x.visitor_id, $4, $5, $6, $7::jsonb, ` + placeholders(8, 9) + `
from exposures x where x.id = $1::uuid and x.session_id = $2::uuid and x.visitor_id = $3::uuid
on conflict (exposure_id) do nothing`

func (p *Postgres) InsertChoice(ctx context.Context, e *events.Choice) error {
	trace, err := jsonArg(e.Trace)
	if err != nil {
		return err
	}
	args := append([]any{e.ExposureID, e.SessionID, e.VisitorID, e.ChosenSlot, *e.LatencyMS, e.Input, trace},
		ctxArgs(e.Ctx)...)
	return p.insertChild(ctx, choiceSQL, args,
		`select exists (select 1 from choices where exposure_id = $1::uuid)`, e.ExposureID)
}

var blockEndSQL = `
insert into block_ends (session_id, block_index, visitor_id, action)
select s.id, $3, s.visitor_id, $4
from sessions s where s.id = $1::uuid and s.visitor_id = $2::uuid
on conflict (session_id, block_index) do nothing`

func (p *Postgres) InsertBlockEnd(ctx context.Context, e *events.BlockEnd) error {
	return p.insertChild(ctx, blockEndSQL, []any{e.SessionID, e.VisitorID, *e.BlockIndex, e.Action},
		`select exists (select 1 from block_ends where session_id = $1::uuid and block_index = $2)`,
		e.SessionID, *e.BlockIndex)
}

func (p *Postgres) insertChild(ctx context.Context, insert string, args []any, exists string, existsArgs ...any) error {
	tag, err := p.pool.Exec(ctx, insert, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var dup bool
	if err := p.pool.QueryRow(ctx, exists, existsArgs...).Scan(&dup); err != nil {
		return err
	}
	if dup {
		return nil
	}
	return ErrMissingParent
}
