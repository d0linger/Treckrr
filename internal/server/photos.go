package server

import (
	"bytes"
	"image"
	"image/jpeg"
	"io"
	"net/http"

	// Register decoders for the formats a phone camera produces. Decoding into a
	// plain image.Image and re-encoding as JPEG drops all EXIF metadata (the
	// stripping is a side effect of not carrying it through image.Image).
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

const (
	// maxPhotoUpload caps the raw upload size read for a booking photo.
	maxPhotoUpload = 12 << 20 // 12 MiB
	// maxPhotoPixels rejects absurdly large images BEFORE a full decode, so a
	// crafted header can't drive a huge allocation (a 12 MiB JPEG can otherwise
	// decode to hundreds of MB). 40 MP covers any real phone camera.
	maxPhotoPixels = 40_000_000
)

// processPhoto decodes an uploaded image and re-encodes it as a JPEG, stripping
// EXIF (orientation/GPS/etc.) in the process. It rejects non-images and images
// whose declared dimensions are implausibly large before decoding them.
func processPhoto(raw []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > maxPhotoPixels {
		return nil, errPhotoTooLarge
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var errPhotoTooLarge = &photoError{"Bild zu groß (max. 40 Megapixel). Bitte kleiner aufnehmen."}

type photoError struct{ msg string }

func (e *photoError) Error() string { return e.msg }

// handleEntryPhotoUpload attaches a re-encoded photo to a booking.
func (s *Server) handleEntryPhotoUpload(w http.ResponseWriter, r *http.Request) {
	entryID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entry, err := s.store.GetEntry(r.Context(), entryID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Receipt photos are evidence for the booking: once its year is closed or the
	// invoice is festgeschrieben, the booking is frozen and its attachments can't
	// change either (same guard the edit/void handlers use).
	if !s.entryYearOpen(w, r, entry, "Das Abrechnungsjahr ist abgeschlossen – Belege können nicht mehr geändert werden.") {
		return
	}
	if err := r.ParseMultipartForm(maxPhotoUpload); err != nil {
		s.setFlash(w, r, "error", "Upload zu groß oder ungültig.")
		redirect(w, r, "/entries/"+itoa64(entryID)+"/edit")
		return
	}
	file, _, err := r.FormFile("photo")
	if err != nil {
		s.setFlash(w, r, "error", "Bitte ein Bild wählen.")
		redirect(w, r, "/entries/"+itoa64(entryID)+"/edit")
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxPhotoUpload))
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	img, perr := processPhoto(raw)
	if perr != nil {
		msg := "Kein gültiges Bild."
		if pe, ok := perr.(*photoError); ok {
			msg = pe.msg
		}
		s.setFlash(w, r, "error", msg)
		redirect(w, r, "/entries/"+itoa64(entryID)+"/edit")
		return
	}
	if _, err := s.store.AddEntryPhoto(r.Context(), entryID, img, "image/jpeg"); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "photo_add", "entry", entryID, s.neighborName(r, entry.NeighborID))
	s.setFlash(w, r, "success", "Foto angehängt.")
	redirect(w, r, "/entries/"+itoa64(entryID)+"/edit")
}

// handleEntryPhotoServe streams a stored photo (scoped to its booking).
func (s *Server) handleEntryPhotoServe(w http.ResponseWriter, r *http.Request) {
	entryID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	photoID, err := formInt64FromPath(r, "pid")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	img, ct, err := s.store.GetEntryPhoto(r.Context(), entryID, photoID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(img)
}

// handleEntryPhotoDelete removes a photo from a booking.
func (s *Server) handleEntryPhotoDelete(w http.ResponseWriter, r *http.Request) {
	entryID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	photoID, err := formInt64FromPath(r, "pid")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entry, err := s.store.GetEntry(r.Context(), entryID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Same immutability guard as upload: no removing evidence from a frozen booking.
	if !s.entryYearOpen(w, r, entry, "Das Abrechnungsjahr ist abgeschlossen – Belege können nicht mehr geändert werden.") {
		return
	}
	if err := s.store.DeleteEntryPhoto(r.Context(), entryID, photoID); err != nil {
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	} else {
		s.audit(r, "photo_delete", "entry", entryID, "")
		s.setFlash(w, r, "success", "Foto entfernt.")
	}
	redirect(w, r, "/entries/"+itoa64(entryID)+"/edit")
}
