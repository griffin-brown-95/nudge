-- Summaries of HOW people chose, derived from choices.interaction_trace.
--
-- "Considered" means hovered with a mouse or pen, or focused with the
-- keyboard. Touch has no equivalent (fingers don't hover), so those columns
-- are null for touch choices.

create view v_choice_traces with (security_invoker = true) as
select
  t.id as exposure_id,
  t.visitor_id,
  t.experiment_version,
  t.trial_index,
  t.frame,
  t.layout,
  t.device_class,
  t.first_exposure,
  t.informed,
  c.chosen_slot,
  c.latency_ms,
  c.input,
  s.first_considered,                 -- slot hovered/focused first
  s.considered_both,                  -- looked at both options before choosing
  s.switches,                         -- times attention moved between options
  s.first_considered <> c.chosen_slot as changed_mind,
  s.started_on_option,                -- pointer was already on an option when they appeared
  case when d.total > 0 then round(d.chosen / d.total, 4) end as dwell_chosen_share
from v_trials t
join choices c on c.exposure_id = t.id
cross join lateral (
  select
    (array_agg(slot order by ord))[1] as first_considered,
    case when count(*) > 0 then count(distinct slot) = 2 end as considered_both,
    case when count(*) > 0 then count(*) filter (where slot <> prev) end as switches,
    case when count(*) > 0 then coalesce(bool_or(t_ms < 100), false) end as started_on_option
  from (
    select slot, ord, t_ms, lag(slot) over (order by ord) as prev
    from (
      select (e ->> 'slot')::int as slot, ord, (e ->> 't')::int as t_ms
      from jsonb_array_elements(coalesce(c.interaction_trace -> 'pointer', '[]')) with ordinality as a(e, ord)
      where c.input in ('mouse', 'pen') and e ->> 'e' = 'enter'
      union all
      select (f #>> '{}')::int, ord, null
      from jsonb_array_elements(coalesce(c.interaction_trace -> 'focus_seq', '[]')) with ordinality as b(f, ord)
      where c.input = 'keyboard'
    ) considered
  ) ordered
) s
cross join lateral (
  select
    (c.interaction_trace -> 'dwell_ms' ->> (c.chosen_slot - 1))::numeric as chosen,
    (c.interaction_trace -> 'dwell_ms' ->> 0)::numeric
      + (c.interaction_trace -> 'dwell_ms' ->> 1)::numeric as total
) d;

-- Decision style by frame and input: how fast, how often people look at
-- both options, and how often the first option considered is the one chosen.
create view v_trace_summary with (security_invoker = true) as
select
  experiment_version,
  frame,
  input,
  count(*) as n,
  percentile_cont(0.5) within group (order by latency_ms) as median_latency_ms,
  round(avg(considered_both::int), 4) as considered_both_share,
  round(avg(changed_mind::int), 4) as changed_mind_share,
  round(avg((first_considered = chosen_slot)::int), 4) as first_considered_wins,
  round(avg(started_on_option::int), 4) as started_on_option_share
from v_choice_traces
where first_exposure and not informed
group by rollup (experiment_version, frame, input);

revoke all on v_choice_traces, v_trace_summary from anon, authenticated;
