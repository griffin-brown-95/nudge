-- A or B? event schema. See docs/EXPERIMENT.md.
--
-- Only the nudge Go service touches these tables (as the owner role). RLS is
-- on with no policies and the Supabase API roles have no grants, so the
-- browser-facing Data API can't read or write anything here.
--
-- Factor columns are nullable: the service fills the ones it knows about and
-- always writes the full condition set to `conditions` jsonb, so a factor added
-- in experiments.yaml is captured before it gets its own column.

create table visitors (
  visitor_id         uuid primary key,
  first_seen         timestamptz not null default now(),
  experiment_version int not null
);

create table sessions (
  id                 uuid primary key,
  visitor_id         uuid not null references visitors on delete cascade,
  experiment_version int not null,
  prime              text,
  conditions         jsonb not null,           -- visitor-scoped levels
  session_number     int not null check (session_number >= 1),
  days_since_first   int not null check (days_since_first >= 0),
  fonts_ready        boolean not null,
  start_pointer      jsonb,                    -- {x, y} as viewport fractions; null on touch
  -- context
  device_class       text not null,
  pointer_type       text not null,
  viewport_w         int not null,
  viewport_h         int not null,
  orientation        text not null,
  layout             text not null,
  theme              text not null,
  referrer_domain    text,
  local_hour         smallint not null check (local_hour between 0 and 23),
  server_ts          timestamptz not null default now()
);
create index sessions_visitor_idx on sessions (visitor_id);

create table exposures (
  id                 uuid primary key,
  session_id         uuid not null references sessions on delete cascade,
  visitor_id         uuid not null references visitors on delete cascade,
  experiment_version int not null,
  trial_index        int not null check (trial_index >= 0),
  block_index        int not null check (block_index >= 0),
  -- opt1 is always the option rendered first (left, or top when stacked)
  opt1_color         text,
  opt1_emphasis      text,
  opt1_size          text,
  opt2_color         text,
  opt2_emphasis      text,
  opt2_size          text,
  labels             text,
  label_order        text,
  default_focus      text,
  prime              text,
  conditions         jsonb not null,           -- {options: [{..},{..}], trial: {..}, visitor: {..}}
  rendered           jsonb not null,           -- render_delay_ms, pointer at render, focus_applied
  informed           boolean not null default false,
  -- context
  device_class       text not null,
  pointer_type       text not null,
  viewport_w         int not null,
  viewport_h         int not null,
  orientation        text not null,
  layout             text not null,
  theme              text not null,
  referrer_domain    text,
  local_hour         smallint not null check (local_hour between 0 and 23),
  server_ts          timestamptz not null default now()
);
create index exposures_session_idx on exposures (session_id);
create index exposures_visitor_idx on exposures (visitor_id, trial_index);

create table choices (
  exposure_id        uuid primary key references exposures on delete cascade,
  session_id         uuid not null references sessions on delete cascade,
  visitor_id         uuid not null references visitors on delete cascade,
  chosen_slot        smallint not null check (chosen_slot in (1, 2)),
  latency_ms         int not null check (latency_ms >= 0),
  input              text not null,
  interaction_trace  jsonb not null,
  -- context
  device_class       text not null,
  pointer_type       text not null,
  viewport_w         int not null,
  viewport_h         int not null,
  orientation        text not null,
  layout             text not null,
  theme              text not null,
  referrer_domain    text,
  local_hour         smallint not null check (local_hour between 0 and 23),
  server_ts          timestamptz not null default now()
);

create table block_ends (
  session_id         uuid not null references sessions on delete cascade,
  block_index        int not null check (block_index >= 0),
  visitor_id         uuid not null references visitors on delete cascade,
  action             text not null check (action in ('keep_going', 'done')),
  server_ts          timestamptz not null default now(),
  primary key (session_id, block_index)
);

alter table visitors   enable row level security;
alter table sessions   enable row level security;
alter table exposures  enable row level security;
alter table choices    enable row level security;
alter table block_ends enable row level security;

revoke all on visitors, sessions, exposures, choices, block_ends from anon, authenticated;
