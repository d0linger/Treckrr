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
		u, err := parseDatabaseURI(dsn)
		if err != nil {
			return "", nil, err
		}
		if user, rawPassword, ok := strings.Cut(u.userinfo, ":"); ok {
			pw, err := url.PathUnescape(strings.Trim(rawPassword, " "))
			if err != nil {
				return "", nil, errors.New("invalid backup database URL password")
			}
			// Empty userinfo passwords are omitted by libpq and pgx.
			if rawPassword != "" {
				password, present = pw, true
			}
			u.userinfo = user
		}
		params := make([]databaseURIParam, 0, len(u.params))
		for _, param := range u.params {
			if param.key == "password" {
				password, present = param.value, true
				continue
			}
			params = append(params, param)
		}
		u.params = params
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

// databaseURI preserves libpq URI bytes while changing selected fields. Unlike
// net/url, libpq treats '+' and '#' as ordinary data and accepts multiple hosts.
type databaseURI struct {
	scheme   string
	userinfo string
	hasUser  bool
	hosts    string
	path     string
	params   []databaseURIParam
}

type databaseURIParam struct {
	raw   string
	key   string
	value string
}

// parseDatabaseURI validates with pgx and retains raw components for edits
// that must not change libpq's credential or connection-option semantics.
func parseDatabaseURI(dsn string) (databaseURI, error) {
	u := databaseURI{params: []databaseURIParam{}}
	if _, err := pgconn.ParseConfig(dsn); err != nil {
		return u, errors.New("invalid backup database connection configuration")
	}
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok || (scheme != "postgres" && scheme != "postgresql") {
		return u, errors.New("invalid backup database URL")
	}
	u.scheme = scheme
	// libpq finds the first @ before a /, including a ? inside userinfo.
	if i := strings.IndexAny(rest, "@/"); i >= 0 && rest[i] == '@' {
		u.userinfo, u.hasUser = rest[:i], true
		rest = rest[i+1:]
	}
	end := 0
	for end < len(rest) && rest[end] != '/' && rest[end] != '?' {
		hostStart := end == 0 || rest[end-1] == ','
		if hostStart && rest[end] == '[' {
			close := strings.IndexByte(rest[end:], ']')
			if close < 0 {
				return u, errors.New("invalid backup database URL host")
			}
			end += close
		}
		end++
	}
	u.hosts = rest[:end]
	path, query, _ := strings.Cut(rest[end:], "?")
	u.path = path
	for query != "" {
		pair, tail, _ := strings.Cut(query, "&")
		query = tail
		rawKey, rawValue, _ := strings.Cut(pair, "=")
		key, err := url.PathUnescape(strings.Trim(rawKey, " "))
		if err != nil {
			return u, errors.New("invalid backup database URL query")
		}
		value, err := url.PathUnescape(strings.Trim(rawValue, " "))
		if err != nil {
			return u, errors.New("invalid backup database URL query")
		}
		u.params = append(u.params, databaseURIParam{raw: pair, key: key, value: value})
	}
	return u, nil
}

// String rejoins the retained URI bytes without form-style query encoding.
func (u databaseURI) String() string {
	var out strings.Builder
	out.WriteString(u.scheme + "://")
	if u.hasUser {
		out.WriteString(u.userinfo + "@")
	}
	out.WriteString(u.hosts + u.path)
	for i, param := range u.params {
		separator := "&"
		if i == 0 {
			separator = "?"
		}
		out.WriteString(separator + param.raw)
	}
	return out.String()
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
