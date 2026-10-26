package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/d0linger/treckrr/internal/store"
)

// handleFavorites exposes preferences only for the authenticated session user.
func (s *Server) handleFavorites(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		http.Error(w, "Anmeldung erforderlich", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodPost {
		if r.Header.Get("X-Preference-User") != strconv.FormatInt(user.ID, 10) {
			http.Error(w, "Anmeldung geändert – bitte die Seite neu laden.", http.StatusConflict)
			return
		}
		var in struct {
			Enabled *bool                  `json:"enabled"`
			Changes []store.FavoriteChange `json:"changes"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		body, err := io.ReadAll(r.Body)
		// encoding/json silently turns invalid UTF-8 into U+FFFD, so a malformed
		// body would be stored as replacement characters instead of refused.
		// Only the raw bytes still show the difference.
		if err != nil || !utf8.Valid(body) {
			s.badRequest(w, "Ungültige Favoriten.")
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&in); err != nil {
			s.badRequest(w, "Ungültige Favoriten.")
			return
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			s.badRequest(w, "Ungültige Favoriten.")
			return
		}
		if err := s.store.ChangeFavorites(r.Context(), user.ID, in.Enabled, in.Changes); err != nil {
			if errors.Is(err, store.ErrFavoriteInvalid) {
				s.badRequest(w, "Ungültige Favoriten.")
				return
			}
			s.serverError(w, "favorites update", err)
			return
		}
	}
	state, err := s.store.Favorites(r.Context(), user.ID)
	if err != nil {
		s.serverError(w, "favorites read", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(struct {
		store.FavoriteState
		UserID int64 `json:"user_id"`
	}{state, user.ID}); err != nil {
		return
	}
}
