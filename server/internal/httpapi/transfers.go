package httpapi

import (
	"net/http"
	"strconv"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func (a *API) transferRoutes(mux *http.ServeMux) {
	const base = "/v1/camps/{camp}/sessions/{session}/transfers"
	mux.HandleFunc("POST "+base+"/{transfer}/workspaces", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.WorkspaceInput
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Git.CreateTransferWorkspace(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("transfer"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, http.StatusCreated, value)
		}
	}))
	for _, suffix := range []string{"", "/chunks/{ordinal}"} {
		mux.HandleFunc("POST /v1/runner/workspaces/{workspace}/transfer"+suffix, a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
			var input struct {
				ClaimID string `json:"claim_id"`
			}
			if !decode(w, r, &input) {
				return
			}
			var value any
			var err error
			if suffix == "" {
				value, err = a.Git.RunnerTransfer(r.Context(), token(r), r.PathValue("workspace"), input.ClaimID)
			} else {
				index, parseErr := strconv.Atoi(r.PathValue("ordinal"))
				if parseErr != nil || index < 0 {
					fail(w, camp.ErrInput)
					return
				}
				value, err = a.Git.RunnerTransferChunk(r.Context(), token(r), r.PathValue("workspace"), input.ClaimID, index)
			}
			if !fail(w, err) {
				write(w, http.StatusOK, value)
			}
		}))
	}
	mux.HandleFunc("POST "+base, a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.TransferInput
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.CreateTransfer(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, http.StatusCreated, value)
		}
	}))
	mux.HandleFunc("GET "+base, a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.ListTransfers(r.Context(), u, r.PathValue("camp"), r.PathValue("session"))
		if !fail(w, err) {
			write(w, http.StatusOK, value)
		}
	}))
	mux.HandleFunc("GET "+base+"/{transfer}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		value, err := a.Camps.Transfer(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("transfer"))
		if !fail(w, err) {
			write(w, http.StatusOK, value)
		}
	}))
	mux.HandleFunc("PUT "+base+"/{transfer}/chunks/{ordinal}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		index, err := strconv.Atoi(r.PathValue("ordinal"))
		if err != nil || index < 0 {
			fail(w, camp.ErrInput)
			return
		}
		var input camp.TransferChunk
		if !decode(w, r, &input) {
			return
		}
		value, err := a.Camps.PutTransferChunk(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("transfer"), index, input)
		if !fail(w, err) {
			write(w, http.StatusOK, value)
		}
	}))
	mux.HandleFunc("GET "+base+"/{transfer}/chunks/{ordinal}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		index, err := strconv.Atoi(r.PathValue("ordinal"))
		if err != nil || index < 0 {
			fail(w, camp.ErrInput)
			return
		}
		value, err := a.Camps.TransferChunk(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("transfer"), index)
		if !fail(w, err) {
			write(w, http.StatusOK, value)
		}
	}))
	for _, action := range []string{"complete", "cancel"} {
		mux.HandleFunc("POST "+base+"/{transfer}/"+action, a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
			if !decode(w, r, &struct{}{}) {
				return
			}
			var value camp.Transfer
			var err error
			if action == "complete" {
				value, err = a.Camps.CompleteTransfer(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("transfer"))
			} else {
				value, err = a.Camps.CancelTransfer(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.PathValue("transfer"))
			}
			if !fail(w, err) {
				write(w, http.StatusOK, value)
			}
		}))
	}
}
