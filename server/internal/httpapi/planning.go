package httpapi

import (
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"net/http"
)

func (a *API) planningRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/camps/{camp}/planning", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.PlanningInput
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.CreatePlanning(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 201, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/planning", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.ListPlanning(r.Context(), u, r.PathValue("camp"))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/planning/{plan}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		version, ok := queryInt(w, r, "version")
		if !ok {
			return
		}
		value, err := a.Camps.Planning(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), version)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/planning/{plan}/actions", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.PlanningAction
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.ActOnPlanning(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/planning-requests/claim", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input struct {
			SessionIDs []string `json:"session_ids"`
		}
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.ClaimPlanningRequest(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), input.SessionIDs)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
}
