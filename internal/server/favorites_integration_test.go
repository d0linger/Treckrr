//go:build integration

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

func TestFavoritesIntegration(t *testing.T) {
	e := newItEnv(t)
	var userID int64
	if err := e.pool.QueryRowContext(e.ctx, `SELECT id FROM users WHERE username=$1`, e.uname).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	post := func(body string, want int) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/account/favorites", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", e.csrf("/"))
		req.Header.Set("X-Preference-User", itoa64(userID))
		res, err := e.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("status=%d want=%d body=%s", res.StatusCode, want, b)
		}
	}
	read := func() store.FavoriteState {
		t.Helper()
		res, err := e.client.Get(e.srv.URL + "/account/favorites")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var state store.FavoriteState
		if err := json.NewDecoder(res.Body).Decode(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	post(`{"changes":[{"kind":"neighbor","value":"1","active":true}]}`, 200)
	if state := read(); state.Enabled || len(state.Items) != 0 {
		t.Fatalf("disabled wrote preferences: %+v", state)
	}
	post(`{"enabled":true,"changes":[{"kind":"neighbor","value":"1","active":true}]}`, 200)
	post(`{"changes":[{"kind":"neighbor","value":"2","active":true}]}`, 200)
	post(`{"changes":[{"kind":"neighbor","value":"1","active":false}]}`, 200)
	post(`{"changes":[{"kind":"neighbor","value":"1","active":false}]}`, 200)
	if state := read(); !state.Enabled || len(state.Items["neighbor"]) != 1 || state.Items["neighbor"][0] != "2" {
		t.Fatalf("merge/remove: %+v", state)
	}
	for id := 3; id < 14; id++ {
		post(fmt.Sprintf(`{"changes":[{"kind":"neighbor","value":"%d","active":true}]}`, id), 200)
	}
	if state := read(); len(state.Items["neighbor"]) != 8 {
		t.Fatalf("unbounded: %+v", state)
	}
	other, err := e.st.CreateUser(e.ctx, e.uname+"favorites", "Test-only-pass-123", models.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.ExecContext(e.ctx, `DELETE FROM users WHERE id=$1`, other) })
	state, err := e.st.Favorites(e.ctx, other)
	if err != nil || state.Enabled || len(state.Items) != 0 {
		t.Fatalf("user isolation: %+v %v", state, err)
	}
	post(`{"user_id":1,"enabled":true}`, 400)
	post(`{"changes":[{"kind":"neighbor","value":"-1","active":true}]}`, 400)
	// Raw invalid UTF-8 is refused, not decoded into U+FFFD and stored.
	post("{\"changes\":[{\"kind\":\"task\",\"value\":\"\xff\xfe\",\"active\":true}]}", 400)
	post(`{"enabled":false}`, 200)
	post(`{"changes":[{"kind":"neighbor","value":"99","active":true}]}`, 200)
	if state := read(); state.Enabled || len(state.Items["neighbor"]) != 8 || len(state.Items["task"]) != 0 {
		t.Fatalf("disabled changed preferences: %+v", state)
	}
}

func TestFavoritesAuthorizationIntegration(t *testing.T) {
	e := newItEnv(t)
	var userID int64
	if err := e.pool.QueryRowContext(e.ctx, `SELECT id FROM users WHERE username=$1`, e.uname).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	csrf := e.csrf("/")
	post := func(client *http.Client, token, user string, want int) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/account/favorites", strings.NewReader(`{"enabled":true}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", token)
		req.Header.Set("X-Preference-User", user)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("favorites status=%d, want %d", res.StatusCode, want)
		}
	}
	for _, tc := range []struct {
		name, token, user string
		want              int
	}{
		{"missing csrf", "", itoa64(userID), http.StatusForbidden},
		{"invalid csrf", "invalid", itoa64(userID), http.StatusForbidden},
		{"missing user binding", csrf, "", http.StatusConflict},
		{"stale user binding", csrf, itoa64(userID + 1), http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) { post(e.client, tc.token, tc.user, tc.want) })
	}
	state, err := e.st.Favorites(e.ctx, userID)
	if err != nil || state.Enabled || len(state.Items) != 0 {
		t.Fatalf("rejected request changed preferences: %+v %v", state, err)
	}
	anonymous := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := anonymous.Get(e.srv.URL + "/account/favorites")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Fatalf("anonymous preferences request: %d %s", res.StatusCode, res.Header.Get("Location"))
	}

	// A stale page from account A must not change account B after session switch,
	// even if it somehow has B's valid CSRF token.
	otherName := e.uname + "_viewer"
	otherID, err := e.st.CreateUser(e.ctx, otherName, e.adminPass, models.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.ExecContext(e.ctx, `DELETE FROM users WHERE id=$1`, otherID) })
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	viewer := &http.Client{Jar: jar}
	getToken := func(path string) string {
		t.Helper()
		res, err := viewer.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		match := csrfRe.FindSubmatch(body)
		if len(match) != 2 {
			t.Fatalf("missing viewer token on %s", path)
		}
		return string(match[1])
	}
	res, err = viewer.PostForm(e.srv.URL+"/login", url.Values{
		"username": {otherName}, "password": {e.adminPass}, "csrf_token": {getToken("/login")},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	viewerToken := getToken("/profile")
	post(viewer, viewerToken, itoa64(userID), http.StatusConflict)
	post(viewer, viewerToken, itoa64(otherID), http.StatusOK)
	other, err := e.st.Favorites(e.ctx, otherID)
	if err != nil || !other.Enabled {
		t.Fatalf("viewer cannot manage own preferences: %+v %v", other, err)
	}
	state, err = e.st.Favorites(e.ctx, userID)
	if err != nil || state.Enabled || len(state.Items) != 0 {
		t.Fatalf("viewer changed other user's preferences: %+v %v", state, err)
	}
}

func TestFavoritesConcurrentChangesIntegration(t *testing.T) {
	e := newItEnv(t)
	var userID int64
	if err := e.pool.QueryRowContext(e.ctx, `SELECT id FROM users WHERE username=$1`, e.uname).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	on := true
	if err := e.st.ChangeFavorites(e.ctx, userID, &on, nil); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	run := func(active bool) {
		t.Helper()
		start := make(chan struct{})
		results := make(chan error, workers)
		for i := range workers {
			go func() {
				<-start
				change := []store.FavoriteChange{{Kind: "neighbor", Value: itoa64(int64(i + 1)), Active: active}}
				if err := e.st.ChangeFavorites(e.ctx, userID, nil, change); err != nil {
					results <- err
					return
				}
				results <- e.st.ChangeFavorites(e.ctx, userID, nil, change)
			}()
		}
		close(start)
		for range workers {
			if err := <-results; err != nil {
				t.Error(err)
			}
		}
	}
	run(true)
	state, err := e.st.Favorites(e.ctx, userID)
	if err != nil || !state.Enabled || len(state.Items["neighbor"]) != workers {
		t.Fatalf("concurrent additions lost or duplicated: %+v %v", state, err)
	}
	for i := range workers {
		if !slices.Contains(state.Items["neighbor"], itoa64(int64(i+1))) {
			t.Fatalf("missing concurrent favorite %d: %+v", i+1, state)
		}
	}
	run(false)
	state, err = e.st.Favorites(e.ctx, userID)
	if err != nil || !state.Enabled || len(state.Items) != 0 {
		t.Fatalf("concurrent removals left entries: %+v %v", state, err)
	}
}
