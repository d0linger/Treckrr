package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func formListRequest(name string, values []string) *http.Request {
	form := url.Values{}
	for _, v := range values {
		form.Add(name, v)
	}
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_ = req.ParseForm()
	return req
}

func TestFormInt64ListRefusesRatherThanTruncates(t *testing.T) {
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = strconv.Itoa(i + 1)
		}
		return out
	}

	t.Run("at the limit everything comes through", func(t *testing.T) {
		got, ok := formInt64List(formListRequest("neighbor_ids", ids(maxFormListLen)), "neighbor_ids")
		if !ok {
			t.Fatalf("exactly %d values was refused; the cap is off by one", maxFormListLen)
		}
		if len(got) != maxFormListLen {
			t.Errorf("got %d ids, want %d", len(got), maxFormListLen)
		}
	})

	t.Run("over the limit is refused, not shortened", func(t *testing.T) {
		got, ok := formInt64List(formListRequest("neighbor_ids", ids(maxFormListLen+1)), "neighbor_ids")
		if ok {
			t.Fatal("accepted more than the limit")
		}
		// The whole point: a caller must not be handed a usable-looking short list.
		// Silently dropping ids would remove neighbors from a billing year while
		// still reporting success.
		if got != nil {
			t.Errorf("refusal returned %d ids, want nil — a shortened list invites silent data loss", len(got))
		}
	})

	t.Run("unparseable padding cannot buy extra work", func(t *testing.T) {
		// Only one valid id, but the request carries far more values than the cap.
		// Counting parsed values instead of submitted ones would let this through.
		vals := append([]string{"7"}, make([]string, maxFormListLen)...)
		for i := 1; i < len(vals); i++ {
			vals[i] = "nicht-numerisch"
		}
		if _, ok := formInt64List(formListRequest("neighbor_ids", vals), "neighbor_ids"); ok {
			t.Error("accepted a request padded past the cap with unparseable values")
		}
	})

	t.Run("whitespace is trimmed", func(t *testing.T) {
		// The machine_ids copy this replaced lacked TrimSpace, so a value the
		// browser padded parsed on one path and was dropped on the other.
		got, ok := formInt64List(formListRequest("machine_ids", []string{" 4 ", "\t9"}), "machine_ids")
		if !ok || len(got) != 2 || got[0] != 4 || got[1] != 9 {
			t.Errorf("got %v (ok=%v), want [4 9]", got, ok)
		}
	})

	t.Run("formMachineIDs shares the behavior", func(t *testing.T) {
		if _, ok := formMachineIDs(formListRequest("machine_ids", ids(maxFormListLen+1))); ok {
			t.Error("formMachineIDs accepted more than the limit")
		}
		got, ok := formMachineIDs(formListRequest("machine_ids", []string{" 3 "}))
		if !ok || len(got) != 1 || got[0] != 3 {
			t.Errorf("got %v (ok=%v), want [3]", got, ok)
		}
	})
}
