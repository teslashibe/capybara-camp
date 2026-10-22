package httpapi

import (
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"net/http"
)

func (a *API) implementationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/camps/{camp}/planning/{plan}/implementation-runs", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		version, ok := queryInt(w, r, "version")
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
		out, err := a.Camps.Implementations(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), version, r.URL.Query().Get("before"), int(limit))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/planning/{plan}/implementation-runs/{implementation}/requests", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		limit, ok := queryInt(w, r, "limit")
		if !ok {
			return
		}
		if limit == 0 {
			limit = 20
		}
		out, err := a.Camps.ImplementationRequests(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.PathValue("implementation"), r.URL.Query().Get("before"), int(limit))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/requests/{request}/local-controls", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in struct {
			LeaseToken string `json:"lease_token"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Camps.LocalImplementationControls(r.Context(), u, r.PathValue("camp"), r.PathValue("request"), in.LeaseToken)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/planning/{plan}/execution-approvals/{approval}/dispatch", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Camps.DispatchPlanningExecution(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.PathValue("approval"), r.Header.Get("Idempotency-Key"), in.ExpectedRevision)
		if !fail(w, err) {
			write(w, 201, out)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/planning/{plan}/implementation-runs/{implementation}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.Implementation(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.PathValue("implementation"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/planning/{plan}/implementation-runs/{implementation}/requests", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in camp.RequestInput
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Camps.ContinueImplementation(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.PathValue("implementation"), r.Header.Get("Idempotency-Key"), in)
		if !fail(w, err) {
			write(w, 201, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/requests/{request}/implementation-events", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in camp.LocalPlanningEvent
		if !decode(w, r, &in) {
			return
		}
		seq, err := a.Camps.AppendLocalImplementationEvent(r.Context(), u, r.PathValue("camp"), r.PathValue("request"), in)
		if !fail(w, err) {
			write(w, 200, map[string]int64{"seq": seq})
		}
	}))
}
