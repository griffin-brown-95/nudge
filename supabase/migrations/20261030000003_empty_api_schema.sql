-- An empty schema for Supabase's Data API (PostgREST) to expose.
--
-- nudge never uses the Data API: the browser only talks to the Go service.
-- But with the Data API disabled, PostgREST keeps running with a placeholder
-- schema ("pg_pgrst_no_exposed_schemas") that doesn't exist, so its startup
-- query fails and retries about every 32 seconds, filling the Postgres logs
-- with errors. Exposing this empty schema instead (Data API settings →
-- exposed schemas: api only) lets PostgREST start cleanly while serving
-- nothing.
--
-- Rule: never create tables, views or functions in this schema. Anything
-- here would become reachable through /rest/v1/.

create schema if not exists api;

comment on schema api is
  'Intentionally empty. Exposed to the Supabase Data API so PostgREST starts cleanly; nudge never uses it. Do not add objects here.';

revoke all on schema api from public, anon, authenticated;
