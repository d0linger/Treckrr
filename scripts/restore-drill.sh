#!/bin/sh
# Restore-Drill: prove that a dump actually restores, in a throwaway, isolated
# Postgres — nothing touches the live database. Run it periodically (e.g. weekly
# cron) so a corrupt/incompatible backup is caught before you ever need it.
#
# Usage: sh scripts/restore-drill.sh [dumpfile]
#        (defaults to the newest ./backups/treckrr-*.dump)
set -e
cd "$(dirname "$0")/.."

dump="${1:-$(ls -1t backups/treckrr-*.dump 2>/dev/null | head -1)}"
if [ -z "$dump" ] || [ ! -f "$dump" ]; then
  echo "restore-drill: no dump found (looked for backups/treckrr-*.dump)"
  exit 1
fi
echo "restore-drill: using $dump"

name="treckrr-drill-$(date +%s)"
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

# Throwaway Postgres (same major version as the dump), auto-removed.
docker run -d --rm --name "$name" \
  -e POSTGRES_PASSWORD=drill -e POSTGRES_DB=drill \
  postgres:16-alpine >/dev/null

# Wait until it accepts connections.
i=0
while [ "$i" -lt 30 ]; do
  if docker exec "$name" pg_isready -U postgres >/dev/null 2>&1; then break; fi
  i=$((i + 1)); sleep 1
done

# Restore the dump into the throwaway DB.
if ! docker exec -i "$name" pg_restore -U postgres -d drill --no-owner < "$dump"; then
  echo "restore-drill: FAILED — pg_restore reported errors"
  exit 1
fi

# Sanity check: a core table must be present (row count is informational).
rows=$(docker exec "$name" psql -U postgres -d drill -tAc "SELECT count(*) FROM entries" 2>/dev/null || echo "ERR")
if [ "$rows" = "ERR" ]; then
  echo "restore-drill: FAILED — 'entries' table not restorable"
  exit 1
fi

echo "restore-drill: OK — dump restores cleanly (entries=$rows)"
