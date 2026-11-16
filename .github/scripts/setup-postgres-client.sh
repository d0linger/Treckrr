#!/usr/bin/env bash
set -euo pipefail

# The server runs in a service container; pg_dump/pg_restore run on the runner.
# Do not inherit the runner's default client (PG 16 cannot dump a PG 18 server).
case "${PG_MAJOR:-}" in
  16|18) ;;
  *) echo "PG_MAJOR must be 16 or 18" >&2; exit 1 ;;
esac
: "${GITHUB_PATH:?GITHUB_PATH must be set}"

# Hosted Ubuntu images remove their PGDG apt source after provisioning. Restore
# the signed official source so the requested major is available for installation.
# Both downloads retry: a dropped connection (curl exit 55, a mirror hiccup)
# must not fail a release run that never reached the code under test.
key=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc
sudo install -d /usr/share/postgresql-common/pgdg
sudo curl --fail --silent --show-error --location \
  --retry 5 --retry-all-errors --retry-delay 3 --connect-timeout 20 \
  https://www.postgresql.org/media/keys/ACCC4CF8.asc -o "$key"
source /etc/os-release
printf 'deb [signed-by=%s] https://apt.postgresql.org/pub/repos/apt %s-pgdg main\n' \
  "$key" "$VERSION_CODENAME" | sudo tee /etc/apt/sources.list.d/treckrr-pgdg.list >/dev/null
sudo apt-get -o Acquire::Retries=5 update
sudo apt-get -o Acquire::Retries=5 install --yes --no-install-recommends "postgresql-client-$PG_MAJOR"

# Use the versioned binaries, not Debian's pg_wrapper or another installed major.
pg_bin="/usr/lib/postgresql/$PG_MAJOR/bin"
for tool in pg_dump pg_restore psql pg_isready; do
  version=$("$pg_bin/$tool" --version)
  echo "$version"
  if [[ "$version" != *" $PG_MAJOR."* ]]; then
    echo "$tool does not match PostgreSQL $PG_MAJOR" >&2
    exit 1
  fi
done
echo "$pg_bin" >> "$GITHUB_PATH"
