package httpapi

import (
	"net/http"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func (a *API) attachmentRoutes(mux *http.ServeMux) {
	// Admit before decoding a base64 body. Two large bodies can be resident at
	// once; saturated requests retry without allocating another attachment.
	bounded := func(next authorizedHandler) http.HandlerFunc {
		return a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
			if !a.acquireAttachment(u.UserID) {
				problem(w, 429, "attachment_busy")
				return
			}
			defer a.releaseAttachment(u.UserID)
			// A 23 MiB body needs longer than the server's 15 s read timeout on a
			// slow uplink, and a stalled reader must not hold a slot forever.
			control := http.NewResponseController(w)
			deadline := time.Now().Add(2 * time.Minute)
			_ = control.SetReadDeadline(deadline)
			_ = control.SetWriteDeadline(deadline)
			next(w, r, u)
		})
	}
	mux.HandleFunc("PUT /v1/camps/{camp}/sessions/{session}/attachments/{hash}", bounded(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in camp.AttachmentUpload
		if !decode(w, r, &in) {
			return
		}
		value, err := a.Camps.PutAttachment(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("hash"), in)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/sessions/{session}/attachment", bounded(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in camp.AttachmentRead
		if !decode(w, r, &in) {
			return
		}
		value, err := a.Camps.ReadAttachment(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), in)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
}

// Two attachment bodies may be resident at once, at most one per person.
func (a *API) acquireAttachment(user string) bool {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	if a.attachmentUsers == nil {
		a.attachmentUsers = make(map[string]bool)
	}
	if a.attachmentCount >= 2 || a.attachmentUsers[user] {
		return false
	}
	a.attachmentCount++
	a.attachmentUsers[user] = true
	return true
}
func (a *API) releaseAttachment(user string) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.attachmentCount--
	delete(a.attachmentUsers, user)
}
