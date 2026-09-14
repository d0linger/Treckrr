package db

import (
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Timeout defaults apply to both DSN forms without overriding an operator.
func TestWithTimeouts(t *testing.T) {
	tests := []struct {
		name      string
		dsn       string
		statement string
		idle      string
		wantErr   bool
	}{
		{name: "plain URL", dsn: "postgres://db:5432/treckrr?sslmode=disable", statement: "30s", idle: "60s"},
		{name: "operator timeout", dsn: "postgres://db/treckrr?statement_timeout=5s", statement: "5s", idle: "60s"},
		{name: "postgresql scheme", dsn: "postgresql://db/treckrr", statement: "30s", idle: "60s"},
		{name: "keyword defaults", dsn: "host=db user=treckrr dbname=treckrr", statement: "30s", idle: "60s"},
		{name: "keyword timeout", dsn: "host=db user=treckrr statement_timeout=5s", statement: "5s", idle: "60s"},
		{
			name:      "zero disables guards",
			dsn:       "postgres://db/treckrr?statement_timeout=0&idle_in_transaction_session_timeout=0",
			statement: "0",
			idle:      "0",
		},
		{name: "malformed keyword", dsn: "=not a dsn=", wantErr: true},
		{name: "unknown scheme", dsn: "mysql://db:3306/treckrr", wantErr: true},
		{name: "malformed query escape", dsn: "postgres://u@db/treckrr?application_name=%zz", wantErr: true},
		{name: "NUL query value", dsn: "postgres://u@db/treckrr?application_name=%00", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := withTimeouts(tt.dsn)
			if (err != nil) != tt.wantErr {
				t.Fatalf("connection error present = %v, want %v", err != nil, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.RuntimeParams["statement_timeout"] != tt.statement {
				t.Errorf("statement timeout = %q, want %q", got.RuntimeParams["statement_timeout"], tt.statement)
			}
			if got.RuntimeParams["idle_in_transaction_session_timeout"] != tt.idle {
				t.Errorf("idle timeout = %q, want %q", got.RuntimeParams["idle_in_transaction_session_timeout"], tt.idle)
			}
		})
	}
}

// TestWithTimeoutsPreservesConnectionConfig checks timeout injection against
// pgx's unmodified interpretation of the operator's connection string.
func TestWithTimeoutsPreservesConnectionConfig(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{name: "query spaces", dsn: "postgres://u@db/treckrr?password=a%20b&application_name=one%20two"},
		{name: "query plus", dsn: "postgres://u@db/treckrr?password=a+b&application_name=one+two"},
		{name: "encoded plus", dsn: "postgres://u@db/treckrr?password=a%2Bb"},
		{name: "query punctuation", dsn: "postgres://u@db/treckrr?password=a#b&application_name=x;y"},
		{
			name: "last duplicate wins",
			dsn:  "postgres://u@db/treckrr?password=first&password=last&statement_timeout=5s&statement_timeout=9s",
		},
		{
			name: "empty final timeout gets default",
			dsn:  "postgres://u@db/treckrr?statement_timeout=5s&statement_timeout=",
		},
		{name: "multiple hosts", dsn: "postgres://u:p@db,replica:5433/treckrr?application_name=one%20two"},
		{
			name: "driver options",
			dsn:  "postgres://u@db/treckrr?default_query_exec_mode=simple_protocol&statement_cache_capacity=0",
		},
		{
			name: "quoted keyword options",
			dsn:  "host=db user=u password='a b+c' options='-c work_mem=32MB' statement_timeout=5s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original, err := pgx.ParseConfig(tt.dsn)
			if err != nil {
				t.Fatal("connection configuration rejected")
			}
			got, err := withTimeouts(tt.dsn)
			if err != nil {
				t.Fatal("connection configuration rejected")
			}
			if got.Host != original.Host || got.Port != original.Port || got.Database != original.Database {
				t.Fatal("connection target changed")
			}
			if got.User != original.User || got.Password != original.Password {
				t.Fatal("connection credentials changed")
			}
			if got.DefaultQueryExecMode != original.DefaultQueryExecMode ||
				got.StatementCacheCapacity != original.StatementCacheCapacity {
				t.Fatal("driver options changed")
			}
			for key, fallback := range map[string]string{
				"statement_timeout":                   "30s",
				"idle_in_transaction_session_timeout": "60s",
			} {
				want := original.RuntimeParams[key]
				if want == "" {
					want = fallback
				}
				if got.RuntimeParams[key] != want {
					t.Errorf("%s = %q, want %q", key, got.RuntimeParams[key], want)
				}
				delete(got.RuntimeParams, key)
				delete(original.RuntimeParams, key)
			}
			if !reflect.DeepEqual(got.RuntimeParams, original.RuntimeParams) {
				t.Fatal("unrelated runtime parameters changed")
			}
		})
	}
}
