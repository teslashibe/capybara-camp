package httpapi

import (
	"net/http"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func (a *API) attachmentRoutes(mux *http.ServeMux) {
	// Admit before decoding a base64 body. Two large bodies can be resident at
	// once; saturated requests retry without allocating another attachment.
	bounded := func(next authorizedHandler) http.HandlerFunc {
		return a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
			if !a.acquireAttachment() {
				problem(w, 429, "attachment_busy")
				return
			}
			defer a.releaseAttachment()
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

func (a *API) acquireAttachment() bool {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	if a.attachmentCount >= 2 {
		return false
	}
	a.attachmentCount++
	return true
}
func (a *API) releaseAttachment() {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.attachmentCount--
}
