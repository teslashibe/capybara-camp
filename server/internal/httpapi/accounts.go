package httpapi

import (
	"net/http"
	"strings"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func (a *API) accountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/provider-accounts/{account}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.ProviderAccountStatus(r.Context(), u, r.PathValue("account"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/provider-accounts", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.ProviderAccounts(r.Context(), u)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/provider-accounts", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.ProviderAccountInput
		if !decode(w, r, &input) {
			return
		}
		out, err := a.Camps.CreateProviderAccount(r.Context(), u, r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 201, out)
		}
	}))
	mux.HandleFunc("POST /v1/provider-accounts/{account}/runner-grants", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.CreateRunnerGrant(r.Context(), u, r.PathValue("account"), r.Header.Get("Idempotency-Key"))
		if !fail(w, err) {
			write(w, 201, out)
		}
	}))
	mux.HandleFunc("POST /v1/provider-accounts/{account}/logins", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.BeginProviderLogin(r.Context(), u, r.PathValue("account"), r.Header.Get("Idempotency-Key"))
		if !fail(w, err) {
			write(w, 201, out)
		}
	}))
	mux.HandleFunc("GET /v1/provider-accounts/{account}/logins/{attempt}", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.ProviderLogin(r.Context(), u, r.PathValue("account"), r.PathValue("attempt"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/provider-accounts/{account}/logins/{attempt}/cancel", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.CancelProviderLogin(r.Context(), u, r.PathValue("account"), r.PathValue("attempt"), r.Header.Get("Idempotency-Key"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/provider-accounts/{account}/logins/{attempt}/code", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.ProviderCodeInput
		if !decode(w, r, &input) {
			return
		}
		out, err := a.Camps.SubmitProviderLoginCode(r.Context(), u, r.PathValue("account"), r.PathValue("attempt"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/provider-accounts/{account}/disconnect", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.DisconnectProviderAccount(r.Context(), u, r.PathValue("account"), r.Header.Get("Idempotency-Key"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/provider-accounts/{account}/reactivate", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.ReactivateProviderAccount(r.Context(), u, r.PathValue("account"), r.Header.Get("Idempotency-Key"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	// Runner tokens use their own domain authentication and cannot authorize any
	// human Camp endpoint. Each mutation rechecks the grant under the account lock.
	mux.HandleFunc("GET /v1/runner/account", a.runnerAuthorize(func(w http.ResponseWriter, r *http.Request, state camp.RunnerAccountState) {
		write(w, 200, state)
	}))
	mux.HandleFunc("GET /v1/runner/credentials", a.runnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		out, err := a.Camps.RunnerCredentialSnapshot(r.Context(), token(r))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/runner/credentials", a.runnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		var in camp.RunnerCredentialUpdate
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Camps.UpdateRunnerCredentials(r.Context(), token(r), in)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/runner/logins/{attempt}/claim", a.runnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		var in struct {
			ClaimID string `json:"claim_id"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Camps.ClaimRunnerLogin(r.Context(), token(r), r.PathValue("attempt"), in.ClaimID)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/runner/logins/{attempt}/report", a.runnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		var in struct {
			ClaimID string `json:"claim_id"`
			camp.RunnerLoginReport
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Camps.ReportRunnerLogin(r.Context(), token(r), r.PathValue("attempt"), in.ClaimID, in.RunnerLoginReport)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/runner/logins/{attempt}/code", a.runnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		var input struct {
			ClaimID    string `json:"claim_id"`
			DeliveryID string `json:"delivery_id"`
		}
		if !decode(w, r, &input) {
			return
		}
		out, err := a.Camps.TakeRunnerLoginCode(r.Context(), token(r), r.PathValue("attempt"), input.ClaimID, input.DeliveryID)
		if !fail(w, err) {
			if out == nil {
				w.WriteHeader(204)
			} else {
				write(w, 200, out)
			}
		}
	}))
	mux.HandleFunc("POST /v1/runner/logins/{attempt}/code/ack", a.runnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		var input struct {
			ClaimID    string `json:"claim_id"`
			DeliveryID string `json:"delivery_id"`
			CodeID     string `json:"code_id"`
		}
		if !decode(w, r, &input) {
			return
		}
		if !fail(w, a.Camps.AcknowledgeRunnerLoginCode(r.Context(), token(r), r.PathValue("attempt"), input.ClaimID, input.DeliveryID, input.CodeID)) {
			w.WriteHeader(204)
		}
	}))
	mux.HandleFunc("POST /v1/runner/erased", a.runnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		if !fail(w, a.Camps.AcknowledgeRunnerErased(r.Context(), token(r))) {
			w.WriteHeader(204)
		}
	}))
}
func (a *API) runnerAuthorize(next func(http.ResponseWriter, *http.Request, camp.RunnerAccountState)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state, err := a.Camps.RunnerAccount(r.Context(), token(r))
		if fail(w, err) {
			return
		}
		scope, maximum := "runner:"+state.InstallationID, 6000
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/requests/") && streamAppend(r.URL.Path) {
			scope, maximum = "runner-stream:"+state.InstallationID, 40000
		}
		if fail(w, a.Auth.Limit(r.Context(), scope, maximum)) {
			return
		}
		next(w, r, state)
	}
}
