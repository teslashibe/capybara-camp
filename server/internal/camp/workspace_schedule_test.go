package camp

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func anotherWorkspace(t *testing.T, g *GitStore, source Workspace) Workspace {
	t.Helper()
	owner := auth.Actor{UserID: source.OwnerUserID}
	session, err := g.store.Session(t.Context(), owner, source.CampID, source.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	owner.DeviceID = session.OwnerDeviceID
	second, err := g.store.PublishSession(t.Context(), owner, source.CampID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: source.ProjectID, ProfileID: session.ProfileID, LocalSessionID: uuid.NewString(), Title: "Another ticket", State: "idle", Stage: "Ready"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := g.CreateWorkspace(t.Context(), owner, source.CampID, second.ID, uuid.NewString(), WorkspaceInput{ProviderAccountID: source.ProviderAccountID, GitConnectionID: source.GitConnectionID, GitHubInstallationID: source.GitHubInstallationID, RepositoryID: source.RepositoryID, InstallationPage: 1, RepositoryPage: 1, Purpose: source.Purpose, ExpectedSessionRevision: second.Revision, ExpectedProjectRevision: source.ProjectRevision})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func readyWorkspace(t *testing.T, g *GitStore, grant RunnerGrant, w Workspace) Workspace {
	t.Helper()
	claim := uuid.NewString()
	if _, err := g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := g.RunnerWorkspaceToken(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	out, err := g.ReportRunnerWorkspace(t.Context(), grant.Token, w.ID, claim, WorkspaceReport{State: "ready", BaseCommit: w.BaseCommit, Branch: w.Branch})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWorkspaceScheduleRetainsReadyTicketsAndSerializesWork(t *testing.T) {
	g, first, grant, owner, assignment := executionFixture(t, "planning")
	second := anotherWorkspace(t, g, first)
	third := anotherWorkspace(t, g, first)
	claim := uuid.NewString()
	if _, err := g.ClaimRunnerWorkspace(t.Context(), grant.Token, second.ID, claim); err != nil {
		t.Fatal("ready ticket blocked another checkout", err)
	}
	if next, err := g.NextRunnerWorkspace(t.Context(), grant.Token); err != nil || next == nil || next.ID != second.ID {
		t.Fatal("preparation was not resumed", next, err)
	}
	if _, err := g.ClaimRunnerWorkspace(t.Context(), grant.Token, third.ID, uuid.NewString()); !errors.Is(err, ErrTransition) {
		t.Fatal("concurrent preparations", err)
	}
	if _, err := g.RunnerWorkspaceToken(t.Context(), grant.Token, second.ID, claim); err != nil {
		t.Fatal(err)
	}
	var err error
	second, err = g.ReportRunnerWorkspace(t.Context(), grant.Token, second.ID, claim, WorkspaceReport{State: "ready", BaseCommit: second.BaseCommit, Branch: second.Branch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.AssignExecution(t.Context(), owner, first.CampID, first.SessionID, uuid.NewString(), assignment); err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.AssignExecution(t.Context(), owner, second.CampID, second.SessionID, uuid.NewString(), ExecutionInput{Location: "cloud", WorkspaceID: second.ID, ExpectedEpoch: 1, ExpectedSessionRevision: second.SourceRevision, ExpectedWorkspaceRevision: second.Revision}); err != nil {
		t.Fatal(err)
	}
	requests := []Request{}
	for _, w := range []Workspace{second, first} {
		r, err := g.store.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Run this ticket"})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, r)
	}
	// The earlier preparation wins before later requests; after it is ready,
	// requests are selected by age across all the account's ready tickets.
	if next, err := g.NextRunnerWorkspace(t.Context(), grant.Token); err != nil || next == nil || next.ID != third.ID {
		t.Fatal("older checkout preparation was starved", next, err)
	}
	third = readyWorkspace(t, g, grant, third)
	if next, err := g.NextRunnerWorkspace(t.Context(), grant.Token); err != nil || next == nil || next.ID != second.ID {
		t.Fatal("oldest queued request was starved by an idle ticket", next, err)
	}
	var mu sync.Mutex
	claimed := []Request{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := []Workspace{first, second}[i%2]
			r, err := g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, uuid.NewString())
			if err != nil {
				t.Error(err)
				return
			}
			if r != nil {
				mu.Lock()
				claimed = append(claimed, *r)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(claimed) != 1 {
		t.Fatal("account ran multiple tickets concurrently", len(claimed))
	}
	r := claimed[0]
	if _, err = g.store.BeginProviderLogin(t.Context(), owner, first.ProviderAccountID, uuid.NewString()); !errors.Is(err, ErrTransition) {
		t.Fatal("login overlapped a claimed turn", err)
	}
	extra := anotherWorkspace(t, g, first)
	if _, err = g.ClaimRunnerWorkspace(t.Context(), grant.Token, extra.ID, uuid.NewString()); !errors.Is(err, ErrTransition) {
		t.Fatal("preparation overlapped a claimed turn", err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, r.ExecutionWorkspaceID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.pool.Exec(t.Context(), `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	other := first
	if r.SessionID == first.SessionID {
		other = second
	}
	if next, err := g.ClaimRunnerRequest(t.Context(), grant.Token, other.ID, uuid.NewString()); err != nil || next != nil {
		t.Fatal("unknown provider outcome did not fence account", next, err)
	}
	unknown, err := g.store.Request(t.Context(), owner, r.CampID, r.ID)
	if err != nil || unknown.State != "outcome_unknown" {
		t.Fatal("expired started turn was replayable", unknown, err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, r.ExecutionWorkspaceID, r.ID, uuid.NewString(), RequestAction{Action: "reconcile", LeaseToken: r.LeaseToken, Outcome: "failed", Evidence: "Fixture confirms provider stopped"}); err != nil {
		t.Fatal(err)
	}
	if next, err := g.ClaimRunnerRequest(t.Context(), grant.Token, other.ID, uuid.NewString()); err != nil || next == nil {
		t.Fatal("reconciled account stayed blocked", next, err)
	}
}

func TestWorkspaceRetentionIsBoundedAndInstallationScoped(t *testing.T) {
	g, first, grant, owner, _ := executionFixture(t, "planning")
	second := anotherWorkspace(t, g, first)
	unknown := uuid.NewString()
	ids := []string{first.ID, second.ID, unknown}
	retired, err := g.RetiredRunnerWorkspaces(t.Context(), grant.Token, ids)
	if err != nil || !slices.Equal(retired, []string{unknown}) {
		t.Fatal("active workspace retired", retired, err)
	}
	if _, err = g.CancelWorkspace(t.Context(), owner, second.CampID, second.ID, uuid.NewString(), WorkspaceCancel{ExpectedRevision: second.Revision}); err != nil {
		t.Fatal(err)
	}
	retired, err = g.RetiredRunnerWorkspaces(t.Context(), grant.Token, ids)
	if err != nil || !slices.Equal(retired, []string{second.ID, unknown}) {
		t.Fatal("cancelled workspace retained", retired, err)
	}
	_, foreign := addCloud(t, g.store, owner, "Codex")
	retired, err = g.RetiredRunnerWorkspaces(t.Context(), foreign.Token, []string{first.ID})
	if err != nil || !slices.Equal(retired, []string{first.ID}) {
		t.Fatal("foreign installation leaked active workspace", retired, err)
	}
	for _, bad := range [][]string{nil, {first.ID, first.ID}, {"bad"}, make([]string, 65)} {
		if _, err = g.RetiredRunnerWorkspaces(t.Context(), grant.Token, bad); !errors.Is(err, ErrInput) {
			t.Fatal("unbounded or malformed retention check", err)
		}
	}
	if _, err = g.RetiredRunnerWorkspaces(t.Context(), "campr_invalid", ids); err == nil {
		t.Fatal("unauthenticated retention check")
	}
}
