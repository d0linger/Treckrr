#!/bin/sh
# The official postgres entrypoint runs this only for an empty data volume.
# It may source the file, so do not exit or change the entrypoint's shell options.
# DATABASE_URL remains explicit: this script never changes application settings.
if [ -z "${APP_DB_USER:-}" ] && [ -z "${APP_DB_PASSWORD:-}" ]; then
	printf '%s\n' 'treckrr: optional application role not configured; existing credentials remain in use'
elif [ -z "${APP_DB_USER:-}" ] || [ -z "${APP_DB_PASSWORD:-}" ]; then
	printf '%s\n' 'treckrr: set both APP_DB_USER and APP_DB_PASSWORD, or leave both empty' >&2
	false
elif [ "$APP_DB_USER" = "$POSTGRES_USER" ]; then
	printf '%s\n' 'treckrr: APP_DB_USER must differ from the bootstrap POSTGRES_USER' >&2
	false
else
	printf '%s\n' 'treckrr: creating the non-superuser application role'
	psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
		-v app_user="$APP_DB_USER" -v app_password="$APP_DB_PASSWORD" -v app_db="$POSTGRES_DB" <<'SQL'
BEGIN;
CREATE ROLE :"app_user" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS
	PASSWORD :'app_password';
ALTER DATABASE :"app_db" OWNER TO :"app_user";
ALTER SCHEMA public OWNER TO :"app_user";
COMMIT;
SQL
fi
