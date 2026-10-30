# A or B?

A playful "just pick one" page that's really an experiment in how presentation
nudges choices. The design is in [docs/EXPERIMENT.md](docs/EXPERIMENT.md); [docs/LEARNING.md](docs/LEARNING.md) explains how it was designed and built.

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
| `web/` | Static frontend: plain HTML, CSS and ES modules, no build step |

## Local development

Prerequisites: Go 1.27+, Docker, Supabase CLI, `psql`.

```bash
cp .env.example .env      # then fill in values
make test                 # unit + experiment-validity tests (no DB needed)
make db                   # local Supabase Postgres on :54322, migrations applied
make test-db              # store tests against the local DB
make run-local            # API on :8080 against the local DB
```

Then, in a second terminal:

```bash
make web                  # frontend on http://localhost:8000
```

Open http://localhost:8000 (use `localhost`, not `127.0.0.1`, so the origin
matches `ALLOWED_ORIGIN`). Before deploying, set `API_BASE` in
[web/config.js](web/config.js) to the Cloud Run URL.

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
| `v_choice_traces` | Per choice: first option considered, switches, changed mind, hover share on the winner |
| `v_trace_summary` | Decision style by frame and input: median time, how often people look at both, change their mind |
| `v_trials`, `v_appearances` | Building blocks for your own queries |

## Adding or changing a factor

1. Edit `nudge/experiments.yaml`: add a factor with a `name`, a `scope` (`option`, `trial` or `visitor`), a new random `salt` (`openssl rand -hex 8`) and its `levels`.
2. Bump `version`. Visitors reshuffle, and the analysis views split by version.
3. Run `make test`. The balance and independence tests cover the new factor automatically.
4. The new factor is stored in the `conditions` jsonb column right away. To give it a column of its own, add a migration and a line in `internal/store/postgres.go`.
5. Render it in the frontend. Exposures are rejected unless the client reports exactly what it was assigned.

## Deploying

Live setup: GCP project `nudge-513203` (number `370784999851`), Cloud Run
service `nudge` in `us-east1`, frontend on GitHub Pages at
https://griffin-brown-95.github.io/nudge/.

**Database migrations** (hosted Supabase). Never edit a migration that has
already been applied; add a new one.

```bash
supabase db push --db-url "$DATABASE_URL"
```

**One-time GCP setup.** Store the DB URL in Secret Manager and let Cloud Run read it:

```bash
grep '^DATABASE_URL=' .env | cut -d= -f2- | tr -d '\n' | gcloud secrets create nudge-database-url --replication-policy=automatic --data-file=-
gcloud secrets add-iam-policy-binding nudge-database-url \
  --member=serviceAccount:370784999851-compute@developer.gserviceaccount.com \
  --role=roles/secretmanager.secretAccessor
```

**Deploy the API:**

```bash
gcloud run deploy nudge --source nudge --region us-east1 --project nudge-513203 \
  --allow-unauthenticated --min-instances 0 --max-instances 3 \
  --set-env-vars ALLOWED_ORIGIN=https://griffin-brown-95.github.io \
  --set-secrets DATABASE_URL=nudge-database-url:latest
```

**Frontend:** merging changes under `web/` to `main` publishes them through
[.github/workflows/pages.yml](.github/workflows/pages.yml). The repo's
Settings → Pages → Source must be **GitHub Actions**.

### Privacy

No IPs are stored by `nudge`, and the rate limiter keeps them only in memory.
Cloud Run's own request logs record client IPs, so they're dropped before
storage by a Cloud Logging exclusion on the `_Default` sink (added 2026-10-30):

```bash
gcloud logging sinks update _Default --project nudge-513203 \
  --add-exclusion='name=nudge-request-logs,filter=resource.type="cloud_run_revision" AND resource.labels.service_name="nudge" AND log_id("run.googleapis.com/requests")'
```

The app's own logs (method, path, status, duration; no IP or visitor ID)
still arrive. Request counts and latency remain available in Cloud Run's
Metrics tab, which doesn't store IPs.
