package httpapi

import (
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"net/http"
)

func (a *API) projectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /v1/camps/{camp}/projects/{project}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.ProjectUpdate
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.UpdateProject(r.Context(), u, r.PathValue("camp"), r.PathValue("project"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("PATCH /v1/camps/{camp}/sessions/{session}/project", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.ProjectAssignment
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.AssignSessionProject(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("PATCH /v1/camps/{camp}/planning/{plan}/project", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.ProjectAssignment
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.AssignPlanningProject(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
}
