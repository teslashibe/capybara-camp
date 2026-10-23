package httpapi

import (
	"net/http"
	"strconv"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func (a *API) workspaceExportRoutes(mux *http.ServeMux) {
	const base = "/v1/camps/{camp}/sessions/{session}/exports"
	mux.HandleFunc("POST "+base+"/{export}/checkout-token", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in struct {
			SHA256 string `json:"sha256"`
		}
		if !decode(w, r, &in) {
			return
		}
		value, err := a.Git.WorkspaceExportCheckoutToken(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("export"), in.SHA256)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("POST "+base, a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in camp.WorkspaceExportInput
		if !decode(w, r, &in) {
			return
		}
		value, err := a.Git.CreateWorkspaceExport(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.Header.Get("Idempotency-Key"), in)
		if !fail(w, err) {
			write(w, 201, value)
		}
	}))
	for _, cancel := range []bool{false, true} {
		method, suffix := "GET", ""
		if cancel {
			method, suffix = "POST", "/cancel"
		}
		mux.HandleFunc(method+" "+base+"/{export}"+suffix, a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
			if cancel && !decode(w, r, &struct{}{}) {
				return
			}
			value, err := a.Camps.WorkspaceExport(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("export"), cancel)
			if !fail(w, err) {
				write(w, 200, value)
			}
		}))
	}
	mux.HandleFunc("GET "+base+"/{export}/chunks/{ordinal}", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		index, err := strconv.Atoi(r.PathValue("ordinal"))
		if err != nil || index < 0 {
			fail(w, camp.ErrInput)
			return
		}
		value, err := a.Git.WorkspaceExportChunk(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("export"), index)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	const runner = "/v1/runner/exports"
	mux.HandleFunc("GET "+runner+"/next", a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		value, err := a.Git.NextWorkspaceExport(r.Context(), token(r))
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	for _, action := range []string{"claim", "complete", "fail"} {
		mux.HandleFunc("POST "+runner+"/{export}/"+action, a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
			var in struct {
				ClaimID string `json:"claim_id"`
			}
			if !decode(w, r, &in) {
				return
			}
			var value any
			var err error
			if action == "claim" {
				value, err = a.Git.ClaimWorkspaceExport(r.Context(), token(r), r.PathValue("export"), in.ClaimID)
			} else {
				value, err = a.Git.FinishWorkspaceExport(r.Context(), token(r), r.PathValue("export"), in.ClaimID, action == "fail")
			}
			if !fail(w, err) {
				write(w, 200, value)
			}
		}))
	}
	mux.HandleFunc("POST "+runner+"/{export}/result", a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		var in camp.WorkspaceExportResultInput
		if !decode(w, r, &in) {
			return
		}
		value, err := a.Git.BeginWorkspaceExportResult(r.Context(), token(r), r.PathValue("export"), in)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
	mux.HandleFunc("PUT "+runner+"/{export}/chunks/{ordinal}", a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		index, err := strconv.Atoi(r.PathValue("ordinal"))
		if err != nil || index < 0 {
			fail(w, camp.ErrInput)
			return
		}
		var in struct {
			ClaimID string `json:"claim_id"`
			camp.TransferChunk
		}
		if !decode(w, r, &in) {
			return
		}
		value, err := a.Git.PutWorkspaceExportChunk(r.Context(), token(r), r.PathValue("export"), in.ClaimID, index, in.TransferChunk)
		if !fail(w, err) {
			write(w, 200, value)
		}
	}))
}
