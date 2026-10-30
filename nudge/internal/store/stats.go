package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"nudge/internal/config"
	"nudge/internal/stats"
)

// StatsReader builds the public results snapshot for the current version.
type StatsReader interface {
	Stats(ctx context.Context, cfg *config.Config) (*stats.Snapshot, error)
}

// Stats reads counts from the analysis views (see supabase/migrations) and
// hands them to package stats, which computes rates and hides small samples.
// It runs about a dozen small queries; the HTTP layer caches the result.
func (p *Postgres) Stats(ctx context.Context, cfg *config.Config) (*stats.Snapshot, error) {
	v := cfg.Version
	s := &stats.Snapshot{Version: v, GeneratedAt: time.Now().UTC(), MinN: stats.MinN}

	var shownOld, abandonedOld int64
	err := p.pool.QueryRow(ctx, `
select
  (select count(distinct visitor_id) from sessions where experiment_version = $1),
  (select count(*) from sessions where experiment_version = $1),
  (select count(*) from exposures where experiment_version = $1),
  (select count(*) from choices c join exposures x on x.id = c.exposure_id where x.experiment_version = $1),
  (select count(*) from choices c join exposures x on x.id = c.exposure_id
     where x.experiment_version = $1 and c.server_ts > now() - interval '24 hours'),
  (select count(*) from exposures where experiment_version = $1 and server_ts < now() - interval '10 minutes'),
  (select count(*) from exposures x where x.experiment_version = $1 and x.server_ts < now() - interval '10 minutes'
     and not exists (select 1 from choices c where c.exposure_id = x.id))`, v).
		Scan(&s.Progress.Visitors, &s.Progress.Sessions, &s.Progress.TrialsShown, &s.Progress.Choices,
			&s.Progress.ChoicesLast24, &shownOld, &abandonedOld)
	if err != nil {
		return nil, fmt.Errorf("stats progress: %w", err)
	}
	if shownOld > 0 {
		a := float64(abandonedOld) / float64(shownOld)
		s.Progress.Abandonment = &a
	}

	err = p.each(ctx, "daily", `
select to_char(d, 'YYYY-MM-DD'), count(c.exposure_id), count(distinct c.visitor_id)
from generate_series((now() at time zone 'utc')::date - 29, (now() at time zone 'utc')::date, interval '1 day') d
left join (
  choices c join exposures x on x.id = c.exposure_id and x.experiment_version = $1
) on (c.server_ts at time zone 'utc')::date = d::date
group by d order by d`, v, func(rows pgx.Rows) error {
		var d stats.Day
		if err := rows.Scan(&d.Date, &d.Choices, &d.Visitors); err != nil {
			return err
		}
		s.Daily = append(s.Daily, d)
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, f := range cfg.Factors {
		b, err := p.balance(ctx, v, f)
		if err != nil {
			return nil, err
		}
		s.Balance = append(s.Balance, b)
	}

	// Rollup rows with a null device_class are the frame × layout breakdown
	// and its totals.
	err = p.each(ctx, "position", `
select frame, layout, n, first_picked from v_position_bias
where experiment_version = $1 and device_class is null
order by frame nulls first, layout nulls first`, v, func(rows pgx.Rows) error {
		var r stats.PositionRate
		var n, hits int64
		if err := rows.Scan(&r.Frame, &r.Layout, &n, &hits); err != nil {
			return err
		}
		r.Rate = stats.NewRate(hits, n)
		s.Position = append(s.Position, r)
		return nil
	})
	if err != nil {
		return nil, err
	}

	err = p.each(ctx, "attributes", `
select frame, attribute, level, n, wins from v_attribute_win_rates
where experiment_version = $1
order by attribute, frame nulls first, level`, v, func(rows pgx.Rows) error {
		var r stats.LevelRate
		var n, wins int64
		if err := rows.Scan(&r.Frame, &r.Attribute, &r.Level, &n, &wins); err != nil {
			return err
		}
		r.Rate = stats.NewRate(wins, n)
		s.Attributes = append(s.Attributes, r)
		return nil
	})
	if err != nil {
		return nil, err
	}

	var counts []stats.MatchupCounts
	err = p.each(ctx, "matchups", `
select attribute, level_a, level_b, n, a_wins from v_matchups
where experiment_version = $1`, v, func(rows pgx.Rows) error {
		var m stats.MatchupCounts
		if err := rows.Scan(&m.Attribute, &m.A, &m.B, &m.N, &m.AWins); err != nil {
			return err
		}
		counts = append(counts, m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.Matchups = stats.NewMatchups(counts)

	err = p.each(ctx, "decisions", `
select frame, input, n, median_latency_ms,
  considered_both_share::float8, changed_mind_share::float8, first_considered_wins::float8
from v_trace_summary where experiment_version = $1
order by frame nulls first, input nulls first`, v, func(rows pgx.Rows) error {
		var d stats.Decision
		if err := rows.Scan(&d.Frame, &d.Input, &d.N, &d.MedianLatencyMS,
			&d.ConsideredBoth, &d.ChangedMind, &d.FirstConsideredWins); err != nil {
			return err
		}
		s.Decisions = append(s.Decisions, d.Gate())
		return nil
	})
	if err != nil {
		return nil, err
	}

	return s, nil
}

// balance counts how often each level of f was assigned, from the
// `conditions` jsonb, so it works for any factor in the config, including
// ones without their own column. Option factors count both options; visitor
// factors count each visitor once.
func (p *Postgres) balance(ctx context.Context, version int, f config.Factor) (stats.FactorBalance, error) {
	var q string
	switch f.Scope {
	case config.ScopeOption:
		q = `select o ->> $2, count(*) from exposures x, jsonb_array_elements(x.conditions -> 'options') o
		     where x.experiment_version = $1 group by 1`
	case config.ScopeTrial:
		q = `select conditions -> 'trial' ->> $2, count(*) from exposures
		     where experiment_version = $1 group by 1`
	default:
		q = `select l, count(*) from (
		       select distinct visitor_id, conditions -> 'visitor' ->> $2 as l
		       from exposures where experiment_version = $1) v group by 1`
	}
	got := map[string]int64{}
	err := p.each(ctx, "balance "+f.Name, q, version, func(rows pgx.Rows) error {
		var level *string
		var n int64
		if err := rows.Scan(&level, &n); err != nil {
			return err
		}
		if level != nil {
			got[*level] = n
		}
		return nil
	}, f.Name)
	if err != nil {
		return stats.FactorBalance{}, err
	}
	levels := make([]stats.LevelCount, len(f.Levels))
	for i, l := range f.Levels {
		levels[i] = stats.LevelCount{Level: l, N: got[l]}
	}
	return stats.NewBalance(f.Name, string(f.Scope), levels), nil
}

// each runs a query whose first parameter is the experiment version (plus
// any extra args), calls fn for every row, and always closes the rows.
func (p *Postgres) each(ctx context.Context, what, q string, version int, fn func(pgx.Rows) error, extra ...any) error {
	rows, err := p.pool.Query(ctx, q, append([]any{version}, extra...)...)
	if err != nil {
		return fmt.Errorf("stats %s: %w", what, err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return fmt.Errorf("stats %s: %w", what, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("stats %s: %w", what, err)
	}
	return nil
}

// Stats for the in-memory store reports progress counts only; it exists so
// `make run-memory` serves a well-formed /stats for frontend work.
func (m *Memory) Stats(_ context.Context, cfg *config.Config) (*stats.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &stats.Snapshot{
		Version:     cfg.Version,
		GeneratedAt: time.Now().UTC(),
		MinN:        stats.MinN,
		Progress: stats.Progress{
			Visitors:    int64(len(m.visitors)),
			Sessions:    int64(len(m.sessions)),
			TrialsShown: int64(len(m.exposures)),
			Choices:     int64(len(m.choices)),
		},
	}, nil
}
