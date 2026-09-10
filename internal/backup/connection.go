package backup

import (
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// dbURLEnv moves passwords out of subprocess argv for both supported libpq
// connection-string forms, preserving every other connection option.
func dbURLEnv(dsn string) (string, []string, error) {
	// Validate without returning the parser's error: it can contain credentials.
	if _, err := pgconn.ParseConfig(dsn); err != nil {
		return "", nil, errors.New("invalid backup database connection configuration")
	}
	password, present := os.LookupEnv("PGPASSWORD")
	var clean string
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", nil, errors.New("invalid backup database URL")
		}
		if u.User != nil {
			if pw, ok := u.User.Password(); ok {
				password, present = pw, true
			}
			u.User = url.User(u.User.Username())
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return "", nil, errors.New("invalid backup database URL query")
		}
		if values, ok := q["password"]; ok {
			password, present = values[len(values)-1], true
			q.Del("password")
		}
		u.RawQuery = q.Encode()
		clean = u.String()
	} else {
		var pw string
		var found bool
		var err error
		clean, pw, found, err = stripDSNPassword(dsn)
		if err != nil {
			return "", nil, err
		}
		if found {
			password, present = pw, true
		}
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "PGPASSWORD=") {
			env = append(env, item)
		}
	}
	if present {
		env = append(env, "PGPASSWORD="+password)
	}
	return clean, env, nil
}

func dsnSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// The driver already validated this keyword DSN. Walk libpq's quoted/escaped
// values to omit password assignments without changing unrelated settings.
func stripDSNPassword(dsn string) (string, string, bool, error) {
	parts := make([]string, 0)
	var password string
	found := false
	for i := 0; i < len(dsn); {
		for i < len(dsn) && dsnSpace(dsn[i]) {
			i++
		}
		if i == len(dsn) {
			break
		}
		start := i
		for i < len(dsn) && dsn[i] != '=' && !dsnSpace(dsn[i]) {
			i++
		}
		key := dsn[start:i]
		for i < len(dsn) && dsnSpace(dsn[i]) {
			i++
		}
		if i == len(dsn) || dsn[i] != '=' {
			return "", "", false, errors.New("invalid backup keyword DSN")
		}
		i++
		for i < len(dsn) && dsnSpace(dsn[i]) {
			i++
		}
		quoted := i < len(dsn) && dsn[i] == '\''
		if quoted {
			i++
		}
		var value strings.Builder
		closed := !quoted
		for i < len(dsn) {
			c := dsn[i]
			if quoted && c == '\'' {
				i++
				closed = true
				break
			}
			if !quoted && dsnSpace(c) {
				break
			}
			i++
			if c == '\\' {
				if i == len(dsn) {
					return "", "", false, errors.New("invalid backup keyword DSN escape")
				}
				c = dsn[i]
				i++
			}
			value.WriteByte(c)
		}
		if !closed {
			return "", "", false, errors.New("invalid backup keyword DSN quote")
		}
		if key == "password" {
			password, found = value.String(), true
		} else {
			parts = append(parts, dsn[start:i])
		}
	}
	return strings.Join(parts, " "), password, found, nil
}
