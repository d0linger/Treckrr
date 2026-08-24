package server

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"time"

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
	// maxConcurrentPhotoDecodes bounds how many uploads may be decoding at once.
	// The per-pixel cap above limits ONE decode, not the sum of them: at 40 MP a
	// JPEG decodes to ~60 MB (YCbCr 4:2:0) and a PNG to ~160 MB (RGBA), so a
	// handful of simultaneous uploads can exceed the container's 768 MB memory
	// limit and get the process OOM-killed — taking every other request with it.
	// Two slots keep the worst case near 320 MB, leaving room for the ~15 MB
	// baseline and everything else in flight.
	maxConcurrentPhotoDecodes = 2
	// photoSlotWait is how long an upload waits for a free decode slot before
	// giving up. Long enough to queue behind a slow decode, short enough that the
	// user gets an answer rather than a hung request.
	photoSlotWait = 30 * time.Second
)

// acquirePhotoSlot blocks until a decode slot is free, the caller's request is
// cancelled, or photoSlotWait elapses. Release with releasePhotoSlot.
//
// Only New() sizes the channel; a Server built literally (as tests do) has a nil
// one and runs unbounded rather than blocking forever on a nil-channel send.
func (s *Server) acquirePhotoSlot(ctx context.Context) error {
	if s.photoSlots == nil {
		return nil
	}
	t := time.NewTimer(photoSlotWait)
	defer t.Stop()
	select {
	case s.photoSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return errPhotoBusy
	}
}

func (s *Server) releasePhotoSlot() {
	if s.photoSlots == nil {
		return
	}
	<-s.photoSlots
}

// decodePhoto runs processPhoto while holding a decode slot, releasing it via
// defer so a panic inside an image decoder cannot leak the slot and slowly
// starve every later upload.
func (s *Server) decodePhoto(ctx context.Context, raw []byte) ([]byte, error) {
	if err := s.acquirePhotoSlot(ctx); err != nil {
		return nil, err
	}
	defer s.releasePhotoSlot()
	return processPhoto(raw)
}

var errPhotoBusy = &photoError{"Gerade werden zu viele Bilder verarbeitet. Bitte in einem Moment erneut versuchen."}

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
	// Bound concurrent decodes: this is the one request path that can allocate
	// hundreds of MB, and nothing else caps how many run at once.
	img, perr := s.decodePhoto(r.Context(), raw)
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
	// Keep s.auth's Cache-Control: no-store — a receipt is sensitive, per-user
	// content and must not linger in the browser cache (recoverable via the back
	// button after logout, or on a shared machine). Don't override it here.
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
		s.audit(r, "photo_delete", "entry", entryID, s.neighborName(r, entry.NeighborID)+" · Foto entfernt")
		s.setFlash(w, r, "success", "Foto entfernt.")
	}
	redirect(w, r, "/entries/"+itoa64(entryID)+"/edit")
}
