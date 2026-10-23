package store

import "context"

// SystemInfo is the database side of the administrator's system page. It is
// read-only and cheap: catalog functions and small counts.
type SystemInfo struct {
	PostgresVersion string
	DatabaseBytes   int64
	Migrations      int
	LatestMigration string
	ActiveSessions  int
	ActiveUsers     int
}

// ReadSystemInfo gathers server version, database size, schema level and
// session counts in one round trip.
func (s *Store) ReadSystemInfo(ctx context.Context) (SystemInfo, error) {
	var info SystemInfo
	err := s.db.QueryRowContext(ctx, `
		SELECT current_setting('server_version'),
		       pg_database_size(current_database()),
		       (SELECT count(*) FROM schema_migrations),
		       COALESCE((SELECT max(name) FROM schema_migrations), ''),
		       (SELECT count(*) FROM sessions WHERE expires_at > now()),
		       (SELECT count(*) FROM users WHERE NOT disabled)`).
		Scan(&info.PostgresVersion, &info.DatabaseBytes, &info.Migrations, &info.LatestMigration,
			&info.ActiveSessions, &info.ActiveUsers)
	return info, err
}
