package httpapi

import (
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"net/http"
)

func (a *API) planningApprovalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/camps/{camp}/planning/{plan}/executors/{session}/execution", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.PlanningExecutorTarget(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.PathValue("session"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/planning/{plan}/execution-approvals", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in camp.PlanningApprovalInput
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Camps.PreparePlanningApproval(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.Header.Get("Idempotency-Key"), in)
		if !fail(w, err) {
			write(w, 201, out)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/planning/{plan}/execution-approvals/{approval}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.PlanningApproval(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.PathValue("approval"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/planning/{plan}/execution-approvals/{approval}/approve", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in camp.PlanningApprovalAction
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Camps.ApprovePlanningExecution(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.PathValue("approval"), r.Header.Get("Idempotency-Key"), in)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/planning/{plan}/execution-approvals/{approval}/cancel", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in struct {
			ExpectedRevision int64 `json:"expected_revision"`
		}
		if !decode(w, r, &in) {
			return
		}
		if !fail(w, a.Camps.CancelPlanningApproval(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"), r.PathValue("approval"), r.Header.Get("Idempotency-Key"), in.ExpectedRevision)) {
			w.WriteHeader(204)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/planning/{plan}/execution-approvals", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.PlanningApprovalStatuses(r.Context(), u, r.PathValue("camp"), r.PathValue("plan"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
}
