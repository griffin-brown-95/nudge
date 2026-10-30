# A or B?

A playful "just pick one" page that's really an experiment in how presentation
nudges choices. The design is in [docs/EXPERIMENT.md](docs/EXPERIMENT.md).

```
GitHub Pages (/web)  →  nudge API on Cloud Run (/nudge, us-east1)  →  Supabase Postgres (us-east-1)
```

The browser never talks to Supabase directly. Only the `nudge` service has DB credentials.

| Path | What |
|---|---|
| `nudge/` | Go service: `cmd/nudge`, `internal/{config,assign,events,store,httpapi}` |
| `nudge/experiments.yaml` | Factors, levels and salts. Edit this to change the experiment. |
| `supabase/migrations/` | Schema and analysis views |
| `scripts/wipe.sh` | Deletes all experiment data and keeps the schema |
| `web/` | Static frontend (coming soon) |

## Local development

Prerequisites: Go 1.27+, Docker, Supabase CLI, `psql`.

```bash
cp .env.example .env      # then fill in values
make test                 # unit + experiment-validity tests (no DB needed)
make db                   # local Supabase Postgres on :54322, migrations applied
make test-db              # store tests against the local DB
make run-local            # API on :8080 against the local DB
```

`make run-memory` runs the API without any database, for frontend work. The
[Makefile](Makefile) lists every target.

## Test, then wipe

Point a local or staging frontend at the API, click around, then inspect the
data in Supabase Studio or with `psql "$DIRECT_URL"`. When you're ready for
real data, run:

```bash
make wipe
```

It shows the DB host and current row counts and asks you to type `wipe`. The
local DB can be reset completely with `make db-reset`.

## Analysis views

| View | Answers |
|---|---|
| `v_position_bias` | How often the first-rendered option wins, by layout and device |
| `v_attribute_win_rates` | Each color, emphasis and size level's win rate when contested |
| `v_matchups` | Head-to-heads, ranked by how one-sided they are (min 30 trials) |
| `v_trials`, `v_appearances` | Building blocks for your own queries |

## Adding or changing a factor

1. Edit `nudge/experiments.yaml`: add a factor with a `name`, a `scope` (`option`, `trial` or `visitor`), a new random `salt` (`openssl rand -hex 8`) and its `levels`.
2. Bump `version`. Visitors reshuffle, and the analysis views split by version.
3. Run `make test`. The balance and independence tests cover the new factor automatically.
4. The new factor is stored in the `conditions` jsonb column right away. To give it a column of its own, add a migration and a line in `internal/store/postgres.go`.
5. Render it in the frontend. Exposures are rejected unless the client reports exactly what it was assigned.

## Deploying (Cloud Run, us-east1)

Placeholders: `<gcp-project>`, `<pages-origin>`.

```bash
supabase link --project-ref "$SUPABASE_PROJECT_REF"
supabase db push                                   # apply migrations to the hosted DB

gcloud secrets create nudge-database-url --data-file=- <<< "$DATABASE_URL"
gcloud run deploy nudge --source nudge --region us-east1 --project <gcp-project> \
  --allow-unauthenticated --min-instances 0 --max-instances 3 \
  --set-env-vars ALLOWED_ORIGIN=<pages-origin> \
  --set-secrets DATABASE_URL=nudge-database-url:latest
```

### Privacy

No IPs are stored by `nudge`, and the rate limiter keeps them only in memory.
Cloud Run's own request logs do record client IPs; to drop them, add a log
exclusion in Cloud Logging for `resource.type="cloud_run_revision" AND
log_name:"requests"`.
