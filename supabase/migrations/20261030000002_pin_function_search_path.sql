-- Pin search_path on the Wilson interval helpers (Supabase advisor lint
-- "function_search_path_mutable"). With an empty search_path, a function
-- can't be tricked into resolving a name to an object someone created in
-- another schema; built-ins in pg_catalog are always found.
--
-- `create or replace` keeps the views that call these functions intact.
-- wilson_upper now calls wilson_lower by its schema-qualified name, since
-- the empty search_path no longer finds it unqualified.

create or replace function public.wilson_lower(wins bigint, n bigint) returns double precision
language sql immutable
set search_path = ''
as $$
  select case when n = 0 then null else
    (wins::float8 / n + 1.96^2 / (2 * n)
      - 1.96 * sqrt((wins::float8 / n) * (1 - wins::float8 / n) / n + 1.96^2 / (4 * n^2)))
    / (1 + 1.96^2 / n)
  end
$$;

create or replace function public.wilson_upper(wins bigint, n bigint) returns double precision
language sql immutable
set search_path = ''
as $$
  select case when n = 0 then null else 1 - public.wilson_lower(n - wins, n) end
$$;

revoke execute on function public.wilson_lower(bigint, bigint), public.wilson_upper(bigint, bigint)
  from public, anon, authenticated;
