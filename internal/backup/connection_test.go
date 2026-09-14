package backup

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestDBURLEnvPreservesLibpqURI keeps passwords out of command arguments
// without changing libpq's interpretation of credentials or runtime options.
func TestDBURLEnvPreservesLibpqURI(t *testing.T) {
	t.Setenv("PGPASSWORD", "inherited")
	tests := []struct {
		name     string
		dsn      string
		password string
		clean    string
	}{
		{
			name:     "encoded spaces",
			dsn:      "postgres://u@db/test?password=a%20b&application_name=one%20two",
			password: "a b",
			clean:    "postgres://u@db/test?application_name=one%20two",
		},
		{
			name:     "literal plus",
			dsn:      "postgres://u@db/test?password=a+b&application_name=one+two",
			password: "a+b",
			clean:    "postgres://u@db/test?application_name=one+two",
		},
		{
			name:     "encoded plus",
			dsn:      "postgres://u@db/test?password=a%2Bb&application_name=one%2Btwo",
			password: "a+b",
			clean:    "postgres://u@db/test?application_name=one%2Btwo",
		},
		{
			name:     "hash is password data",
			dsn:      "postgres://u@db/test?password=a#b&application_name=one#two",
			password: "a#b",
			clean:    "postgres://u@db/test?application_name=one#two",
		},
		{
			name:     "userinfo punctuation",
			dsn:      "postgres://u:a?#b@db/test?application_name=one%20two",
			password: "a?#b",
			clean:    "postgres://u@db/test?application_name=one%20two",
		},
		{ //nolint:gosec // G101: synthetic URI tests password precedence without connecting.
			name:     "last password and encoded key",
			dsn:      "postgres://u:ignored@db/test?password=first&pass%77ord=last%20one&application_name=first&application_name=last",
			password: "last one",
			clean:    "postgres://u@db/test?application_name=first&application_name=last",
		},
		{
			name:     "multiple hosts and semicolon",
			dsn:      "postgres://u:a%20b@db,replica:5433/test?application_name=one;two",
			password: "a b",
			clean:    "postgres://u@db,replica:5433/test?application_name=one;two",
		},
		{
			name:     "IPv6",
			dsn:      "postgres://u:a%20b@[::1]:5432/test?application_name=one%20two",
			password: "a b",
			clean:    "postgres://u@[::1]:5432/test?application_name=one%20two",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, env, err := dbURLEnv(tt.dsn)
			if err != nil {
				t.Fatal("valid connection string rejected")
			}
			if clean != tt.clean {
				t.Fatal("password stripping changed unrelated URI bytes or retained credentials")
			}
			count := 0
			for _, entry := range env {
				if strings.HasPrefix(entry, "PGPASSWORD=") {
					count++
					if entry != "PGPASSWORD="+tt.password {
						t.Fatal("password value changed")
					}
				}
			}
			if count != 1 {
				t.Fatalf("password environment entries = %d, want 1", count)
			}
			before, err := pgconn.ParseConfig(tt.dsn)
			if err != nil {
				t.Fatal("original connection configuration rejected")
			}
			after, err := pgconn.ParseConfig(clean)
			if err != nil {
				t.Fatal("clean connection configuration rejected")
			}
			if before.Host != after.Host || before.Port != after.Port || before.Database != after.Database {
				t.Fatal("connection target changed")
			}
			if before.User != after.User || !reflect.DeepEqual(before.RuntimeParams, after.RuntimeParams) {
				t.Fatal("user or connection options changed")
			}
		})
	}
}

// TestWithDatabasePreservesLibpqURI prevents query aliases from overriding
// a rehearsal target while retaining the original connection settings.
func TestWithDatabasePreservesLibpqURI(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{name: "encoded spaces", dsn: "postgres://u@db/old?password=a%20b&application_name=one%20two"},
		{name: "literal plus", dsn: "postgres://u@db/old?password=a+b&application_name=one+two"},
		{name: "encoded plus", dsn: "postgres://u@db/old?password=a%2Bb&application_name=one%2Btwo"},
		{name: "hash data", dsn: "postgres://u@db/old?password=a#b&application_name=one#two"},
		{
			name: "duplicate database aliases",
			dsn:  "postgres://u@db/old?dbname=first&database=second&db%6Eame=third&application_name=one%20two",
		},
		{name: "multiple hosts", dsn: "postgres://u:p@db,replica:5433/old?application_name=one%20two"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, err := pgconn.ParseConfig(tt.dsn)
			if err != nil {
				t.Fatal("original connection configuration rejected")
			}
			after, err := pgconn.ParseConfig(withDatabase(tt.dsn, "treckrr_rehearsal_test"))
			if err != nil {
				t.Fatal("scratch connection configuration rejected")
			}
			if after.Database != "treckrr_rehearsal_test" {
				t.Fatal("existing database alias overrode scratch target")
			}
			if before.Host != after.Host || before.Port != after.Port || before.User != after.User {
				t.Fatal("connection host, port, or user changed")
			}
			if before.Password != after.Password || !reflect.DeepEqual(before.RuntimeParams, after.RuntimeParams) {
				t.Fatal("connection credentials or options changed")
			}
		})
	}
}
