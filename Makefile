# Common tasks. Targets that talk to a database read it from .env.
LOCAL_DB := postgresql://postgres:postgres@127.0.0.1:54322/postgres

.PHONY: test test-db db db-reset run run-local run-memory wipe

test:            ## unit + experiment-validity tests (no database needed)
	cd nudge && go vet ./... && go test ./...

test-db:         ## also run the Postgres store test against the local Supabase DB
	cd nudge && NUDGE_TEST_DATABASE_URL=$(LOCAL_DB) go test -count=1 ./internal/store

db:              ## start the local Supabase Postgres (applies migrations on first start)
	supabase db start

db-reset:        ## rebuild the local DB from migrations (deletes local data)
	supabase db reset

run:             ## run the API against DATABASE_URL from .env
	set -a && . ./.env && set +a && cd nudge && go run ./cmd/nudge

run-local:       ## run the API against the local Supabase DB
	set -a && . ./.env && set +a && cd nudge && DATABASE_URL=$(LOCAL_DB) go run ./cmd/nudge

run-memory:      ## run the API with no database (events are discarded on exit)
	set -a && . ./.env && set +a && cd nudge && STORE=memory go run ./cmd/nudge

wipe:            ## delete all experiment data in DATABASE_URL (asks first)
	scripts/wipe.sh
