# Common tasks. Targets that talk to a database read it from .env.
LOCAL_DB := postgresql://postgres:postgres@127.0.0.1:54322/postgres

.PHONY: test test-db db db-reset db-push run run-local run-memory web simulate wipe

test:            ## unit + experiment-validity tests (no database needed)
	cd nudge && go vet ./... && go test ./...

test-db:         ## also run the Postgres store test against the local Supabase DB
	cd nudge && NUDGE_TEST_DATABASE_URL=$(LOCAL_DB) go test -count=1 ./internal/store

db:              ## start the local Supabase Postgres (applies migrations on first start)
	supabase db start

db-reset:        ## rebuild the local DB from migrations (deletes local data)
	supabase db reset

db-push:         ## apply new migrations to the hosted DB (session-mode pooler; asks first)
	set -a && . ./.env && set +a && \
	  supabase db push --db-url "$$(printf '%s' "$$DATABASE_URL" | sed 's#:6543/#:5432/#')"

run:             ## run the API against DATABASE_URL from .env
	set -a && . ./.env && set +a && cd nudge && go run ./cmd/nudge

run-local:       ## run the API against the local Supabase DB
	set -a && . ./.env && set +a && cd nudge && DATABASE_URL=$(LOCAL_DB) go run ./cmd/nudge

run-memory:      ## run the API with no database (events are discarded on exit)
	set -a && . ./.env && set +a && cd nudge && STORE=memory go run ./cmd/nudge

web:             ## serve the frontend on http://localhost:8000 (matches ALLOWED_ORIGIN)
	python3 -m http.server 8000 --bind 127.0.0.1 --directory web

simulate:        ## fill the LOCAL db with 300 synthetic visitors (known biases) for the dashboard
	cd nudge && DATABASE_URL=$(LOCAL_DB) go run ./cmd/simulate -visitors 300

wipe:            ## delete all experiment data in DATABASE_URL (asks first)
	scripts/wipe.sh
