package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

type workspaceHTTPProvider struct{ gitHTTPProvider }

func (workspaceHTTPProvider) ScopeRead(_ context.Context, _ string, expiry time.Time, user int64, _ gitprovider.Installation, repo gitprovider.Repository) (gitprovider.ReadGrant, error) {
	return gitprovider.ReadGrant{Token: "ghu_" + strings.Repeat("n", 36), ExpiresAt: time.Now().Add(5 * time.Minute), RepositoryID: repo.ID, UserID: user}, nil
}

func (workspaceHTTPProvider) Repositories(context.Context, string, gitprovider.Installation, int) (gitprovider.Repositories, error) {
	return gitprovider.Repositories{Values: []gitprovider.Repository{{ID: 20, InstallationID: 10, FullName: "test-user/project", DefaultBranch: "main", Read: true}}}, nil
}

func (workspaceHTTPProvider) ResolveCommit(context.Context, string, gitprovider.Repository, string) (string, error) {
	return strings.Repeat("a", 40), nil
}

func TestWorkspaceHTTPRequiresOwnerAndReplaysReceipt(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Open(t)
	store, err := camp.NewWithProviderKey(pool, "fixture-secret", bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	g, err := camp.NewGitStore(store, workspaceHTTPProvider{}, bytes.Repeat([]byte{37}, 32))
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Pool: pool, Auth: auth.New(pool, strings.Repeat("s", 32), "https://camp.example", unusedMail{}), Camps: store, Git: g, Origin: "https://camp.example"}
	tokens := make([]string, 2)
	actors := make([]auth.Actor, 2)
	for i := range actors {
		actors[i] = auth.Actor{UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: uuid.NewString() + "@example.com", Name: "Fixture person"}
		tokens[i], err = auth.Token()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, actors[i].UserID, actors[i].Email, actors[i].Name); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Fixture',$3,now()+interval '1 day')`, actors[i].DeviceID, actors[i].UserID, auth.HashToken(tokens[i])); err != nil {
			t.Fatal(err)
		}
	}
	c, err := store.Create(ctx, actors[0], uuid.NewString(), "Workspace fixture")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := store.Invite(ctx, actors[0], c.ID, uuid.NewString(), actors[1].Email, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Accept(ctx, actors[1], uuid.NewString(), inv.Token); err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, actors[0], c.ID, uuid.NewString(), "Client project")
	if err != nil {
		t.Fatal(err)
	}
	project, err = store.UpdateProject(ctx, actors[0], c.ID, project.ID, uuid.NewString(), camp.ProjectUpdate{Name: project.Name, Repositories: []string{"https://github.com/test-user/project"}, ExpectedRevision: project.Revision})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.RegisterProfile(ctx, actors[0], uuid.NewString(), camp.ProfileInput{LocalID: strings.Repeat("a", 64), Provider: "Codex", Label: "Local fixture"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), camp.SessionInput{ID: uuid.NewString(), ProjectID: project.ID, ProfileID: profile.ID, LocalSessionID: "fixture", Title: "Prepare a workspace", State: "idle", Stage: "Ready", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateProviderAccount(ctx, actors[0], uuid.NewString(), camp.ProviderAccountInput{Provider: "Codex", Label: "Cloud fixture"})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := store.CreateRunnerGrant(ctx, actors[0], account.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	login, err := store.BeginProviderLogin(ctx, actors[0], account.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	claim := uuid.NewString()
	if _, err = store.ClaimRunnerLogin(ctx, grant.Token, login.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, camp.RunnerLoginReport{State: "connected", VerifiedSubject: "fixture-owner", Credentials: json.RawMessage(`{"fixture_token":"private-fixture"}`)}); err != nil {
		t.Fatal(err)
	}
	gitLogin, err := g.Begin(ctx, actors[0], uuid.NewString(), "")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(gitLogin.URL)
	if err != nil {
		t.Fatal(err)
	}
	x, err := g.Claim(ctx, u.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Exchange(ctx, x, "fixture-code"); err != nil {
		t.Fatal(err)
	}
	input := camp.WorkspaceInput{ProviderAccountID: account.ID, GitConnectionID: gitLogin.ConnectionID, GitHubInstallationID: 10, RepositoryID: 20, InstallationPage: 1, RepositoryPage: 1, Purpose: "planning", ExpectedSessionRevision: session.Revision, ExpectedProjectRevision: project.Revision}
	body, _ := json.Marshal(input)
	handler := api.Handler()
	call := func(method, path, token, key string, body []byte, status int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.RemoteAddr = "127.0.0.1:12345"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s workspace: got %d want %d (%s)", method, w.Code, status, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("workspace response cacheable")
		}
		return w.Body.Bytes()
	}
	path := "/v1/camps/" + c.ID + "/sessions/" + session.ID + "/workspaces"
	call("POST", path, "", uuid.NewString(), body, 401)
	call("POST", path, tokens[1], uuid.NewString(), body, 404)
	call("POST", path, tokens[0], uuid.NewString(), append(body[:len(body)-1], []byte(`,"credentials":"rejected"}`)...), 400)
	body, _ = json.Marshal(input)
	key := uuid.NewString()
	raw := call("POST", path, tokens[0], key, body, 201)
	var workspace camp.Workspace
	if err = json.Unmarshal(raw, &workspace); err != nil {
		t.Fatal(err)
	}
	if workspace.State != "queued" || workspace.BaseCommit != strings.Repeat("a", 40) {
		t.Fatal("wrong workspace response")
	}
	if strings.Contains(string(raw), "ghu_") || strings.Contains(string(raw), "private-fixture") {
		t.Fatal("credential leaked")
	}
	if !bytes.Equal(raw, call("POST", path, tokens[0], key, body, 201)) {
		t.Fatal("workspace replay changed")
	}
	readPath := "/v1/camps/" + c.ID + "/workspaces/" + workspace.ID
	call("GET", readPath, tokens[1], "", nil, 404)
	call("GET", readPath, tokens[0], "", nil, 200)
	runnerPath := "/v1/runner/workspaces/" + workspace.ID
	claimID := uuid.NewString()
	claimJSON, _ := json.Marshal(map[string]string{"claim_id": claimID})
	call("POST", runnerPath+"/claim", tokens[0], "", claimJSON, 401)
	call("GET", "/v1/runner/workspaces/next", grant.Token, "", nil, 200)
	raw = call("POST", runnerPath+"/claim", grant.Token, "", claimJSON, 200)
	var claimResult camp.RunnerWorkspaceClaim
	if err = json.Unmarshal(raw, &claimResult); err != nil || claimResult.Workspace.State != "preparing" {
		t.Fatal("runner claim failed", err)
	}
	raw = call("POST", runnerPath+"/token", grant.Token, "", claimJSON, 200)
	var readToken camp.RunnerWorkspaceToken
	if err = json.Unmarshal(raw, &readToken); err != nil || readToken.Token == "" {
		t.Fatal("runner token missing", err)
	}
	if !bytes.Equal(raw, call("POST", runnerPath+"/token", grant.Token, "", claimJSON, 200)) {
		t.Fatal("token redelivery changed")
	}
	public := call("GET", readPath, tokens[0], "", nil, 200)
	if bytes.Contains(public, []byte(readToken.Token)) {
		t.Fatal("token in public record")
	}
	call("POST", runnerPath+"/heartbeat", grant.Token, "", claimJSON, 200)
	reportJSON, _ := json.Marshal(map[string]string{"claim_id": claimID, "state": "ready", "base_commit": workspace.BaseCommit, "branch": workspace.Branch})
	call("POST", runnerPath+"/report", grant.Token, "", reportJSON, 200)
	call("POST", runnerPath+"/report", grant.Token, "", reportJSON, 200)
	call("POST", runnerPath+"/token", grant.Token, "", claimJSON, 409)
	call("POST", readPath+"/cancel", tokens[1], uuid.NewString(), []byte(`{"expected_revision":1}`), 404)
	raw = call("POST", readPath+"/cancel", tokens[0], uuid.NewString(), []byte(`{"expected_revision":3}`), 200)
	if err = json.Unmarshal(raw, &workspace); err != nil || workspace.State != "cancelled" {
		t.Fatal("cancellation failed", err)
	}
}
