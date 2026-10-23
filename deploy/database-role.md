# Database role and recovery

These instructions supplement [upgrade validation](../docs/upgrade-validation.md).
Role conversion is a separate deployment step. Applying the source changes does
not change credentials or ownership in an existing volume.

## Fresh installations

The official PostgreSQL image creates `POSTGRES_USER` as a bootstrap superuser.
Reserve that credential for database administration. On the first start of an
empty volume, the mounted `postgres-init/10-app-role.sh` creates an application
role when both `APP_DB_USER` and `APP_DB_PASSWORD` are set. Use a different name
and password from the bootstrap account, and put those application credentials
in `DATABASE_URL`. Hex-generated passwords avoid URL-encoding ambiguity.

The application role owns only the selected database and its `public` schema.
It is `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`. Database
ownership permits Treckrr's schema migrations and trusted `pg_trgm` / `unaccent`
extensions. PostgreSQL owns the privileged implementation objects of trusted
extensions; do not transfer those individual extension objects to the app role.
Default PostgreSQL `PUBLIC` privileges still apply to other databases in the
cluster, so this is not a general multi-tenant database isolation policy.

The init hook deliberately rejects partial configuration or a role name equal
to `POSTGRES_USER`. Initialization scripts run only on an empty volume. Fixing
their settings after a failed first initialization does not rerun them against
the partially initialized volume; investigate that new volume before retrying.
Never remove an existing volume to make an init script run again.

## Existing installations: rehearse a separate target database

Do not change `DATABASE_URL` before the target role owns the app's objects. Do
not run `REASSIGN OWNED` against the bootstrap superuser: it also owns system
objects. The safer conversion creates a new database and restores a trusted
Treckrr-produced backup. Treckrr creates its archives with `pg_dump --no-owner
--no-privileges`; its restore command also uses `--no-owner`, but does not strip
arbitrary ACL entries from foreign archives. The original database remains the
rollback point.

1. Take a fresh encrypted backup, retain the original backup key and the exact
   current image/configuration, and restore it in a disposable environment.
   Verify the existing invoice hashes, business fingerprints and reports using
   the upgrade-validation guide. Never rehearse on the live database.
2. As the database administrator, create a new login role with the attributes
   listed above and a new database owned by it. For example, in an interactive
   `psql` session on the intended cluster:

   ```sql
   CREATE ROLE treckrr_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE
     NOREPLICATION NOBYPASSRLS;
   \password treckrr_app
   CREATE DATABASE treckrr_role_cutover OWNER treckrr_app;
   ```

   Choose unused role/database names. `\password` avoids putting the new secret
   in shell history. PostgreSQL 16's `public` schema is owned by
   `pg_database_owner`, so the new database owner can create its schema objects.
3. Restore with the Treckrr offline CLI against the **new** database as the app
   role, using the archive's backup key. Use a separate explicit environment
   file/project with the reviewed image; check its rendered configuration before
   running anything. Follow README's typed-confirmation restore command. The
   restore strips original ownership, clears session/challenge data, reconciles
   restored account credentials and applies pending migrations. Never start
   background workers against this target until validation is complete.
4. Confirm database ownership and `SELECT current_user, rolsuper, rolcreatedb,
   rolcreaterole, rolreplication, rolbypassrls FROM pg_roles WHERE rolname =
   current_user;`. All five privilege flags must be false. Check migrations,
   sign-in, normal booking/invoice access, and a new encrypted backup followed by
   a restore rehearsal under the new role. Compare all business values again.
5. At the actual cutover, stop every writer to the old database, take the final
   backup and repeat the proven restore/verification against a fresh target.
   Change `DATABASE_URL` only after that succeeds, then start the app and check
   readiness and disappearance of the superuser warning. Keep the old database
   unavailable to writers until the rollback retention period expires.

Switching back to the untouched old database is only lossless before new writes
reach the new target. After that, recovery requires a deliberate data-reconciliation
plan. Do not run mixed application versions or writers against both databases.

## Backups and rehearsal privileges

The runtime role can dump and restore its own Treckrr database, including the
trusted search extensions, without superuser or `CREATEDB`. Only restore
archives from trusted operators: database ownership still allows replacing app
tables, triggers and functions, even though server-side `COPY ... PROGRAM` is
blocked.

`BACKUP_REHEARSE_URL` is different: the rehearsal worker creates and drops a
random scratch database. Use a separate `NOSUPERUSER CREATEDB NOCREATEROLE`
login on a dedicated rehearsal server when possible. It needs its own password
and must own the scratch databases it creates. Do not grant `CREATEDB` to the
normal application role merely to enable this optional check.

Keep the bootstrap credential available for operational recovery. Rotating
`APP_DB_PASSWORD` in Compose alone changes neither an existing database role's
password nor its ownership; coordinate any later rotation with PostgreSQL and
`DATABASE_URL` explicitly.
