package server

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"net/http"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

const (
	// signatureDataPrefix is the only accepted encoding: the pad's PNG data URL.
	signatureDataPrefix = "data:image/png;base64,"
	// maxSignatureData bounds the base64 payload; a pad stroke image is a few
	// dozen KB, far below this.
	maxSignatureData = 512 << 10
	// maxSignatureWidth/Height bound the decode before it allocates.
	maxSignatureWidth, maxSignatureHeight = 2000, 1000
	// minSignatureInk is how many drawn pixels make a signature rather than
	// an accidental tap on an empty pad.
	minSignatureInk = 40
)

// signatureFromForm reads an optional on-site signature (signature_name,
// signature_png, signature_at). It returns nil without a signature and a
// German message for an unusable one.
func signatureFromForm(r *http.Request) (*models.EntrySignature, string) {
	name := strings.TrimSpace(r.FormValue("signature_name"))
	data := strings.TrimSpace(r.FormValue("signature_png"))
	if name == "" && data == "" {
		return nil, ""
	}
	if data == "" {
		return nil, "Bitte vor Ort unterschreiben lassen oder den Namen der unterschreibenden Person leeren."
	}
	if name == "" || lenError("Name", name, maxNameLen) != "" {
		return nil, "Bitte den Namen der unterschreibenden Person angeben (höchstens 100 Zeichen)."
	}
	img, ok := decodeSignature(data)
	if !ok {
		return nil, "Die Unterschrift konnte nicht gelesen werden. Bitte erneut unterschreiben lassen."
	}
	now := time.Now()
	signedAt := now
	// The pad stamps the device time of signing, which matters for a booking
	// replayed from the offline queue days later. A clock far off is ignored.
	if at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(r.FormValue("signature_at"))); err == nil &&
		at.Before(now.Add(10*time.Minute)) && at.After(now.AddDate(0, 0, -60)) {
		signedAt = at
	}
	createdBy := ""
	if u := userFromCtx(r); u != nil {
		createdBy = u.Username
	}
	return &models.EntrySignature{SignerName: name, SignedAt: signedAt, Image: img, CreatedBy: createdBy}, ""
}

// decodeSignature validates the pad's PNG data URL and re-encodes it, so only
// pixels the server decoded are stored (no ancillary chunks, no other format).
func decodeSignature(data string) ([]byte, bool) {
	if !strings.HasPrefix(data, signatureDataPrefix) || len(data) > maxSignatureData {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(data, signatureDataPrefix))
	if err != nil {
		return nil, false
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || format != "png" || cfg.Width < 1 || cfg.Height < 1 ||
		cfg.Width > maxSignatureWidth || cfg.Height > maxSignatureHeight {
		return nil, false
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil || signatureInk(img) < minSignatureInk {
		return nil, false
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, img); err != nil {
		return nil, false
	}
	return out.Bytes(), true
}

// signatureInk counts visibly dark, non-transparent pixels.
func signatureInk(img image.Image) int {
	ink := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a > 0x8000 && (r+g+bl)/3 < 0xA000 {
				ink++
			}
		}
	}
	return ink
}

// signatureView adds an entry's signature state to its edit page.
func (s *Server) signatureView(r *http.Request, data pageData, entry *models.Entry) error {
	data["SignatureAction"] = "/entries/" + itoa64(entry.ID) + "/signature"
	sig, err := s.store.GetEntrySignature(r.Context(), entry.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	sig.Image = nil
	data["Signature"] = sig
	data["SignatureChanged"] = sig.ConfirmedText != models.SignatureSummary(*entry)
	return nil
}

// handleEntrySignatureAdd signs an existing booking on its edit page.
func (s *Server) handleEntrySignatureAdd(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if !s.entryYearOpen(w, r, entry, "Das Abrechnungsjahr ist abgeschlossen – Unterschriften können nicht mehr ergänzt werden.") {
		return
	}
	back := "/entries/" + itoa64(id) + "/edit#unterschrift"
	sig, msg := signatureFromForm(r)
	if msg == "" && sig == nil {
		msg = "Bitte unterschreiben lassen und den Namen angeben."
	}
	if msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, back)
		return
	}
	entry.Signature = sig
	switch err := s.store.AddEntrySignature(r.Context(), entry); {
	case errors.Is(err, store.ErrYearCompleted):
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen – Unterschriften können nicht mehr ergänzt werden.")
	case errors.Is(err, store.ErrNeighborAnonymized):
		s.setFlash(w, r, "error", "Der Nachbar wurde inzwischen anonymisiert. Die Unterschrift wurde nicht gespeichert.")
	case errors.Is(err, store.ErrSignatureExists):
		s.setFlash(w, r, "info", "Diese Buchung ist bereits unterschrieben.")
	case errors.Is(err, store.ErrInvoiceLocked):
		s.setFlash(w, r, "error", "Diese Position ist festgeschrieben. Für Korrekturen bitte die zugehörige Rechnung stornieren.")
	case err != nil:
		s.serverError(w, r.URL.Path, err)
		return
	default:
		s.audit(r, "signature_add", "entry", id, s.neighborName(r, entry.NeighborID)+" · unterschrieben von "+sig.SignerName+" · "+models.SignatureSummary(*entry))
		s.setFlash(w, r, "success", "Unterschrift gespeichert.")
	}
	redirect(w, r, back)
}

// handleEntrySignatureServe streams a booking's signature image.
func (s *Server) handleEntrySignatureServe(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	sig, err := s.store.GetEntrySignature(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(sig.Image)
}

// handleEntrySignatureDelete removes a signature while the booking is open,
// e.g. after the booking was corrected and has to be signed again.
func (s *Server) handleEntrySignatureDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if !s.entryYearOpen(w, r, entry, "Das Abrechnungsjahr ist abgeschlossen – Unterschriften können nicht mehr geändert werden.") {
		return
	}
	back := "/entries/" + itoa64(id) + "/edit#unterschrift"
	sig, err := s.store.DeleteEntrySignature(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrYearCompleted):
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen – Unterschriften können nicht mehr geändert werden.")
	case errors.Is(err, store.ErrNeighborAnonymized):
		s.setFlash(w, r, "error", "Der Nachbar wurde inzwischen anonymisiert. Die Unterschrift wurde nicht geändert.")
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, r, "info", "Die Unterschrift war bereits entfernt.")
	case errors.Is(err, store.ErrInvoiceLocked):
		s.setFlash(w, r, "error", "Diese Position ist festgeschrieben. Für Korrekturen bitte die zugehörige Rechnung stornieren.")
	case err != nil:
		s.serverError(w, r.URL.Path, err)
		return
	default:
		s.audit(r, "signature_delete", "entry", id, s.neighborName(r, entry.NeighborID)+" · Unterschrift von "+sig.SignerName+" entfernt")
		s.setFlash(w, r, "success", "Unterschrift entfernt.")
	}
	redirect(w, r, back)
}
