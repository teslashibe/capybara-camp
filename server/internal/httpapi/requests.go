package httpapi

import (
	"net/http"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func (a *API) requestRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/camps/{camp}/sessions/{session}/requests", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.RequestInput
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.CreateRequest(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 201, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/requests", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.ListRequests(r.Context(), u, r.PathValue("camp"))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/requests/{request}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.Request(r.Context(), u, r.PathValue("camp"), r.PathValue("request"))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/requests/claim", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input struct {
			SessionIDs *[]string `json:"session_ids"`
		}
		if (r.ContentLength != 0 || len(r.TransferEncoding) != 0) && !decode(w, r, &input) {
			return
		}
		var value *camp.Request
		var err error
		if input.SessionIDs == nil {
			value, err = a.Camps.ClaimRequest(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"))
		} else {
			value, err = a.Camps.ClaimAvailableRequest(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), *input.SessionIDs)
		}
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/requests/{request}/actions", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.RequestAction
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.ActOnRequest(r.Context(), u, r.PathValue("camp"), r.PathValue("request"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
}
