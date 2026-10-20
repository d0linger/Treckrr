package server

import "net/http"

// Photos shared from the phone's camera or gallery ("Teilen" → Treckrr) arrive
// as a Web Share Target POST. The service worker intercepts it, parks the
// images in IndexedDB on the device and opens the picker below; the upload
// itself then goes through the normal, CSRF-protected booking photo route.

// shareTargetPath is the manifest's share_target action.
const shareTargetPath = "/share-target"

// handleShareTargetPost answers a share that reached the server because the
// service worker was not active yet. It changes nothing and reads no body; it
// only explains the next step.
func (s *Server) handleShareTargetPost(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, shareTargetPath+"?fallback=1", http.StatusSeeOther)
}

// handleShareTarget renders the picker: recent bookings that may still take
// photos, filled by the client from the parked images.
func (s *Server) handleShareTarget(w http.ResponseWriter, r *http.Request) {
	data := s.newPage(w, r, "Fotos zuordnen", "entries")
	data["Fallback"] = r.URL.Query().Get("fallback") == "1"
	if user := userFromCtx(r); user != nil && user.CanWrite() {
		targets, err := s.store.RecentPhotoTargets(r.Context(), 30)
		if err != nil {
			s.serverError(w, "share target: bookings", err)
			return
		}
		data["Targets"] = targets
	}
	s.render(w, r, "share_target", data)
}
