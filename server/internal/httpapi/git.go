package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func (a *API) gitAuthorize(next authorizedHandler) http.HandlerFunc {
	return a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		if a.Git == nil {
			problem(w, 503, "git_not_configured")
			return
		}
		if fail(w, a.Auth.Limit(r.Context(), "git:"+u.UserID, 1200)) {
			return
		}
		next(w, r, u)
	})
}
func (a *API) gitRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/camps/{camp}/sessions/{session}/execution", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Camps.Execution(r.Context(), u, r.PathValue("camp"), r.PathValue("session"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("PUT /v1/camps/{camp}/sessions/{session}/execution", a.authorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var in camp.ExecutionInput
		if !decode(w, r, &in) {
			return
		}
		if in.Location == "cloud" && a.Git == nil {
			problem(w, 503, "git_not_configured")
			return
		}
		out, err := a.Camps.AssignExecution(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.Header.Get("Idempotency-Key"), in)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/runner/workspaces/next", a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		out, err := a.Git.NextRunnerWorkspace(r.Context(), token(r))
		if !fail(w, err) {
			if out == nil {
				w.WriteHeader(204)
			} else {
				write(w, 200, out)
			}
		}
	}))
	mux.HandleFunc("POST /v1/runner/workspaces/retired", a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		var in struct {
			IDs []string `json:"ids"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Git.RetiredRunnerWorkspaces(r.Context(), token(r), in.IDs)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/runner/workspaces/{workspace}/requests/claim", a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		var in struct {
			ClaimID string `json:"claim_id"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Git.ClaimRunnerRequest(r.Context(), token(r), r.PathValue("workspace"), in.ClaimID)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/runner/workspaces/{workspace}/requests/{request}", a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		out, err := a.Git.RunnerTurn(r.Context(), token(r), r.PathValue("workspace"), r.PathValue("request"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/runner/workspaces/{workspace}/requests/{request}/controls", a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		out, err := a.Git.RunnerControls(r.Context(), token(r), r.PathValue("workspace"), r.PathValue("request"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/runner/workspaces/{workspace}/requests/{request}/status", a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		out, err := a.Git.RunnerRequestStatus(r.Context(), token(r), r.PathValue("workspace"), r.PathValue("request"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/runner/workspaces/{workspace}/requests/{request}/events", a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		var in camp.StreamEvent
		if !decode(w, r, &in) {
			return
		}
		seq, err := a.Git.AppendRunnerEvent(r.Context(), token(r), r.PathValue("workspace"), r.PathValue("request"), in)
		if !fail(w, err) {
			write(w, 200, map[string]int64{"seq": seq})
		}
	}))
	mux.HandleFunc("POST /v1/runner/workspaces/{workspace}/requests/{request}/actions", a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
		var in camp.RequestAction
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Git.ActOnRunnerRequest(r.Context(), token(r), r.PathValue("workspace"), r.PathValue("request"), r.Header.Get("Idempotency-Key"), in)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	for _, operation := range []string{"claim", "heartbeat", "token", "report", "status"} {
		mux.HandleFunc("POST /v1/runner/workspaces/{workspace}/"+operation, a.workspaceRunnerAuthorize(func(w http.ResponseWriter, r *http.Request, _ camp.RunnerAccountState) {
			var in struct {
				ClaimID string `json:"claim_id"`
				camp.WorkspaceReport
			}
			if !decode(w, r, &in) {
				return
			}
			if operation != "report" && (in.State != "" || in.BaseCommit != "" || in.Branch != "" || in.FailureCode != "") {
				problem(w, 400, "invalid_input")
				return
			}
			var out any
			var err error
			switch operation {
			case "claim":
				out, err = a.Git.ClaimRunnerWorkspace(r.Context(), token(r), r.PathValue("workspace"), in.ClaimID)
			case "heartbeat":
				out, err = a.Git.HeartbeatRunnerWorkspace(r.Context(), token(r), r.PathValue("workspace"), in.ClaimID)
			case "token":
				out, err = a.Git.RunnerWorkspaceToken(r.Context(), token(r), r.PathValue("workspace"), in.ClaimID)
			case "report":
				out, err = a.Git.ReportRunnerWorkspace(r.Context(), token(r), r.PathValue("workspace"), in.ClaimID, in.WorkspaceReport)
			case "status":
				out, err = a.Git.RunnerWorkspaceStatus(r.Context(), token(r), r.PathValue("workspace"), in.ClaimID)
			}
			if !fail(w, err) {
				write(w, 200, out)
			}
		}))
	}
	mux.HandleFunc("GET /v1/camps/{camp}/sessions/{session}/workspaces", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Git.SessionWorkspaces(r.Context(), u, r.PathValue("camp"), r.PathValue("session"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/sessions/{session}/workspaces", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.WorkspaceInput
		if !decode(w, r, &input) {
			return
		}
		out, err := a.Git.CreateWorkspace(r.Context(), u, r.PathValue("camp"), r.PathValue("session"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 201, out)
		}
	}))
	mux.HandleFunc("GET /v1/camps/{camp}/workspaces/{workspace}", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Git.Workspace(r.Context(), u, r.PathValue("camp"), r.PathValue("workspace"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/camps/{camp}/workspaces/{workspace}/cancel", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input camp.WorkspaceCancel
		if !decode(w, r, &input) {
			return
		}
		out, err := a.Git.CancelWorkspace(r.Context(), u, r.PathValue("camp"), r.PathValue("workspace"), r.Header.Get("Idempotency-Key"), input)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/git/connections", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Git.Connections(r.Context(), u)
		if !fail(w, err) {
			write(w, 200, map[string]any{"connections": out, "install_url": a.Git.InstallURL()})
		}
	}))
	mux.HandleFunc("GET /v1/git/connections/{connection}", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Git.Status(r.Context(), u, r.PathValue("connection"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/git/logins", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		var input struct {
			ConnectionID string `json:"connection_id"`
		}
		if !decode(w, r, &input) {
			return
		}
		out, err := a.Git.Begin(r.Context(), u, r.Header.Get("Idempotency-Key"), input.ConnectionID)
		if !fail(w, err) {
			write(w, 201, out)
		}
	}))
	mux.HandleFunc("POST /v1/git/connections/{connection}/logins/{login}/cancel", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Git.Cancel(r.Context(), u, r.PathValue("connection"), r.PathValue("login"), r.Header.Get("Idempotency-Key"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("POST /v1/git/connections/{connection}/disconnect", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		out, err := a.Git.Disconnect(r.Context(), u, r.PathValue("connection"), r.Header.Get("Idempotency-Key"))
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/git/connections/{connection}/installations", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		page, ok := gitPage(w, r, "page")
		if !ok {
			return
		}
		out, err := a.Git.Installations(r.Context(), u, r.PathValue("connection"), page)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/git/connections/{connection}/installations/{installation}/repositories", a.gitAuthorize(func(w http.ResponseWriter, r *http.Request, u auth.Actor) {
		page, ok := gitPage(w, r, "page")
		if !ok {
			return
		}
		installationPage, ok := gitPage(w, r, "installation_page")
		if !ok {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("installation"), 10, 64)
		if err != nil || id <= 0 {
			problem(w, 400, "invalid_input")
			return
		}
		out, err := a.Git.Repositories(r.Context(), u, r.PathValue("connection"), id, installationPage, page)
		if !fail(w, err) {
			write(w, 200, out)
		}
	}))
	mux.HandleFunc("GET /v1/git/github/callback", a.gitCallback)
	mux.HandleFunc("POST /v1/git/github/events", a.gitWebhook)
}

func (a *API) workspaceRunnerAuthorize(next func(http.ResponseWriter, *http.Request, camp.RunnerAccountState)) http.HandlerFunc {
	return a.runnerAuthorize(func(w http.ResponseWriter, r *http.Request, state camp.RunnerAccountState) {
		if a.Git == nil {
			problem(w, 503, "git_not_configured")
			return
		}
		next(w, r, state)
	})
}

func (a *API) gitWebhook(w http.ResponseWriter, r *http.Request) {
	if a.Git == nil || a.GitWebhookSecret == "" {
		problem(w, 503, "git_not_configured")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		problem(w, 400, "invalid_input")
		return
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
	mac := hmac.New(sha256.New, []byte(a.GitWebhookSecret))
	_, _ = mac.Write(data)
	if err != nil || !strings.HasPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=") || !hmac.Equal(provided, mac.Sum(nil)) {
		problem(w, 401, "invalid_signature")
		return
	}
	if r.Header.Get("X-GitHub-Event") != "github_app_authorization" {
		w.WriteHeader(204)
		return
	}
	id := r.Header.Get("X-GitHub-Delivery")
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		problem(w, 400, "invalid_input")
		return
	}
	var payload struct {
		Action string `json:"action"`
		Sender struct {
			ID int64 `json:"id"`
		} `json:"sender"`
	}
	if json.Unmarshal(data, &payload) != nil {
		problem(w, 400, "invalid_input")
		return
	}
	if payload.Action != "revoked" {
		w.WriteHeader(204)
		return
	}
	digest := sha256.Sum256(data)
	if !fail(w, a.Git.RevokeGitHubUser(r.Context(), id, payload.Sender.ID, digest[:])) {
		w.WriteHeader(204)
	}
}
func gitPage(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	values := r.URL.Query()[name]
	if len(values) == 0 {
		return 1, true
	}
	if len(values) != 1 {
		problem(w, 400, "invalid_input")
		return 0, false
	}
	v, err := strconv.Atoi(values[0])
	if err != nil || v < 1 || v > 10000 {
		problem(w, 400, "invalid_input")
		return 0, false
	}
	return v, true
}
func (a *API) gitCallback(w http.ResponseWriter, r *http.Request) {
	if a.Git == nil {
		problem(w, 503, "git_not_configured")
		return
	}
	if !a.authLimit(w, r) {
		return
	}
	q := r.URL.Query()
	states, codes := q["state"], q["code"]
	if len(r.URL.RawQuery) <= 4096 && len(states) == 1 && len(codes) == 0 && len(q["error"]) == 1 {
		if exchange, err := a.Git.Claim(r.Context(), states[0]); err == nil {
			_ = a.Git.Reject(r.Context(), exchange)
		}
		gitCallbackPage(w, false)
		return
	}
	if len(r.URL.RawQuery) > 4096 || len(states) != 1 || len(codes) != 1 || len(codes[0]) == 0 || len(codes[0]) > 512 || strings.ContainsAny(codes[0], "\x00\r\n") {
		gitCallbackPage(w, false)
		return
	}
	exchange, err := a.Git.Claim(r.Context(), states[0])
	if err == nil {
		err = a.Git.Exchange(r.Context(), exchange, codes[0])
	}
	gitCallbackPage(w, err == nil)
}
func gitCallbackPage(w http.ResponseWriter, success bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	text := "GitHub sign-in could not finish. Return to Capybara Camp and try again."
	if success {
		text = "GitHub is connected. You can close this tab and return to Capybara Camp."
	}
	_, _ = fmt.Fprintf(w, "<!doctype html><html lang=\"en\"><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width\"><title>Capybara Camp</title><h1>Capybara Camp</h1><p>%s</p></html>", text)
}
