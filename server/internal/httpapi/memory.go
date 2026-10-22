package httpapi

import (
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"net/http"
)

func (a *API) memoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/camps/{camp}/memory/observations", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.MemoryObservationInput
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.SaveMemoryObservation(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 201, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/memory/observations/{observation}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.MemoryObservation(r.Context(), u, r.PathValue("camp"), r.PathValue("observation"))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/memory/observations", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		limit, ok := queryInt(w, r, "limit")
		if !ok {
			return
		}
		if limit == 0 {
			limit = 50
		}
		value, err := a.Camps.MemoryObservations(r.Context(), u, r.PathValue("camp"), r.URL.Query().Get("project_id"), r.URL.Query().Get("visibility"), r.URL.Query().Get("after"), int(limit))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
}
