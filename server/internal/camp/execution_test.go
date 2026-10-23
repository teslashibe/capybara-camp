package camp

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func executionFixture(t *testing.T, purpose string) (*GitStore, Workspace, RunnerGrant, auth.Actor, ExecutionInput) {
	t.Helper()
	g, _, actors, c, session, input := workspaceFixture(t)
	input.Purpose = purpose
	w, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := g.store.CreateRunnerGrant(t.Context(), actors[0], input.ProviderAccountID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = g.RunnerWorkspaceToken(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	w, err = g.ReportRunnerWorkspace(t.Context(), grant.Token, w.ID, claim, WorkspaceReport{State: "ready", BaseCommit: w.BaseCommit, Branch: w.Branch})
	if err != nil {
		t.Fatal(err)
	}
	return g, w, grant, actors[0], ExecutionInput{Location: "cloud", WorkspaceID: w.ID, ExpectedEpoch: session.ExecutionEpoch, ExpectedSessionRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision, ExpectedWorkspaceRevision: w.Revision}
}

func TestCloudAssignmentClaimsAndActivityUseOneQueue(t *testing.T) {
	g, w, grant, owner, input := executionFixture(t, "planning")
	s := g.store
	key := uuid.NewString()
	target, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, key, input)
	if err != nil || target.Location != "cloud" || target.Epoch != 2 || target.Mode != "chat" {
		t.Fatal(target, err)
	}
	if replay, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, key, input); err != nil || replay.Epoch != target.Epoch {
		t.Fatal("assignment replay", err)
	}
	r, err := s.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Use this cloud workspace", Model: "fixture-model"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ExecutionEpoch != 2 || r.ExecutionWorkspaceID != w.ID || r.ExecutionLocation != "cloud" {
		t.Fatal("request not pinned", r)
	}
	local, err := s.ClaimAvailableWork(t.Context(), owner, w.CampID, uuid.NewString(), []string{w.SessionID})
	if err != nil || local != nil {
		t.Fatal("Mac stole cloud work", err)
	}
	_, foreign := addCloud(t, s, owner, "Codex")
	if _, err = g.ClaimRunnerRequest(t.Context(), foreign.Token, w.ID, uuid.NewString()); err == nil {
		t.Fatal("different installation claimed prompt")
	}
	claim := uuid.NewString()
	claimed, err := g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, claim)
	if err != nil || claimed == nil || claimed.ID != r.ID {
		t.Fatal(claimed, err)
	}
	if replay, err := g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, claim); err != nil || replay == nil || replay.LeaseToken != claimed.LeaseToken {
		t.Fatal("claim replay", err)
	}
	if _, err = s.ActOnRequest(t.Context(), owner, w.CampID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claim}); !errors.Is(err, ErrDenied) {
		t.Fatal("Mac started cloud request", err)
	}
	startKey := uuid.NewString()
	started, err := g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, startKey, RequestAction{Action: "start", LeaseToken: claim})
	if err != nil || started.State != "started" {
		t.Fatal(started, err)
	}
	session, err := s.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil || session.State != "running" || session.Stage != "In progress" {
		t.Fatal("running card not updated", session, err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, startKey, RequestAction{Action: "start", LeaseToken: claim}); err != nil {
		t.Fatal("lost start response", err)
	}
	followup, err := s.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Queued follow-up"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(t.Context(), owner, w.CampID, followup.ID, uuid.NewString(), RequestAction{Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	session, err = s.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil || session.State != "running" || session.Stage != "In progress" {
		t.Fatal("queued cancellation hid the running turn", session, err)
	}
	finished, err := g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: claim, Outcome: "completed", Evidence: "Provider terminal receipt"})
	if err != nil || finished.State != "completed" {
		t.Fatal(finished, err)
	}
	session, err = s.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil || session.Stage != "Done" {
		t.Fatal(session, err)
	}
	back := ExecutionInput{Location: "local", ExpectedEpoch: 2, ExpectedSessionRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision}
	if _, err = g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), back); err != nil {
		t.Fatal(err)
	}
	if _, err = g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, uuid.NewString()); err == nil {
		t.Fatal("old assignment remained executable")
	}
	retired, err := g.Workspace(t.Context(), owner, w.CampID, w.ID)
	if err != nil || retired.State != "cancelled" {
		t.Fatal("unused workspace kept its account preparation slot", retired, err)
	}
	nextSession, err := s.PublishSession(t.Context(), owner, w.CampID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: w.ProjectID, ProfileID: session.ProfileID, LocalSessionID: uuid.NewString(), Title: "Next ticket", State: "idle", Stage: "Ready"})
	if err != nil {
		t.Fatal(err)
	}
	nextWorkspace, err := g.CreateWorkspace(t.Context(), owner, w.CampID, nextSession.ID, uuid.NewString(), WorkspaceInput{ProviderAccountID: w.ProviderAccountID, GitConnectionID: w.GitConnectionID, GitHubInstallationID: w.GitHubInstallationID, RepositoryID: w.RepositoryID, InstallationPage: 1, RepositoryPage: 1, Purpose: "planning", ExpectedSessionRevision: nextSession.Revision, ExpectedProjectRevision: w.ProjectRevision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.ClaimRunnerWorkspace(t.Context(), grant.Token, nextWorkspace.ID, uuid.NewString()); err != nil {
		t.Fatal("next ticket could not prepare after local handoff", err)
	}
}

func TestAssignmentRefusesPendingWorkAndStaleConsent(t *testing.T) {
	g, w, _, owner, input := executionFixture(t, "planning")
	request, err := g.store.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Keep on Mac"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); !errors.Is(err, ErrTransition) {
		t.Fatal("queued work moved silently", err)
	}
	if _, err = g.store.ActOnRequest(t.Context(), owner, w.CampID, request.ID, uuid.NewString(), RequestAction{Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	stale := input
	stale.ExpectedTranscriptRevision++
	if _, err = g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), stale); !errors.Is(err, ErrRevision) {
		t.Fatal("stale history consent", err)
	}
	if _, err = g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.WriteTranscript(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "stale-mac", Role: "assistant", Text: "Old local output"}}}); !errors.Is(err, ErrTransition) {
		t.Fatal("Mac overwrote cloud transcript", err)
	}
}

func TestImplementationAssignmentRequiresExplicitPermissions(t *testing.T) {
	g, w, grant, owner, input := executionFixture(t, "implementation")
	if _, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); !errors.Is(err, ErrDenied) {
		t.Fatal("unapproved implementation", err)
	}
	input.ExecutionApproved = true
	if _, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); !errors.Is(err, ErrInput) {
		t.Fatal("implicit permissions accepted", err)
	}
	input.Permissions = localcli.PermissionSettings{"sandbox": "workspace-write", "approval": "on-request", "reviewer": "user"}
	target, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input)
	if err != nil || target.Mode != "implementation" {
		t.Fatal(target, err)
	}
	r, err := g.store.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Implement it"})
	if err != nil || r.Mode != "implementation" || r.Permissions["sandbox"] != "workspace-write" {
		t.Fatal(r, err)
	}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claim}); err != nil {
		t.Fatal(err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: claim, Outcome: "completed", Evidence: "Fixture result"}); err != nil {
		t.Fatal(err)
	}
	back := ExecutionInput{Location: "local", ExpectedEpoch: 2, ExpectedSessionRevision: input.ExpectedSessionRevision, ExpectedTranscriptRevision: input.ExpectedTranscriptRevision}
	if _, err = g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), back); !errors.Is(err, ErrTransition) {
		t.Fatal("implementation switched without reviewing file transfer", err)
	}
}

func TestExpiredCloudStartNeedsReconciliation(t *testing.T) {
	g, w, grant, owner, input := executionFixture(t, "planning")
	if _, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	r, err := g.store.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "One attempt"})
	if err != nil {
		t.Fatal(err)
	}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claim}); err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.pool.Exec(t.Context(), `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = g.store.ReapRequests(t.Context()); err != nil {
		t.Fatal(err)
	}
	session, err := g.store.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil || session.Stage != "Needs you" {
		t.Fatal("expired turn kept running state", err)
	}
	if again, err := g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, uuid.NewString()); err != nil || again != nil {
		t.Fatal("uncertain provider work repeated", err)
	}
	replacement, err := g.store.CreateRunnerGrant(t.Context(), owner, w.ProviderAccountID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), replacement.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "reconcile", LeaseToken: claim, Outcome: "completed", Evidence: "Unknown"}); !errors.Is(err, ErrDenied) {
		t.Fatal("new worker adopted uncertain turn", err)
	}
	done, err := g.store.ActOnRequest(t.Context(), owner, w.CampID, r.ID, uuid.NewString(), RequestAction{Action: "reconcile", Outcome: "failed", Evidence: "Owner reviewed the interrupted attempt"})
	if err != nil || done.State != "failed" {
		t.Fatal(done, err)
	}
	back := ExecutionInput{Location: "local", ExpectedEpoch: 2, ExpectedSessionRevision: input.ExpectedSessionRevision, ExpectedTranscriptRevision: input.ExpectedTranscriptRevision}
	if _, err = g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), back); err != nil {
		t.Fatal("failed read-only turn could not return Local", err)
	}
}

func TestAssignmentAndLocalClaimAreSerialized(t *testing.T) {
	g, w, _, owner, input := executionFixture(t, "planning")
	var wg sync.WaitGroup
	wg.Add(2)
	var assignmentErr, claimErr error
	var claimed *Request
	_, err := g.store.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Race"})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer wg.Done()
		_, assignmentErr = g.store.AssignExecution(context.Background(), owner, w.CampID, w.SessionID, uuid.NewString(), input)
	}()
	go func() {
		defer wg.Done()
		claimed, claimErr = g.store.ClaimRequest(context.Background(), owner, w.CampID, uuid.NewString())
	}()
	wg.Wait()
	if !errors.Is(assignmentErr, ErrTransition) || claimErr != nil || claimed == nil || claimed.ExecutionLocation != "local" {
		t.Fatal("assignment crossed queued local work", assignmentErr, claimErr)
	}
}

func TestUnavailableAssignmentStaysVisibleAndCanReturnLocal(t *testing.T) {
	g, w, _, owner, input := executionFixture(t, "planning")
	key := uuid.NewString()
	if _, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, key, input); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CancelWorkspace(t.Context(), owner, w.CampID, w.ID, uuid.NewString(), WorkspaceCancel{ExpectedRevision: w.Revision}); err != nil {
		t.Fatal(err)
	}
	target, err := g.store.Execution(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil || target.Location != "cloud" || target.Available {
		t.Fatal("unavailable target disappeared", target, err)
	}
	back := ExecutionInput{Location: "local", ExpectedEpoch: 2, ExpectedSessionRevision: input.ExpectedSessionRevision, ExpectedTranscriptRevision: input.ExpectedTranscriptRevision}
	if _, err = g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), back); err != nil {
		t.Fatal(err)
	}
	replay, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, key, input)
	if err != nil || replay.Location != "local" || replay.Epoch != 3 {
		t.Fatal("old assignment replay moved execution back", replay, err)
	}
}

func TestCloudSharingRevocationPreservesRuntimeAndStopsAuthority(t *testing.T) {
	g, w, grant, owner, input := executionFixture(t, "planning")
	source, err := g.store.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	r, err := g.store.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Cloud work"})
	if err != nil {
		t.Fatal(err)
	}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claim}); err != nil {
		t.Fatal(err)
	}
	unshare := SessionInput{ID: source.ID, ProjectID: source.ProjectID, ProfileID: source.ProfileID, LocalSessionID: source.LocalSessionID, Title: source.Title, Model: source.Model, State: source.State, Stage: source.Stage, Shared: false, AllowRequests: false, ExpectedRevision: source.Revision}
	changed, err := g.store.PublishSession(t.Context(), owner, w.CampID, uuid.NewString(), unshare)
	if err != nil || changed.Shared || changed.State != "running" || changed.Stage != "In progress" {
		t.Fatal("stale local state blocked revocation or replaced runtime", changed, err)
	}
	// Revocation reaches the runner as a cancellation of the running turn. The
	// owner's assigned workspace itself stays usable.
	beat, err := g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "heartbeat", LeaseToken: claim})
	if err == nil && (beat.State != "cancel_requested" || beat.Reason != "permission_changed") {
		t.Fatal("revoked authority stayed live", beat)
	}
	if err = g.ReapWorkspaces(t.Context()); err != nil {
		t.Fatal(err)
	}
	if kept, err := g.Workspace(t.Context(), owner, w.CampID, w.ID); err != nil || kept.State != "ready" {
		t.Fatal("sharing change failed the assigned workspace", kept.State, err)
	}
}

func TestDisplayEditsKeepAssignedCloudWorkspace(t *testing.T) {
	g, w, grant, owner, input := executionFixture(t, "planning")
	s := g.store
	if _, err := s.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Cloud work"})
	if err != nil {
		t.Fatal(err)
	}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claim}); err != nil {
		t.Fatal(err)
	}
	current, err := s.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// A board move and a sharing grant both change the session's display
	// revision; neither changes who may run in this workspace.
	moved, err := s.MoveSession(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), "Needs you", current.Revision)
	if err != nil || moved.Revision == w.SourceRevision {
		t.Fatal("board move did not change the display revision", moved.Revision, err)
	}
	if err = g.ReapWorkspaces(t.Context()); err != nil {
		t.Fatal(err)
	}
	beat, err := g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "heartbeat", LeaseToken: claim})
	if err != nil || beat.State != "started" {
		t.Fatal("board move fenced the running turn", beat.State, err)
	}
	finished, err := g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: claim, Outcome: "completed", Evidence: "Provider terminal receipt"})
	if err != nil || finished.State != "completed" {
		t.Fatal("board move blocked completion", finished.State, err)
	}
	if target, err := s.Execution(t.Context(), owner, w.CampID, w.SessionID); err != nil || !target.Available {
		t.Fatal("assigned workspace became unavailable", target, err)
	}
	next, err := s.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Next cloud turn"})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, uuid.NewString()); err != nil || claimed == nil || claimed.ID != next.ID {
		t.Fatal("next turn could not run after a board move", claimed, err)
	}
	if kept, err := g.Workspace(t.Context(), owner, w.CampID, w.ID); err != nil || kept.State != "ready" {
		t.Fatal("board move failed the assigned workspace", kept.State, err)
	}
}

func TestCloudNoopPublicationReplayCannotReshare(t *testing.T) {
	g, w, _, owner, input := executionFixture(t, "planning")
	if _, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	s, err := g.store.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	publication := SessionInput{ID: s.ID, ProjectID: s.ProjectID, ProfileID: s.ProfileID, LocalSessionID: s.LocalSessionID, Title: s.Title, Model: s.Model, State: s.State, Stage: s.Stage, Shared: s.Shared, AllowRequests: s.AllowRequests, ExpectedRevision: s.Revision}
	key := uuid.NewString()
	if _, err = g.store.PublishSession(t.Context(), owner, w.CampID, key, publication); err != nil {
		t.Fatal(err)
	}
	unshare := publication
	unshare.Shared = false
	if _, err = g.store.PublishSession(t.Context(), owner, w.CampID, uuid.NewString(), unshare); err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.PublishSession(t.Context(), owner, w.CampID, key, publication); err != nil {
		t.Fatal(err)
	}
	current, err := g.store.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil || current.Shared {
		t.Fatal("delayed no-op re-shared the session", err)
	}
}

func TestControllerDefersGrantRotationDuringCloudTurn(t *testing.T) {
	g, w, grant, owner, input := executionFixture(t, "planning")
	s := g.store
	if _, err := s.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE runner_grants SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, auth.HashToken(grant.Token)); err != nil {
		t.Fatal(err)
	}
	accounts, err := s.ProviderAccounts(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	var account ProviderAccount
	for _, a := range accounts {
		if a.ID == w.ProviderAccountID {
			account = a
		}
	}
	managed, err := s.EnsureControllerGrant(t.Context(), account, 4)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Keep grant while running"})
	if err != nil {
		t.Fatal(err)
	}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerRequest(t.Context(), managed.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), managed.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claim}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE runner_grants SET expires_at=now()+interval '10 minutes' WHERE id=$1`, managed.ID); err != nil {
		t.Fatal(err)
	}
	retained, err := s.EnsureControllerGrant(t.Context(), account, 4)
	if err != nil || retained.ID != managed.ID {
		t.Fatal("controller rotated a live execution grant", err)
	}
}
