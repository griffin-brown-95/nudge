#!/usr/bin/env bash
# Deletes ALL experiment data (visitors, sessions, exposures, choices,
# block ends) and keeps the schema. Use it to clear test data before launch.
#
#   scripts/wipe.sh            # wipes the database in DATABASE_URL from .env
#   scripts/wipe.sh <url>      # wipes the given database
#
# For the local Supabase DB, `supabase db reset` does the same and more.
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ $# -ge 1 ]]; then
  url="$1"
else
  [[ -f .env ]] || { echo "no .env found" >&2; exit 1; }
  set -a; . ./.env; set +a
  url="${DATABASE_URL:?DATABASE_URL is not set in .env}"
fi

host=$(printf '%s' "$url" | sed -E 's#^[^@]*@([^/:]*).*#\1#')
counts=$(psql "$url" -X -A -t -c \
  "select (select count(*) from visitors) || ' visitors, ' || (select count(*) from choices) || ' choices'")

echo "Database host: $host"
echo "Currently holds: $counts"
read -r -p "Type 'wipe' to permanently delete all experiment data: " answer
[[ "$answer" == "wipe" ]] || { echo "Aborted."; exit 1; }

psql "$url" -X -v ON_ERROR_STOP=1 -c \
  "truncate visitors, sessions, exposures, choices, block_ends;"
echo "Wiped."
