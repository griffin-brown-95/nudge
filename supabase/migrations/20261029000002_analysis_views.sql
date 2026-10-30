-- Analysis views. See docs/EXPERIMENT.md §7.
--
-- Conventions:
--   * Only a visitor's FIRST exposure of each trial counts (a reload re-renders
--     the same trial; later renders are kept in exposures but not analyzed).
--   * Trials shown after the visitor read the "what was varied" note
--     (informed = true) are excluded from the attribute and matchup views.
--   * Confidence bounds are 95% Wilson score intervals.

create function wilson_lower(wins bigint, n bigint) returns double precision
language sql immutable as $$
  select case when n = 0 then null else
    (wins::float8 / n + 1.96^2 / (2 * n)
      - 1.96 * sqrt((wins::float8 / n) * (1 - wins::float8 / n) / n + 1.96^2 / (4 * n^2)))
    / (1 + 1.96^2 / n)
  end
$$;

create function wilson_upper(wins bigint, n bigint) returns double precision
language sql immutable as $$
  select case when n = 0 then null else 1 - wilson_lower(n - wins, n) end
$$;

-- One row per exposure with its outcome.
create view v_trials with (security_invoker = true) as
select
  e.*,
  c.chosen_slot,
  c.latency_ms,
  c.input,
  c.exposure_id is not null as answered,
  row_number() over (
    partition by e.visitor_id, e.experiment_version, e.trial_index
    order by e.server_ts, e.id
  ) = 1 as first_exposure,
  (e.opt1_color = e.opt2_color and e.opt1_emphasis = e.opt2_emphasis
    and e.opt1_size = e.opt2_size) as identical_options
from exposures e
left join choices c on c.exposure_id = e.id;

-- Each analyzed trial contributes two rows per attribute: one per option.
create view v_appearances with (security_invoker = true) as
select
  t.id as exposure_id,
  t.visitor_id,
  t.experiment_version,
  t.trial_index,
  t.frame,
  a.attribute,
  a.level,
  a.opponent,
  a.slot,
  t.chosen_slot = a.slot as won
from v_trials t
cross join lateral (values
  ('color',    t.opt1_color,    t.opt2_color,    1),
  ('color',    t.opt2_color,    t.opt1_color,    2),
  ('emphasis', t.opt1_emphasis, t.opt2_emphasis, 1),
  ('emphasis', t.opt2_emphasis, t.opt1_emphasis, 2),
  ('size',     t.opt1_size,     t.opt2_size,     1),
  ('size',     t.opt2_size,     t.opt1_size,     2)
) as a(attribute, level, opponent, slot)
where t.answered and t.first_exposure and not t.informed;

-- Q1: how often is the first-rendered option picked? Rows with a null
-- frame/layout/device_class are rollup totals across that column.
create view v_position_bias with (security_invoker = true) as
select
  experiment_version,
  frame,
  layout,
  device_class,
  count(*) as n,
  count(*) filter (where chosen_slot = 1) as first_picked,
  round(avg((chosen_slot = 1)::int), 4) as first_share,
  wilson_lower(count(*) filter (where chosen_slot = 1), count(*)) as first_share_lo,
  wilson_upper(count(*) filter (where chosen_slot = 1), count(*)) as first_share_hi,
  count(*) filter (where identical_options) as n_identical,
  round(avg((chosen_slot = 1)::int) filter (where identical_options), 4) as first_share_identical
from v_trials
where answered and first_exposure and not informed
group by rollup (experiment_version, frame, layout, device_class);

-- Q2: win rate of each level when its opponent has a different level,
-- overall (frame is null) and within each frame.
create view v_attribute_win_rates with (security_invoker = true) as
select
  experiment_version,
  frame,
  attribute,
  level,
  count(*) as n,
  count(*) filter (where won) as wins,
  round(avg(won::int), 4) as win_rate,
  wilson_lower(count(*) filter (where won), count(*)) as win_rate_lo,
  wilson_upper(count(*) filter (where won), count(*)) as win_rate_hi
from v_appearances
where level <> opponent
group by grouping sets (
  (experiment_version, attribute, level),
  (experiment_version, frame, attribute, level)
);

-- Q4: head-to-head matchups, each trial counted once, ranked by how
-- one-sided they are. Ranking uses the Wilson lower bound so small samples
-- can't top the list; matchups under 30 trials are unranked.
create view v_matchups with (security_invoker = true) as
with m as (
  select
    experiment_version,
    attribute,
    least(level, opponent)    as level_a,
    greatest(level, opponent) as level_b,
    count(*) as n,
    count(*) filter (where won = (level < opponent)) as a_wins
  from v_appearances
  where slot = 1 and level <> opponent
  group by 1, 2, 3, 4
)
select
  experiment_version,
  attribute,
  level_a,
  level_b,
  n,
  a_wins,
  n - a_wins as b_wins,
  case when a_wins >= n - a_wins then level_a else level_b end as winner,
  round(greatest(a_wins, n - a_wins)::numeric / n, 4) as winner_share,
  wilson_lower(greatest(a_wins, n - a_wins), n) as winner_share_lo,
  case when n >= 30 then
    rank() over (
      partition by experiment_version, (n >= 30)
      order by wilson_lower(greatest(a_wins, n - a_wins), n) desc
    )
  end as one_sided_rank
from m;

revoke all on v_trials, v_appearances, v_position_bias, v_attribute_win_rates, v_matchups
  from anon, authenticated;
revoke execute on function wilson_lower(bigint, bigint), wilson_upper(bigint, bigint)
  from public, anon, authenticated;
