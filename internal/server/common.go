package server

import (
	"errors"
	"net/http"
	"strconv"

	"treckrr/internal/models"
	"treckrr/internal/store"
)

// resolveYear returns the billing year (Abrechnungsjahr) selected via the
// "year" query parameter, falling back to the latest year. When no billing
// year exists yet it redirects the user to create one and returns ok=false.
func (s *Server) resolveYear(w http.ResponseWriter, r *http.Request) (*models.BillingYear, bool) {
	if raw := r.URL.Query().Get("year"); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if year, err := s.store.GetBillingYear(r.Context(), id); err == nil {
				return year, true
			}
		}
	}
	year, err := s.store.LatestBillingYear(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "info", "Bitte zuerst ein Abrechnungsjahr anlegen.")
		redirect(w, r, "/years")
		return nil, false
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return nil, false
	}
	return year, true
}

// withYearSelector adds the list of billing years and the active year to page
// data so the shared layout can render the Abrechnungsjahr selector.
func (s *Server) withYearSelector(r *http.Request, p pageData, active *models.BillingYear) error {
	years, err := s.store.ListBillingYears(r.Context())
	if err != nil {
		return err
	}
	p["Years"] = years
	p["Year"] = active
	return nil
}

// resolveBase returns the pricing basis (Bemessungsgrundlage) selected via the
// "base" query parameter, falling back to the latest basis. Used by the pricing
// and gespann management pages, which operate on a basis directly.
func (s *Server) resolveBase(w http.ResponseWriter, r *http.Request) (*models.PriceBase, bool) {
	if raw := r.URL.Query().Get("base"); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			base, err := s.store.GetBase(r.Context(), id)
			if err == nil {
				return base, true
			}
		}
	}
	base, err := s.store.LatestBase(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "Keine Bemessungsgrundlage vorhanden", http.StatusInternalServerError)
		return nil, false
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return nil, false
	}
	return base, true
}

// baseIDFromForm resolves the base id submitted with a form.
func (s *Server) baseIDFromForm(r *http.Request) int64 {
	return formInt64(r, "base_id")
}

// yearIDFromForm resolves the billing year id submitted with a form.
func (s *Server) yearIDFromForm(r *http.Request) int64 {
	return formInt64(r, "year_id")
}
