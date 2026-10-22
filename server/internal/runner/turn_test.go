package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

// The test process implements the provider stdio protocols. Its executable is
// installed only in an isolated test PATH; no real provider is contacted.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "context-tool" {
		if localcli.New(config.Local{}).ServeContext(context.Background(), os.Args[2], os.Stdin, os.Stdout) != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if len(os.Args) > 4 && os.Args[1] == "provider-fixture" {
		runTurnFixture(os.Args[2], os.Args[3], os.Args[4])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fixtureRecord(path, value string) {
	f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if f != nil {
		_, _ = f.WriteString(value + "\n")
		_ = f.Close()
	}
}
func runTurnFixture(provider, mode, record string) {
	fixtureRecord(record, "process")
	decoder := json.NewDecoder(os.Stdin)
	decoder.UseNumber()
	encoder := json.NewEncoder(os.Stdout)
	read := func() map[string]any {
		var v map[string]any
		if decoder.Decode(&v) != nil {
			os.Exit(0)
		}
		return v
	}
	out := func(v any) {
		if encoder.Encode(v) != nil {
			os.Exit(0)
		}
	}
	object := func(v any) map[string]any { m, _ := v.(map[string]any); return m }
	if provider == "Codex" {
		thread := ""
		for {
			v := read()
			params := object(v["params"])
			result := map[string]any{}
			switch v["method"] {
			case "initialized":
				continue
			case "config/read":
				result["config"] = map[string]any{}
			case "configRequirements/read":
				result["requirements"] = map[string]any{}
			case "thread/start", "thread/resume":
				fixtureRecord(record, v["method"].(string))
				thread, _ = params["threadId"].(string)
				if thread == "" {
					thread = uuid.NewString()
				}
				result["thread"] = map[string]any{"id": thread}
				result["approvalPolicy"] = params["approvalPolicy"]
				result["approvalsReviewer"] = params["approvalsReviewer"]
				if params["sandbox"] != nil {
					result["sandbox"] = map[string]any{"type": "workspaceWrite"}
				}
			case "turn/start":
				fixtureRecord(record, "turn")
				out(map[string]any{"id": v["id"], "result": map[string]any{"turn": map[string]any{"id": "active"}}})
				if mode == "approval" {
					out(map[string]any{"id": "fixture-approval", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": thread, "turnId": "active", "itemId": "cmd", "command": "echo synthetic"}})
					answer := read()
					if object(answer["result"])["decision"] != "accept" {
						os.Exit(3)
					}
				}
				out(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": thread, "turnId": "active", "itemId": "reply", "delta": "Cloud reply"}})
				if mode == "stall" {
					continue
				}
				out(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": map[string]any{"id": "active", "status": "completed"}}})
				continue
			}
			out(map[string]any{"id": v["id"], "result": result})
		}
	}
	v := read()
	if v["type"] == "control_request" {
		out(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": v["request_id"]}})
		v = read()
	}
	if v["type"] != "user" {
		os.Exit(3)
	}
	fixtureRecord(record, "turn")
	session := v["session_id"]
	out(map[string]any{"type": "system", "subtype": "init", "session_id": session, "permissionMode": "default"})
	if mode == "approval" {
		out(map[string]any{"type": "control_request", "request_id": "fixture-approval", "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": "echo synthetic"}}})
		answer := read()
		if object(object(answer["response"])["response"])["behavior"] != "allow" {
			os.Exit(3)
		}
	}
	out(map[string]any{"type": "stream_event", "session_id": session, "event": map[string]any{"delta": map[string]any{"type": "text_delta", "text": "Cloud reply"}}})
	if mode != "stall" {
		out(map[string]any{"type": "result", "session_id": session, "is_error": false, "result": "Cloud reply"})
	}
	for {
		read()
	}
}

func runtimeCredentials(provider string) (string, json.RawMessage) {
	var files map[string]json.RawMessage
	subject := "claude:org:account"
	if provider == "Codex" {
		claims, _ := json.Marshal(map[string]any{"sub": "user"})
		auth, _ := json.Marshal(map[string]any{"tokens": map[string]any{"account_id": "account", "id_token": "x." + base64.RawURLEncoding.EncodeToString(claims) + ".x", "access_token": "fixture-access", "refresh_token": "fixture-refresh"}})
		files = map[string]json.RawMessage{"auth.json": auth}
		subject = "codex:account:user"
	} else {
		files = map[string]json.RawMessage{".credentials.json": json.RawMessage(`{"claudeAiOauth":{"accessToken":"fixture-access","refreshToken":"fixture-refresh"}}`), ".claude.json": json.RawMessage(`{"oauthAccount":{"accountUuid":"account","organizationUuid":"org"}}`)}
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "provider": provider, "files": files})
	return subject, raw
}

func turnFixture(t *testing.T, provider, mode string, wraps ...func(http.Handler) http.Handler) (fixture, *camp.GitStore, camp.Workspace, string) {
	t.Helper()
	remote, sha, _ := gitFixtureServer(t, nil, nil)
	subject, credentials := runtimeCredentials(provider)
	var wrap func(http.Handler) http.Handler
	if len(wraps) > 0 {
		wrap = wraps[0]
	}
	purpose := "planning"
	if mode == "approval" {
		purpose = "implementation"
	}
	f, g, _, workspace := workerWorkspaceIdentity(t, remote, sha, wrap, provider, subject, credentials, purpose)
	bin := privateTemp(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "calls")
	wrapper := "#!/bin/sh\nexec " + shellQuote(self) + " provider-fixture " + shellQuote(provider) + " " + shellQuote(mode) + " " + shellQuote(record) + " \"$@\"\n"
	for _, name := range []string{"codex", "claude"} {
		if err = os.WriteFile(filepath.Join(bin, name), []byte(wrapper), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.worker.environment = []string{"PATH=" + bin}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerWorkspace(t.Context(), f.grant.Token, workspace.ID, claim); err != nil {
		t.Fatal(err)
	}
	token, err := g.RunnerWorkspaceToken(t.Context(), f.grant.Token, workspace.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.worker.checkout.prepare(t.Context(), workspace, token); err != nil {
		t.Fatal(err)
	}
	workspace, err = g.ReportRunnerWorkspace(t.Context(), f.grant.Token, workspace.ID, claim, camp.WorkspaceReport{State: "ready", BaseCommit: workspace.BaseCommit, Branch: workspace.Branch})
	if err != nil {
		t.Fatal(err)
	}
	assignment := camp.ExecutionInput{Location: "cloud", WorkspaceID: workspace.ID, ExpectedEpoch: 1, ExpectedSessionRevision: workspace.SourceRevision, ExpectedWorkspaceRevision: workspace.Revision}
	if purpose == "implementation" {
		assignment.ExecutionApproved = true
		assignment.Permissions = localcli.PermissionSettings{"sandbox": "workspace-write", "approval": "on-request", "reviewer": "user"}
		if provider == "Claude" {
			assignment.Permissions = localcli.PermissionSettings{"claudeMode": "manual"}
		}
	}
	if _, err = f.store.AssignExecution(t.Context(), f.owner, workspace.CampID, workspace.SessionID, uuid.NewString(), assignment); err != nil {
		t.Fatal(err)
	}
	return f, g, workspace, record
}

func TestRunnerApprovalChannelsBothProviders(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			f, _, w, _ := turnFixture(t, provider, "approval")
			r, err := f.store.CreateRequest(t.Context(), f.owner, w.CampID, w.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Run the approved fixture"})
			if err != nil {
				t.Fatal(err)
			}
			cancel, done := startWorker(t, f.worker)
			var approval map[string]any
			var seq int64
			eventually(t, func() bool {
				page, e := f.store.RequestStream(t.Context(), f.owner, w.CampID, r.ID, 0)
				if e != nil {
					return false
				}
				for _, event := range page.Events {
					var v map[string]any
					_ = json.Unmarshal(event.Event, &v)
					if v["type"] == "approval" {
						approval = v
						seq = event.Seq
						return true
					}
				}
				return false
			})
			session, e := f.store.Session(t.Context(), f.owner, w.CampID, w.SessionID)
			if e != nil || session.Stage != "Needs you" {
				t.Fatal("approval did not update card", session, e)
			}
			if _, err = f.store.SendControl(t.Context(), f.owner, w.CampID, r.ID, camp.ControlInput{ID: uuid.NewString(), ApprovalSeq: &seq, Control: map[string]any{"type": "approval", "id": approval["id"], "allow": true}}); err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool {
				r, e := f.store.Request(t.Context(), f.owner, w.CampID, r.ID)
				return e == nil && r.State == "completed"
			})
			stopWorker(t, cancel, done)
		})
	}
}

func TestRunnerSchedulesMultipleTicketsAndRecoversTerminalReceipt(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			var dropped atomic.Bool
			f, g, first, record := turnFixture(t, provider, "complete", func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/actions") {
						raw, _ := io.ReadAll(r.Body)
						r.Body = io.NopCloser(bytes.NewReader(raw))
						var input camp.RequestAction
						_ = json.Unmarshal(raw, &input)
						if input.Action == "finish" && dropped.CompareAndSwap(false, true) {
							next.ServeHTTP(discardResponse{}, r)
							w.WriteHeader(503)
							return
						}
					}
					next.ServeHTTP(w, r)
				})
			})
			second := siblingWorkspace(t, f, g, first)
			cancel, done := startWorker(t, f.worker)
			eventually(t, func() bool {
				w, err := g.Workspace(t.Context(), f.owner, second.CampID, second.ID)
				return err == nil && w.State == "ready"
			})
			stopWorker(t, cancel, done)
			var err error
			second, err = g.Workspace(t.Context(), f.owner, second.CampID, second.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.AssignExecution(t.Context(), f.owner, second.CampID, second.SessionID, uuid.NewString(), camp.ExecutionInput{Location: "cloud", WorkspaceID: second.ID, ExpectedEpoch: 1, ExpectedSessionRevision: second.SourceRevision, ExpectedWorkspaceRevision: second.Revision}); err != nil {
				t.Fatal(err)
			}
			requests := []camp.Request{}
			for _, w := range []camp.Workspace{second, first, second} {
				r, err := f.store.CreateRequest(t.Context(), f.owner, w.CampID, w.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Run ticket " + w.ID})
				if err != nil {
					t.Fatal(err)
				}
				requests = append(requests, r)
			}
			cancel, done = startWorker(t, f.worker)
			eventually(t, func() bool {
				for _, r := range requests {
					current, err := f.store.Request(t.Context(), f.owner, r.CampID, r.ID)
					if err != nil || current.State != "completed" {
						return false
					}
				}
				return true
			})
			stopWorker(t, cancel, done)
			raw, err := os.ReadFile(record)
			if err != nil || strings.Count(string(raw), "turn\n") != 3 || !dropped.Load() {
				t.Fatal("lost receipt duplicated or stalled work", string(raw), err)
			}
			var histories int
			if err = f.api.Pool.QueryRow(t.Context(), `SELECT count(DISTINCT provider_session_id) FROM workspace_runtime WHERE workspace_id=ANY($1::uuid[])`, []string{first.ID, second.ID}).Scan(&histories); err != nil || histories != 2 {
				t.Fatal("ticket histories were mixed", histories, err)
			}
			if provider == "Codex" && strings.Count(string(raw), "thread/resume\n") != 1 {
				t.Fatal("existing ticket did not resume its own provider history", string(raw))
			}
			for _, w := range []camp.Workspace{first, second} {
				if err = f.worker.checkout.verify(t.Context(), w); err != nil {
					t.Fatal("ticket checkout not retained", err)
				}
			}
		})
	}
}

func TestRunnerLostStartAndFinishRepliesNeverRepeatProvider(t *testing.T) {
	seen := map[string]bool{}
	f, _, w, record := turnFixture(t, "Codex", "complete", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/actions") {
				var action map[string]any
				raw, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(raw))
				_ = json.Unmarshal(raw, &action)
				name, _ := action["action"].(string)
				if (name == "start" || name == "finish") && !seen[name] {
					seen[name] = true
					next.ServeHTTP(discardResponse{}, r)
					writer.WriteHeader(503)
					return
				}
			}
			next.ServeHTTP(writer, r)
		})
	})
	r, err := f.store.CreateRequest(t.Context(), f.owner, w.CampID, w.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Exactly one provider submission"})
	if err != nil {
		t.Fatal(err)
	}
	cancel, done := startWorker(t, f.worker)
	eventually(t, func() bool {
		r, e := f.store.Request(t.Context(), f.owner, w.CampID, r.ID)
		return e == nil && r.State == "completed"
	})
	eventually(t, func() bool { r, e := f.worker.checkout.loadTurn(); return e == nil && r == nil })
	stopWorker(t, cancel, done)
	data, _ := os.ReadFile(record)
	if strings.Count(string(data), "turn\n") != 1 {
		t.Fatal("provider repeated after lost response", string(data))
	}
}

func TestRunnerRestartHoldsUnknownTurn(t *testing.T) {
	f, _, w, record := turnFixture(t, "Codex", "stall")
	r, err := f.store.CreateRequest(t.Context(), f.owner, w.CampID, w.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Interrupted turn"})
	if err != nil {
		t.Fatal(err)
	}
	cancel, done := startWorker(t, f.worker)
	eventually(t, func() bool { data, _ := os.ReadFile(record); return strings.Contains(string(data), "turn\n") })
	stopWorker(t, cancel, done)
	if _, err = f.api.Pool.Exec(t.Context(), `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.ReapRequests(t.Context()); err != nil {
		t.Fatal(err)
	}
	cancel, done = startWorker(t, f.worker)
	time.Sleep(80 * time.Millisecond)
	stopWorker(t, cancel, done)
	data, _ := os.ReadFile(record)
	if strings.Count(string(data), "turn\n") != 1 {
		t.Fatal("interrupted provider was restarted", string(data))
	}
	state, err := f.store.Request(t.Context(), f.owner, w.CampID, r.ID)
	if err != nil || state.State != "outcome_unknown" {
		t.Fatal(state, err)
	}
}

func TestRunnerExecutesBothProvidersAndPersistsTranscript(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			f, _, w, record := turnFixture(t, provider, "complete")
			request, err := f.store.CreateRequest(t.Context(), f.owner, w.CampID, w.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Hello cloud"})
			if err != nil {
				t.Fatal(err)
			}
			cancel, done := startWorker(t, f.worker)
			eventually(t, func() bool {
				r, err := f.store.Request(t.Context(), f.owner, w.CampID, request.ID)
				return err == nil && r.State == "completed"
			})
			second, err := f.store.CreateRequest(t.Context(), f.owner, w.CampID, w.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Continue in the same cloud session"})
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool {
				r, err := f.store.Request(t.Context(), f.owner, w.CampID, second.ID)
				return err == nil && r.State == "completed"
			})
			stopWorker(t, cancel, done)
			transcript, err := f.store.Transcript(t.Context(), f.owner, w.CampID, w.SessionID, 0, 0, 20)
			if err != nil || len(transcript.Messages) != 4 {
				t.Fatal(transcript, err)
			}
			found := false
			for _, m := range transcript.Messages {
				found = found || m.Text == "Cloud reply"
			}
			if !found {
				t.Fatal("stream was not persisted")
			}
			data, err := os.ReadFile(record)
			if err != nil || strings.Count(string(data), "turn\n") != 2 {
				t.Fatal("provider did not execute once per prompt", err, string(data))
			}
			if provider == "Codex" && strings.Count(string(data), "thread/resume\n") != 1 {
				t.Fatal("provider session was not resumed")
			}
		})
	}
}

func TestRunnerCancellationStopsProviderAndRetainsWorkspace(t *testing.T) {
	f, _, w, record := turnFixture(t, "Codex", "stall")
	r, err := f.store.CreateRequest(t.Context(), f.owner, w.CampID, w.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Wait"})
	if err != nil {
		t.Fatal(err)
	}
	cancel, done := startWorker(t, f.worker)
	eventually(t, func() bool { raw, _ := os.ReadFile(record); return strings.Contains(string(raw), "turn\n") })
	if _, err = f.store.ActOnRequest(t.Context(), f.owner, w.CampID, r.ID, uuid.NewString(), camp.RequestAction{Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		r, e := f.store.Request(t.Context(), f.owner, w.CampID, r.ID)
		return e == nil && r.State == "cancelled"
	})
	stopWorker(t, cancel, done)
	if err = f.worker.checkout.cleanAll(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(f.worker.checkout.workdir(w)); err != nil {
		t.Fatal("used workspace was removed", err)
	}
}

func TestRunnerRotatedCredentialsUseRevisionAndIdentityFence(t *testing.T) {
	f, _, _, _ := turnFixture(t, "Claude", "normal")
	original, err := f.store.RunnerCredentialSnapshot(t.Context(), f.grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err = json.Unmarshal(original.Credentials, &snapshot); err != nil {
		t.Fatal(err)
	}
	files := snapshot["files"].(map[string]any)
	files[".credentials.json"].(map[string]any)["claudeAiOauth"].(map[string]any)["accessToken"] = "fixture-rotated"
	raw, _ := json.Marshal(snapshot)
	input := camp.RunnerCredentialUpdate{ExpectedRevision: original.Revision, Credentials: raw}
	updated, err := f.store.UpdateRunnerCredentials(t.Context(), f.grant.Token, input)
	if err != nil || updated.Revision != original.Revision+1 {
		t.Fatal(updated.Revision, err)
	}
	replay, err := f.store.UpdateRunnerCredentials(t.Context(), f.grant.Token, input)
	if err != nil || replay.Revision != updated.Revision {
		t.Fatal("rotation replay", err)
	}
	if _, err = f.store.UpdateRunnerCredentials(t.Context(), f.grant.Token, camp.RunnerCredentialUpdate{ExpectedRevision: original.Revision, Credentials: original.Credentials}); err == nil {
		t.Fatal("old credentials overwrote rotation")
	}
	files[".claude.json"].(map[string]any)["oauthAccount"].(map[string]any)["accountUuid"] = "different"
	wrong, _ := json.Marshal(snapshot)
	if _, err = f.store.UpdateRunnerCredentials(t.Context(), f.grant.Token, camp.RunnerCredentialUpdate{ExpectedRevision: updated.Revision, Credentials: wrong}); err == nil {
		t.Fatal("changed identity accepted")
	}
}
