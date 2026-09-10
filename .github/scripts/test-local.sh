#!/bin/sh
set -eu

# Used only in the disposable development test container, never against prod.
: "${TEST_DATABASE_URL:?Set TEST_DATABASE_URL to an isolated test database}"
PG_MAJOR=${PG_MAJOR:-16}
case "$PG_MAJOR" in 16|18) ;; *) echo "PG_MAJOR must be 16 or 18"; exit 1;; esac
apk add --no-cache build-base "postgresql${PG_MAJOR}-client"
pg_isready --dbname "$TEST_DATABASE_URL"
server_version=$(psql "$TEST_DATABASE_URL" -Atc 'SHOW server_version_num')
if [ "$((server_version / 10000))" != "$PG_MAJOR" ]; then
  echo "Test server/client major versions differ; set PG_MAJOR to match the isolated server."
  exit 1
fi
go test -race -tags=integration -p 1 ./...
