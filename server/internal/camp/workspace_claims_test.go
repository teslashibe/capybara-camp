package camp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
)

func actorForWorkspace(id string) auth.Actor { return auth.Actor{UserID: id} }

func workspaceClaimFixture(t *testing.T) (*GitStore, *workspaceGit, Workspace, RunnerGrant, string) {
	t.Helper()
	g, f, actors, c, session, input := workspaceFixture(t)
	w, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := g.store.CreateRunnerGrant(t.Context(), actors[0], input.ProviderAccountID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return g, f, w, grant, uuid.NewString()
}

func TestWorkspaceClaimTokenReplayAndCompletion(t *testing.T) {
	g, f, w, grant, claim := workspaceClaimFixture(t)
	next, err := g.NextRunnerWorkspace(t.Context(), grant.Token)
	if err != nil || next == nil || next.ID != w.ID {
		t.Fatal(next, err)
	}
	c, err := g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, claim)
	if err != nil || c.Workspace.State != "preparing" {
		t.Fatal(c, err)
	}
	again, err := g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, claim)
	if err != nil || again != c {
		t.Fatal("claim response loss", err)
	}
	if _, err = g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, uuid.NewString()); !errors.Is(err, ErrTransition) {
		t.Fatal("second claim replaced first", err)
	}
	heartbeat, err := g.HeartbeatRunnerWorkspace(t.Context(), grant.Token, w.ID, claim)
	if err != nil || heartbeat.Deadline != c.Deadline || heartbeat.LeaseUntil.Before(c.LeaseUntil) {
		t.Fatal("heartbeat changed deadline", err)
	}
	var results [4]RunnerWorkspaceToken
	var errs [4]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = g.RunnerWorkspaceToken(t.Context(), grant.Token, w.ID, claim)
		}(i)
	}
	wg.Wait()
	var credential RunnerWorkspaceToken
	for i, err := range errs {
		if err == nil {
			credential = results[i]
		} else if !errors.Is(err, ErrTransition) {
			t.Fatal(err)
		}
	}
	if credential.Token == "" {
		t.Fatal("no credential returned")
	}
	retry, err := g.RunnerWorkspaceToken(t.Context(), grant.Token, w.ID, claim)
	if err != nil || retry != credential {
		t.Fatal("credential redelivery changed", err)
	}
	f.mu.Lock()
	calls := f.scopeCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatal("credential minted repeatedly", calls)
	}
	var encrypted []byte
	if err = g.store.pool.QueryRow(t.Context(), `SELECT encrypted_read_token FROM ticket_workspaces WHERE id=$1`, w.ID).Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte(credential.Token)) {
		t.Fatal("plaintext credential", err)
	}
	public, _ := json.Marshal(c)
	if bytes.Contains(public, []byte(credential.Token)) || bytes.Contains(public, []byte("encrypted_read")) {
		t.Fatal("credential exposed in claim")
	}
	if _, err = g.RunnerWorkspaceToken(t.Context(), grant.Token, w.ID, uuid.NewString()); !errors.Is(err, ErrTransition) {
		t.Fatal("wrong claim read token", err)
	}
	report := WorkspaceReport{State: "ready", BaseCommit: w.BaseCommit, Branch: w.Branch}
	done, err := g.ReportRunnerWorkspace(t.Context(), grant.Token, w.ID, claim, report)
	if err != nil || done.State != "ready" {
		t.Fatal(done, err)
	}
	dup, err := g.ReportRunnerWorkspace(t.Context(), grant.Token, w.ID, claim, report)
	if err != nil || dup != done {
		t.Fatal("completion redelivery changed", err)
	}
	if _, err = g.RunnerWorkspaceToken(t.Context(), grant.Token, w.ID, claim); !errors.Is(err, ErrTransition) {
		t.Fatal("completed workspace still released token", err)
	}
	if err = g.store.pool.QueryRow(t.Context(), `SELECT encrypted_read_token FROM ticket_workspaces WHERE id=$1`, w.ID).Scan(&encrypted); err != nil || encrypted != nil {
		t.Fatal("credential retained after completion", err)
	}
	if err = g.Reap(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	revoked := f.revokes
	f.mu.Unlock()
	if revoked != 1 {
		t.Fatal("completion did not retire token", revoked)
	}
}

func TestWorkspaceClaimsStayInstallationScopedAndExclusive(t *testing.T) {
	g, _, w, grant, claim := workspaceClaimFixture(t)
	var owner string
	if err := g.store.pool.QueryRow(t.Context(), `SELECT owner_user_id FROM provider_accounts WHERE id=$1`, w.ProviderAccountID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	_, other := addCloud(t, g.store, actorForWorkspace(owner), "Codex")
	if _, err := g.ClaimRunnerWorkspace(t.Context(), other.Token, w.ID, claim); !errors.Is(err, ErrDenied) {
		t.Fatal("another installation claimed workspace", err)
	}
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, uuid.NewString())
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if !errors.Is(err, ErrTransition) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatal("non-exclusive claim", wins)
	}
}

func TestWorkspaceInstallationSerializesDifferentTickets(t *testing.T) {
	g, _, w, grant, claim := workspaceClaimFixture(t)
	owner := actorForWorkspace(w.OwnerUserID)
	source, err := g.store.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	owner.DeviceID = source.OwnerDeviceID
	second, err := g.store.PublishSession(t.Context(), owner, w.CampID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: w.ProjectID, ProfileID: source.ProfileID, LocalSessionID: uuid.NewString(), Title: "Second workspace", State: "idle", Stage: "Ready"})
	if err != nil {
		t.Fatal(err)
	}
	input := WorkspaceInput{ProviderAccountID: w.ProviderAccountID, GitConnectionID: w.GitConnectionID, GitHubInstallationID: w.GitHubInstallationID, RepositoryID: w.RepositoryID, InstallationPage: 1, RepositoryPage: 1, Purpose: w.Purpose, ExpectedSessionRevision: second.Revision, ExpectedProjectRevision: w.ProjectRevision}
	queued, err := g.CreateWorkspace(t.Context(), owner, w.CampID, second.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = g.ClaimRunnerWorkspace(t.Context(), grant.Token, queued.ID, uuid.NewString()); !errors.Is(err, ErrTransition) {
		t.Fatal("installation prepared two tickets", err)
	}
	if _, err = g.CancelWorkspace(t.Context(), owner, w.CampID, w.ID, uuid.NewString(), WorkspaceCancel{ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err = g.ClaimRunnerWorkspace(t.Context(), grant.Token, queued.ID, uuid.NewString()); err != nil {
		t.Fatal("released slot was not reusable", err)
	}
}

func TestWorkspaceUnknownTokenIssueIsNotRepeated(t *testing.T) {
	g, f, w, grant, claim := workspaceClaimFixture(t)
	if _, err := g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	f.scopeError = gitprovider.ErrUnavailable
	if _, err := g.RunnerWorkspaceToken(t.Context(), grant.Token, w.ID, claim); !errors.Is(err, gitprovider.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := g.RunnerWorkspaceToken(t.Context(), grant.Token, w.ID, claim); !errors.Is(err, ErrTransition) {
		t.Fatal("uncertain token issuance retried", err)
	}
	f.mu.Lock()
	calls := f.scopeCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatal("uncertain issuance repeated")
	}
	current, err := g.Workspace(t.Context(), actorForWorkspace(w.OwnerUserID), w.CampID, w.ID)
	if err != nil || current.State != "failed" || current.FailureCode != "git_grant_failed" {
		t.Fatal(current, err)
	}
}

func TestWorkspaceRevocationDuringTokenIssueRetiresCandidate(t *testing.T) {
	g, f, w, grant, claim := workspaceClaimFixture(t)
	if _, err := g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	f.scopeStarted = make(chan struct{}, 1)
	f.scopeProceed = make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := g.RunnerWorkspaceToken(ctx, grant.Token, w.ID, claim); done <- err }()
	select {
	case <-f.scopeStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := g.CancelWorkspace(ctx, actorForWorkspace(w.OwnerUserID), w.CampID, w.ID, uuid.NewString(), WorkspaceCancel{ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	close(f.scopeProceed)
	if err := <-done; !errors.Is(err, ErrTransition) {
		t.Fatal("cancelled claim received credentials", err)
	}
	if err := g.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	revoked := f.revokes
	f.mu.Unlock()
	if revoked != 1 {
		t.Fatal("cancelled candidate retained", revoked)
	}
}

func TestWorkspaceExpiryFencesHeartbeatAndToken(t *testing.T) {
	g, f, w, grant, claim := workspaceClaimFixture(t)
	if _, err := g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := g.RunnerWorkspaceToken(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := g.store.pool.Exec(t.Context(), `UPDATE ticket_workspaces SET lease_until=now()-interval '1 second' WHERE id=$1`, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := g.HeartbeatRunnerWorkspace(t.Context(), grant.Token, w.ID, claim); !errors.Is(err, ErrTransition) {
		t.Fatal("revived expired lease", err)
	}
	if err := g.Reap(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := g.Workspace(t.Context(), actorForWorkspace(w.OwnerUserID), w.CampID, w.ID)
	if err != nil || current.State != "failed" || current.FailureCode != "lease_expired" {
		t.Fatal(current, err)
	}
	f.mu.Lock()
	revoked := f.revokes
	f.mu.Unlock()
	if revoked != 1 {
		t.Fatal("expired token not revoked")
	}
	if _, err = g.RunnerWorkspaceToken(t.Context(), grant.Token, w.ID, claim); !errors.Is(err, ErrTransition) {
		t.Fatal("expired token redelivered", err)
	}
}

func TestWorkspaceNewTokenCannotReplaceClaim(t *testing.T) {
	g, _, w, grant, claim := workspaceClaimFixture(t)
	if _, err := g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	replacement, err := g.store.CreateRunnerGrant(t.Context(), actorForWorkspace(w.OwnerUserID), w.ProviderAccountID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.ClaimRunnerWorkspace(t.Context(), replacement.Token, w.ID, claim); !errors.Is(err, ErrTransition) {
		t.Fatal("new grant adopted old claim", err)
	}
	if _, err = g.HeartbeatRunnerWorkspace(t.Context(), grant.Token, w.ID, claim); err == nil {
		t.Fatal("revoked grant renewed claim")
	}
	if err = g.ReapWorkspaces(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := g.Workspace(t.Context(), actorForWorkspace(w.OwnerUserID), w.CampID, w.ID)
	if err != nil || current.FailureCode != "authority_changed" {
		t.Fatal(current, err)
	}
}
