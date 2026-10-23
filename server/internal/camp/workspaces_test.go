package camp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
)

type workspaceGit struct {
	fixtureGit
	repository     gitprovider.Repository
	commit         string
	resolutions    int
	resolvedRefs   []string
	resolveStarted chan struct{}
	resolveProceed chan struct{}
	scopeCalls     int
	scopeError     error
	scopeStarted   chan struct{}
	scopeProceed   chan struct{}
}

func (f *workspaceGit) ScopeRead(ctx context.Context, root string, expiry time.Time, user int64, installation gitprovider.Installation, repo gitprovider.Repository) (gitprovider.ReadGrant, error) {
	f.mu.Lock()
	f.scopeCalls++
	failure, started, proceed := f.scopeError, f.scopeStarted, f.scopeProceed
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if proceed != nil {
		select {
		case <-proceed:
		case <-ctx.Done():
			return gitprovider.ReadGrant{}, ctx.Err()
		}
	}
	if failure != nil {
		return gitprovider.ReadGrant{}, failure
	}
	return gitprovider.ReadGrant{Token: "ghu_" + strings.Repeat("n", 36), ExpiresAt: time.Now().Add(5 * time.Minute), RepositoryID: repo.ID, UserID: user}, nil
}

func (f *workspaceGit) Installations(_ context.Context, _ string, page int) (gitprovider.Installations, error) {
	if page != 1 {
		return gitprovider.Installations{}, nil
	}
	return gitprovider.Installations{Values: []gitprovider.Installation{{ID: 7, Account: gitprovider.User{ID: 123}, Contents: "write"}}}, nil
}
func (f *workspaceGit) Repositories(_ context.Context, _ string, _ gitprovider.Installation, page int) (gitprovider.Repositories, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if page != 1 {
		return gitprovider.Repositories{}, nil
	}
	return gitprovider.Repositories{Values: []gitprovider.Repository{f.repository}}, nil
}
func (f *workspaceGit) ResolveCommit(ctx context.Context, _ string, _ gitprovider.Repository, ref string) (string, error) {
	f.mu.Lock()
	f.resolutions++
	f.resolvedRefs = append(f.resolvedRefs, ref)
	commit, started, proceed := f.commit, f.resolveStarted, f.resolveProceed
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if proceed != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-proceed:
		}
	}
	return commit, nil
}

func workspaceFixture(t *testing.T) (*GitStore, *workspaceGit, []auth.Actor, Camp, Session, WorkspaceInput) {
	t.Helper()
	s, pool, actors, c, project, profile := sessionFixture(t)
	cloud, err := NewWithProviderKey(pool, "fixture-receipt-key", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s.providerCipher = cloud.providerCipher
	f := &workspaceGit{fixtureGit: fixtureGit{user: 123}, repository: gitprovider.Repository{ID: 5, InstallationID: 7, FullName: "fixture/repo", DefaultBranch: "main", Read: true, Write: true}, commit: strings.Repeat("a", 40)}
	g, err := NewGitStore(s, f, bytes.Repeat([]byte{17}, 32))
	if err != nil {
		t.Fatal(err)
	}
	connection := connectGit(t, g, actors[0])
	account, grant := addCloud(t, s, actors[0], "Codex")
	login, claim := startCloudLogin(t, s, actors[0], account, grant)
	if _, err = s.ReportRunnerLogin(t.Context(), grant.Token, login.ID, claim, loginProof("workspace-owner")); err != nil {
		t.Fatal(err)
	}
	project, err = s.UpdateProject(t.Context(), actors[0], c.ID, project.ID, uuid.NewString(), ProjectUpdate{Name: project.Name, Repositories: []string{"https://github.com/fixture/repo"}, ExpectedRevision: project.Revision})
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.PublishSession(t.Context(), actors[0], c.ID, uuid.NewString(), sessionInput(project, profile))
	if err != nil {
		t.Fatal(err)
	}
	input := WorkspaceInput{ProviderAccountID: account.ID, GitConnectionID: connection.ID, GitHubInstallationID: 7, RepositoryID: 5, InstallationPage: 1, RepositoryPage: 1, Purpose: "planning", ExpectedSessionRevision: session.Revision, ExpectedProjectRevision: project.Revision}
	return g, f, actors, c, session, input
}

func TestWorkspacePinsIdentityCommitAndOneBranchAcrossRetries(t *testing.T) {
	g, f, actors, c, session, input := workspaceFixture(t)
	key := uuid.NewString()
	w, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, key, input)
	if err != nil {
		t.Fatal(err)
	}
	if w.BaseCommit != strings.Repeat("a", 40) || w.RequestedRef != "main" || w.GitHubUserID != 123 || w.GitConnectionID != input.GitConnectionID || w.ProviderAccountID != input.ProviderAccountID || w.State != "queued" || w.Purpose != "planning" || !strings.HasPrefix(w.Branch, "camp/"+session.ID+"/") {
		t.Fatal("incorrect frozen workspace", w)
	}
	f.mu.Lock()
	f.commit = strings.Repeat("b", 40)
	f.mu.Unlock()
	retry, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, key, input)
	if err != nil || retry != w {
		t.Fatal("lost response changed pin or branch", err)
	}
	f.mu.Lock()
	resolutions := f.resolutions
	f.mu.Unlock()
	if resolutions != 1 {
		t.Fatal("committed retry resolved a moving ref again")
	}
	changed := input
	changed.Ref = "another"
	if _, err = g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, key, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("request accepted different payload", err)
	}
	if _, err = g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), input); !errors.Is(err, ErrTransition) {
		t.Fatal("two pending workspaces for one ticket", err)
	}
	var count int
	if err = g.store.pool.QueryRow(t.Context(), `SELECT count(*) FROM ticket_workspaces`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	for _, other := range actors[1:] {
		if _, err = g.Workspace(t.Context(), other, c.ID, w.ID); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign owner read workspace", err)
		}
		if _, err = g.CreateWorkspace(t.Context(), other, c.ID, session.ID, uuid.NewString(), input); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign owner prepared workspace", err)
		}
	}
	ownerEvents, err := g.store.Events(t.Context(), actors[0], c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	workspaceEvents := 0
	for _, e := range ownerEvents {
		if e.Kind == "workspace.updated" {
			workspaceEvents++
			raw := string(e.Payload)
			if strings.Contains(raw, "git_connection") || strings.Contains(raw, "ghu_") {
				t.Fatal("private data in workspace event")
			}
		}
	}
	if workspaceEvents != 1 {
		t.Fatal("missing or duplicated workspace event", workspaceEvents)
	}
	otherEvents, err := g.store.Events(t.Context(), actors[1], c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range otherEvents {
		if e.Kind == "workspace.updated" {
			t.Fatal("private workspace broadcast to teammate")
		}
	}
	current, err := g.store.Session(t.Context(), actors[0], c.ID, session.ID)
	if err != nil || current != session {
		t.Fatal("preparation changed execution ownership", err)
	}
	raw, _ := json.Marshal(w)
	if bytes.Contains(raw, []byte("ghu_")) || bytes.Contains(raw, []byte("fixture_token")) {
		t.Fatal("credential in workspace")
	}
}

func TestWorkspaceConcurrentCreateAndCancellation(t *testing.T) {
	g, _, actors, c, session, input := workspaceFixture(t)
	key := uuid.NewString()
	results := make(chan Workspace, 6)
	errorsCh := make(chan error, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, key, input)
			results <- w
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first Workspace
	for w := range results {
		if first.ID == "" {
			first = w
		}
		if w != first {
			t.Fatal("concurrent retry returned different workspaces")
		}
	}
	if _, err := g.CancelWorkspace(t.Context(), actors[1], c.ID, first.ID, uuid.NewString(), WorkspaceCancel{ExpectedRevision: 1}); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign cancellation", err)
	}
	if _, err := g.CancelWorkspace(t.Context(), actors[0], c.ID, first.ID, uuid.NewString(), WorkspaceCancel{ExpectedRevision: 9}); !errors.Is(err, ErrRevision) {
		t.Fatal("stale cancellation", err)
	}
	cancelKey := uuid.NewString()
	cancelled, err := g.CancelWorkspace(t.Context(), actors[0], c.ID, first.ID, cancelKey, WorkspaceCancel{ExpectedRevision: 1})
	if err != nil || cancelled.State != "cancelled" || cancelled.Revision != 2 {
		t.Fatal(cancelled, err)
	}
	again, err := g.CancelWorkspace(t.Context(), actors[0], c.ID, first.ID, cancelKey, WorkspaceCancel{ExpectedRevision: 1})
	if err != nil || again != cancelled {
		t.Fatal("cancel response loss", err)
	}
	replay, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, key, input)
	if err != nil || replay != cancelled {
		t.Fatal("prepare replay restarted a cancelled workspace", err)
	}
	w, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), input)
	if err != nil || w.Branch == first.Branch || w.ID == first.ID {
		t.Fatal("new attempt reused old checkout", err)
	}
}

func TestWorkspaceRepositoryPermissionsAndFrozenRevisions(t *testing.T) {
	g, f, actors, c, session, input := workspaceFixture(t)
	otherAccount, _ := addCloud(t, g.store, actors[1], "Codex")
	otherGit := connectGit(t, g, actors[1])
	for _, change := range []func(*WorkspaceInput){func(v *WorkspaceInput) { v.ProviderAccountID = otherAccount.ID }, func(v *WorkspaceInput) { v.GitConnectionID = otherGit.ID }} {
		bad := input
		change(&bad)
		if _, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), bad); !errors.Is(err, ErrDenied) {
			t.Fatal("owner selected another member's credentials", err)
		}
	}
	for _, change := range []func(*WorkspaceInput){func(v *WorkspaceInput) { v.ExpectedProjectRevision++ }, func(v *WorkspaceInput) { v.ExpectedSessionRevision++ }} {
		bad := input
		change(&bad)
		if _, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), bad); !errors.Is(err, ErrRevision) {
			t.Fatal("stale source accepted", err)
		}
	}
	for _, change := range []func(*WorkspaceInput){func(v *WorkspaceInput) { v.RepositoryID = 99 }, func(v *WorkspaceInput) { v.GitHubInstallationID = 99 }, func(v *WorkspaceInput) { v.RepositoryPage = 2 }} {
		bad := input
		change(&bad)
		if _, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), bad); !errors.Is(err, gitprovider.ErrAccess) {
			t.Fatal("ungranted repository accepted", err)
		}
	}
	f.mu.Lock()
	f.repository.Write = false
	f.mu.Unlock()
	bad := input
	bad.Purpose = "implementation"
	if _, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), bad); !errors.Is(err, gitprovider.ErrAccess) {
		t.Fatal("read-only connection can implement", err)
	}
	f.mu.Lock()
	f.repository.FullName = "fixture/unlinked"
	f.mu.Unlock()
	if _, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), input); !errors.Is(err, gitprovider.ErrAccess) {
		t.Fatal("unlinked repository accepted", err)
	}
	f.mu.Lock()
	f.repository.FullName = "fixture/repo"
	f.commit = "invalid"
	f.mu.Unlock()
	if _, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), input); !errors.Is(err, gitprovider.ErrResponse) {
		t.Fatal("invalid commit accepted", err)
	}
	f.mu.Lock()
	f.commit = strings.Repeat("a", 40)
	f.mu.Unlock()
	if _, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), input); err != nil {
		t.Fatal("read-only planning blocked", err)
	}
}

func TestWorkspaceRevocationDuringGitReadCannotPublish(t *testing.T) {
	for _, kind := range []string{"git", "provider", "source", "membership"} {
		t.Run(kind, func(t *testing.T) {
			g, f, actors, c, session, input := workspaceFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			f.resolveStarted = make(chan struct{}, 1)
			f.resolveProceed = make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, err := g.CreateWorkspace(ctx, actors[0], c.ID, session.ID, uuid.NewString(), input)
				done <- err
			}()
			select {
			case <-f.resolveStarted:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var err error
			want := ErrDenied
			switch kind {
			case "git":
				_, err = g.Disconnect(ctx, actors[0], input.GitConnectionID, uuid.NewString())
				want = gitprovider.ErrAuthorization
			case "provider":
				_, err = g.store.DisconnectProviderAccount(ctx, actors[0], input.ProviderAccountID, uuid.NewString())
				want = ErrTransition
			case "source":
				_, err = g.store.pool.Exec(ctx, `UPDATE sessions SET revision=revision+1 WHERE id=$1`, session.ID)
				want = ErrRevision
			case "membership":
				_, err = g.store.pool.Exec(ctx, `DELETE FROM memberships WHERE camp_id=$1 AND user_id=$2`, c.ID, actors[0].UserID)
			}
			if err != nil {
				t.Fatal(err)
			}
			close(f.resolveProceed)
			if err = <-done; !errors.Is(err, want) {
				t.Fatal("changed authority published workspace", err)
			}
			var count int
			if err = g.store.pool.QueryRow(ctx, `SELECT count(*) FROM ticket_workspaces`).Scan(&count); err != nil || count != 0 {
				t.Fatal("workspace survived authority change", count, err)
			}
		})
	}
}

func TestWorkspaceListIsBoundToOwnerAndSession(t *testing.T) {
	g, _, actors, c, session, input := workspaceFixture(t)
	workspace, err := g.CreateWorkspace(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	values, err := g.SessionWorkspaces(t.Context(), actors[0], c.ID, session.ID)
	if err != nil || len(values) != 1 || values[0].ID != workspace.ID {
		t.Fatal(values, err)
	}
	for _, actor := range actors[1:] {
		if _, err = g.SessionWorkspaces(t.Context(), actor, c.ID, session.ID); err == nil {
			t.Fatal("workspace account identity exposed to another member")
		}
	}
}
