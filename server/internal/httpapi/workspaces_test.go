package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
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
	unknownWorkspace := uuid.NewString()
	retentionJSON, _ := json.Marshal(map[string]any{"ids": []string{workspace.ID, unknownWorkspace}})
	call("POST", "/v1/runner/workspaces/retired", tokens[0], "", retentionJSON, 401)
	retiredRaw := call("POST", "/v1/runner/workspaces/retired", grant.Token, "", retentionJSON, 200)
	var retiredIDs []string
	if err = json.Unmarshal(retiredRaw, &retiredIDs); err != nil || len(retiredIDs) != 1 || retiredIDs[0] != unknownWorkspace {
		t.Fatal("retention endpoint removed active workspace", string(retiredRaw), err)
	}
	call("POST", "/v1/runner/workspaces/retired", grant.Token, "", []byte(`{"ids":[]}`), 400)
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
	executionPath := "/v1/camps/" + c.ID + "/sessions/" + session.ID + "/execution"
	assignment, _ := json.Marshal(camp.ExecutionInput{Location: "cloud", WorkspaceID: workspace.ID, ExpectedEpoch: 1, ExpectedSessionRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision, ExpectedWorkspaceRevision: 3})
	call("PUT", executionPath, "", uuid.NewString(), assignment, 401)
	call("PUT", executionPath, tokens[1], uuid.NewString(), assignment, 404)
	call("PUT", executionPath, tokens[0], uuid.NewString(), assignment, 200)
	call("GET", executionPath, tokens[1], "", nil, 404)
	call("GET", executionPath, tokens[0], "", nil, 200)
	{
		// A destination Mac gets a distinct owner/device-bound read credential
		// after capturing a reviewed export, never the runner's credential response.
		current, err := store.Session(ctx, actors[0], c.ID, session.ID)
		if err != nil {
			t.Fatal(err)
		}
		exportsPath := "/v1/camps/" + c.ID + "/sessions/" + session.ID + "/exports"
		exportBody, _ := json.Marshal(camp.WorkspaceExportInput{Kind: "capture", Paths: []string{"file.txt"}, ExpectedEpoch: current.ExecutionEpoch, ExpectedRevision: current.Revision, ExpectedTranscriptRevision: current.TranscriptRevision, ExpectedPermissionRevision: current.PermissionRevision})
		var exported camp.WorkspaceExport
		if err = json.Unmarshal(call("POST", exportsPath, tokens[0], uuid.NewString(), exportBody, 201), &exported); err != nil {
			t.Fatal(err)
		}
		exportClaim := uuid.NewString()
		claimBody, _ := json.Marshal(map[string]string{"claim_id": exportClaim})
		runnerExportPath := "/v1/runner/exports/" + exported.ID
		call("POST", runnerExportPath+"/claim", grant.Token, "", claimBody, 200)
		fileBytes := []byte("reviewed bytes")
		fileHash := sha256.Sum256(fileBytes)
		payload, _ := json.Marshal(transfer.Manifest{Version: 1, BaseCommit: workspace.BaseCommit, Branch: workspace.Branch, Files: []transfer.File{{Path: "file.txt", Data: fileBytes, SHA256: hex.EncodeToString(fileHash[:])}}})
		payloadHash := sha256.Sum256(payload)
		digest := hex.EncodeToString(payloadHash[:])
		resultBody, _ := json.Marshal(camp.WorkspaceExportResultInput{ClaimID: exportClaim, SHA256: digest, Bytes: len(payload)})
		call("POST", runnerExportPath+"/result", grant.Token, "", resultBody, 200)
		chunkBody, _ := json.Marshal(map[string]any{"claim_id": exportClaim, "data": payload, "sha256": digest})
		call("PUT", runnerExportPath+"/chunks/0", grant.Token, "", chunkBody, 200)
		call("POST", runnerExportPath+"/complete", grant.Token, "", claimBody, 200)
		tokenPath := exportsPath + "/" + exported.ID + "/checkout-token"
		checkoutBody, _ := json.Marshal(map[string]string{"sha256": digest})
		call("POST", tokenPath, "", "", checkoutBody, 401)
		call("POST", tokenPath, grant.Token, "", checkoutBody, 401)
		call("POST", tokenPath, tokens[1], "", checkoutBody, 404)
		call("POST", tokenPath, tokens[0], "", []byte(`{"sha256":"wrong"}`), 409)
		call("POST", tokenPath, tokens[0], "", []byte(`{"sha256":"wrong","device_id":"injected"}`), 400)
		checkoutRaw := call("POST", tokenPath, tokens[0], "", checkoutBody, 200)
		var checkout camp.ExportCheckoutToken
		if err = json.Unmarshal(checkoutRaw, &checkout); err != nil || checkout.DeviceID != actors[0].DeviceID || checkout.WorkspaceID != workspace.ID || checkout.Token == "" || checkout.SHA256 != digest {
			t.Fatal("invalid native checkout response", err)
		}
		if !bytes.Equal(checkoutRaw, call("POST", tokenPath, tokens[0], "", checkoutBody, 200)) {
			t.Fatal("lost native token response changed")
		}
		if bytes.Contains(call("GET", exportsPath+"/"+exported.ID, tokens[0], "", nil, 200), []byte(checkout.Token)) {
			t.Fatal("export metadata leaked credential")
		}
		call("POST", exportsPath+"/"+exported.ID+"/cancel", tokens[0], "", []byte(`{}`), 200)
		call("POST", tokenPath, tokens[0], "", checkoutBody, 409)
	}
	raw = call("POST", "/v1/camps/"+c.ID+"/sessions/"+session.ID+"/requests", tokens[0], uuid.NewString(), []byte(`{"prompt":"Use this workspace"}`), 201)
	var request camp.Request
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	requestClaim := uuid.NewString()
	requestBody, _ := json.Marshal(map[string]string{"claim_id": requestClaim})
	call("POST", runnerPath+"/requests/claim", tokens[0], "", requestBody, 401)
	call("POST", runnerPath+"/requests/claim", grant.Token, "", requestBody, 200)
	startBody, _ := json.Marshal(camp.RequestAction{Action: "start", LeaseToken: requestClaim})
	call("POST", "/v1/camps/"+c.ID+"/requests/"+request.ID+"/actions", tokens[0], uuid.NewString(), startBody, 404)
	call("POST", runnerPath+"/requests/"+request.ID+"/actions", grant.Token, uuid.NewString(), startBody, 200)
	controlID := uuid.NewString()
	controlBody, _ := json.Marshal(camp.ControlInput{ID: controlID, Control: map[string]any{"type": "steer", "id": controlID, "text": "Synthetic direction"}})
	controlsPath := "/v1/camps/" + c.ID + "/requests/" + request.ID + "/controls"
	call("POST", controlsPath, tokens[0], "", controlBody, 200)
	call("GET", controlsPath+"/"+controlID, "", "", nil, 401)
	call("GET", controlsPath+"/"+controlID, grant.Token, "", nil, 401)
	call("GET", controlsPath+"/"+controlID, tokens[1], "", nil, 404)
	var controlStatus camp.ControlStatus
	if err = json.Unmarshal(call("GET", controlsPath+"/"+controlID, tokens[0], "", nil, 200), &controlStatus); err != nil || controlStatus.State != "pending" {
		t.Fatal("control acceptance was fabricated", controlStatus, err)
	}
	finishBody, _ := json.Marshal(camp.RequestAction{Action: "finish", LeaseToken: requestClaim, Outcome: "completed", Evidence: "Fixture provider receipt"})
	call("POST", runnerPath+"/requests/"+request.ID+"/actions", grant.Token, uuid.NewString(), finishBody, 200)
	if err = json.Unmarshal(call("GET", controlsPath+"/"+controlID, tokens[0], "", nil, 200), &controlStatus); err != nil || controlStatus.State != "unconfirmed" {
		t.Fatal("unacknowledged control was reported accepted", controlStatus, err)
	}
	localBody, _ := json.Marshal(camp.ExecutionInput{Location: "local", ExpectedEpoch: 2, ExpectedSessionRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision})
	call("PUT", executionPath, tokens[0], uuid.NewString(), localBody, 200)
	raw = call("GET", readPath, tokens[0], "", nil, 200)
	if err = json.Unmarshal(raw, &workspace); err != nil || workspace.State != "cancelled" || workspace.Revision != 4 {
		t.Fatal("local handoff did not retire the unused workspace", err)
	}
	call("POST", readPath+"/cancel", tokens[1], uuid.NewString(), []byte(`{"expected_revision":1}`), 404)
	raw = call("POST", readPath+"/cancel", tokens[0], uuid.NewString(), []byte(`{"expected_revision":4}`), 200)
	if err = json.Unmarshal(raw, &workspace); err != nil || workspace.State != "cancelled" {
		t.Fatal("cancellation failed", err)
	}

	// A transfer uses a separate destination and only its claimed runner reads
	// the file payload. A staging receipt does not authorize execution.
	current, err := store.Session(ctx, actors[0], c.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	digest := func(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
	data := []byte("reviewed source contents")
	manifest := transfer.Manifest{Version: 1, BaseCommit: strings.Repeat("a", 40), Files: []transfer.File{{Path: "file.txt", Data: data, SHA256: digest(data)}}}
	manifest.History = &transfer.HistoryBinding{Version: 1, ParentID: strings.Repeat("c", 64), MainRevision: strings.Repeat("d", 64), Children: []transfer.HistoryChild{}, TranscriptRevision: current.TranscriptRevision}
	payload, _ := json.Marshal(manifest)
	review, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	create, _ := json.Marshal(camp.TransferInput{TargetLocation: "cloud", TargetAccountID: account.ID, ExpectedEpoch: current.ExecutionEpoch, ExpectedRevision: current.Revision, ExpectedTranscriptRevision: current.TranscriptRevision, ExpectedPermissionRevision: current.PermissionRevision, PayloadSHA256: digest(payload), ReviewSHA256: review, PayloadBytes: len(payload)})
	transferBase := "/v1/camps/" + c.ID + "/sessions/" + session.ID + "/transfers"
	var upload camp.Transfer
	if err = json.Unmarshal(call("POST", transferBase, tokens[0], uuid.NewString(), create, 201), &upload); err != nil {
		t.Fatal(err)
	}
	transferPath := transferBase + "/" + upload.ID
	chunkBody, _ := json.Marshal(camp.TransferChunk{Data: payload, SHA256: digest(payload)})
	call("PUT", transferPath+"/chunks/0", tokens[0], "", chunkBody, 200)
	if err = json.Unmarshal(call("POST", transferPath+"/complete", tokens[0], "", []byte(`{}`), 200), &upload); err != nil {
		t.Fatal(err)
	}
	call("POST", transferPath+"/workspaces", tokens[1], uuid.NewString(), body, 404)
	var destination camp.Workspace
	if err = json.Unmarshal(call("POST", transferPath+"/workspaces", tokens[0], uuid.NewString(), body, 201), &destination); err != nil {
		t.Fatal(err)
	}
	if destination.TransferID != upload.ID {
		t.Fatal("missing transfer binding")
	}
	destinationPath := "/v1/runner/workspaces/" + destination.ID
	transferClaim := uuid.NewString()
	claimBody, _ := json.Marshal(map[string]string{"claim_id": transferClaim})
	call("POST", destinationPath+"/transfer", tokens[0], "", claimBody, 401)
	call("POST", destinationPath+"/transfer", grant.Token, "", claimBody, 404)
	call("POST", destinationPath+"/claim", grant.Token, "", claimBody, 200)
	call("POST", destinationPath+"/transfer", grant.Token, "", claimBody, 200)
	call("POST", destinationPath+"/transfer/chunks/-1", grant.Token, "", claimBody, 400)
	var received camp.TransferChunk
	if err = json.Unmarshal(call("POST", destinationPath+"/transfer/chunks/0", grant.Token, "", claimBody, 200), &received); err != nil || !bytes.Equal(received.Data, payload) {
		t.Fatal("runner payload", err)
	}
	call("POST", destinationPath+"/token", grant.Token, "", claimBody, 200)
	stageReport := struct {
		ClaimID string `json:"claim_id"`
		camp.WorkspaceReport
	}{transferClaim, camp.WorkspaceReport{State: "ready", BaseCommit: destination.BaseCommit, Branch: destination.Branch}}
	stageBody, _ := json.Marshal(stageReport)
	call("POST", destinationPath+"/report", grant.Token, "", stageBody, 400)
	stageReport.TransferReceipt = &transfer.StageReceipt{ReviewSHA256: review, BaseCommit: destination.BaseCommit, Branch: destination.Branch, Files: 1}
	stageBody, _ = json.Marshal(stageReport)
	if err = json.Unmarshal(call("POST", destinationPath+"/report", grant.Token, "", stageBody, 200), &destination); err != nil {
		t.Fatal(err)
	}
	assignment, _ = json.Marshal(camp.ExecutionInput{Location: "cloud", WorkspaceID: destination.ID, ExpectedEpoch: current.ExecutionEpoch, ExpectedSessionRevision: current.Revision, ExpectedTranscriptRevision: current.TranscriptRevision, ExpectedWorkspaceRevision: destination.Revision})
	call("PUT", executionPath, tokens[0], uuid.NewString(), assignment, 409)
	handoffInput, _ := json.Marshal(camp.HandoffInput{WorkspaceID: destination.ID, ExpectedWorkspaceRevision: destination.Revision, ExpectedTransferRevision: upload.Revision})
	call("POST", transferPath+"/handoffs", tokens[1], uuid.NewString(), handoffInput, 404)
	var handoff camp.Handoff
	if err = json.Unmarshal(call("POST", transferPath+"/handoffs", tokens[0], uuid.NewString(), handoffInput, 201), &handoff); err != nil {
		t.Fatal(err)
	}
	call("GET", "/v1/camps/"+c.ID+"/handoffs/next", tokens[0], "", nil, 200)
	call("GET", "/v1/camps/"+c.ID+"/handoffs/next", tokens[1], "", nil, 204)
	call("GET", "/v1/runner/handoffs/next", grant.Token, "", nil, 204)
	handoffClaim := uuid.NewString()
	handoffBody, _ := json.Marshal(map[string]string{"claim_id": handoffClaim})
	handoffPath := "/v1/handoffs/" + handoff.ID
	call("POST", handoffPath+"/claim", "", "", handoffBody, 401)
	call("POST", handoffPath+"/claim", tokens[1], "", handoffBody, 404)
	call("POST", handoffPath+"/claim", tokens[0], "", handoffBody, 200)
	recoveryBody, _ := json.Marshal(map[string]any{"handoff_ids": []string{handoff.ID}})
	call("POST", "/v1/camps/"+c.ID+"/handoffs/recover", "", "", recoveryBody, 401)
	if raw := call("POST", "/v1/camps/"+c.ID+"/handoffs/recover", tokens[1], "", recoveryBody, 200); string(bytes.TrimSpace(raw)) != "[]" {
		t.Fatal("Teammate recovered owner's source")
	}
	var recoveries []camp.HandoffRecovery
	if err = json.Unmarshal(call("POST", "/v1/camps/"+c.ID+"/handoffs/recover", tokens[0], "", recoveryBody, 200), &recoveries); err != nil || len(recoveries) != 1 {
		t.Fatal("Owner recovery failed", err)
	}
	call("POST", handoffPath+"/source", tokens[0], "", handoffBody, 200)
	call("POST", handoffPath+"/chunks/0", tokens[0], "", handoffBody, 200)
	call("POST", "/v1/runner/handoffs/"+handoff.ID+"/source", grant.Token, "", handoffBody, 404)
	call("POST", "/v1/camps/"+c.ID+"/sessions/"+session.ID+"/requests", tokens[0], uuid.NewString(), []byte(`{"prompt":"Wait during handoff"}`), 409)
	ackBody, _ := json.Marshal(camp.HandoffAck{ClaimID: handoffClaim, Stopped: true, PayloadSHA256: digest(payload), ReviewSHA256: review})
	call("POST", handoffPath+"/ack", tokens[0], "", ackBody, 200)
	call("POST", handoffPath+"/ack", tokens[0], "", ackBody, 200)
	ownerHandoffPath := "/v1/camps/" + c.ID + "/sessions/" + session.ID + "/handoffs/" + handoff.ID
	call("GET", ownerHandoffPath, tokens[1], "", nil, 404)
	call("POST", ownerHandoffPath+"/release", tokens[0], "", []byte(`{}`), 200)
	call("POST", ownerHandoffPath+"/release", tokens[0], "", []byte(`{}`), 200)
	if err = json.Unmarshal(call("POST", transferPath+"/handoffs", tokens[0], uuid.NewString(), handoffInput, 201), &handoff); err != nil {
		t.Fatal(err)
	}
	handoffPath = "/v1/handoffs/" + handoff.ID
	ownerHandoffPath = "/v1/camps/" + c.ID + "/sessions/" + session.ID + "/handoffs/" + handoff.ID
	call("POST", handoffPath+"/claim", tokens[0], "", handoffBody, 200)
	if err = json.Unmarshal(call("POST", handoffPath+"/ack", tokens[0], "", ackBody, 200), &handoff); err != nil {
		t.Fatal(err)
	}
	commitBody, _ := json.Marshal(camp.HandoffCommitInput{ExpectedHandoffRevision: handoff.Revision})
	commitKey := uuid.NewString()
	call("POST", ownerHandoffPath+"/commit", "", commitKey, commitBody, 401)
	call("POST", ownerHandoffPath+"/commit", tokens[1], commitKey, commitBody, 404)
	if err = json.Unmarshal(call("POST", ownerHandoffPath+"/commit", tokens[0], commitKey, commitBody, 202), &handoff); err != nil {
		t.Fatal(err)
	}
	call("POST", ownerHandoffPath+"/commit", tokens[0], commitKey, commitBody, 202)
	proof, _ := json.Marshal(camp.HandoffFinishInput{HandoffAck: camp.HandoffAck{ClaimID: handoffClaim, Stopped: true, PayloadSHA256: digest(payload), ReviewSHA256: review}, CommitNonce: handoff.CommitNonce})
	call("POST", handoffPath+"/finish", tokens[1], "", proof, 404)
	call("POST", "/v1/runner/handoffs/"+handoff.ID+"/finish", grant.Token, "", proof, 404)
	if err = json.Unmarshal(call("POST", handoffPath+"/finish", tokens[0], "", proof, 200), &handoff); err != nil {
		t.Fatal(err)
	}
	if handoff.State != "committed" || handoff.ExecutionEpoch != current.ExecutionEpoch+1 {
		t.Fatal("HTTP handoff did not commit")
	}
	call("POST", handoffPath+"/finish", tokens[0], "", proof, 200)
	call("POST", handoffPath+"/claim", tokens[0], "", handoffBody, 200)
	call("POST", handoffPath+"/chunks/0", tokens[0], "", handoffBody, 404)
	call("POST", ownerHandoffPath+"/release", tokens[0], "", []byte(`{}`), 409)
	call("POST", transferPath+"/cancel", tokens[0], "", []byte(`{}`), 409)
	call("POST", destinationPath+"/transfer/chunks/0", grant.Token, "", claimBody, 404)
}
