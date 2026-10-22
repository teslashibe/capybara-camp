package httpapi

import (
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"net/http"
)

func (a *API) planningRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/camps/{camp}/planning/{plan}/implementations", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		version, ok := queryInt(w, r, "version")
		if !ok {
			return
		}
		after, ok := queryInt(w, r, "after")
		if !ok {
			return
		}
		limit, ok := queryInt(w, r, "limit")
		if !ok {
			return
		}
		if limit == 0 {
			limit = 20
		}
		value, err := a.Camps.ImportedImplementations(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), version, int(after), int(limit))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/planning/{plan}/implementations/{implementation}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.ImportedImplementation(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.PathValue("implementation"))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/planning/{plan}/implementations/{implementation}/attempts", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		after, ok := queryInt(w, r, "after")
		if !ok {
			return
		}
		limit, ok := queryInt(w, r, "limit")
		if !ok {
			return
		}
		if limit == 0 {
			limit = 10
		}
		value, err := a.Camps.ImportedImplementationAttempts(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.PathValue("implementation"), int(after), int(limit))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/planning/{plan}/implementations/{implementation}/transcript", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		before, ok := queryInt(w, r, "before")
		if !ok {
			return
		}
		limit, ok := queryInt(w, r, "limit")
		if !ok {
			return
		}
		if limit == 0 {
			limit = 200
		}
		value, err := a.Camps.ImportedImplementationTranscript(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.PathValue("implementation"), before, int(limit))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/planning/import", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.PlanningImportInput
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.ImportPlanning(r.Context(), u, r.PathValue("camp"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 201, value)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/planning-sources", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.PlanningSources(r.Context(), u, r.PathValue("camp"), r.URL.Query().Get("project_id"), r.URL.Query().Get("account_id"))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
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
