package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
)

type checkoutGit struct {
	sha    string
	scopes atomic.Int32
}

func (f *checkoutGit) AuthorizeURL(state, verifier string) string {
	return "https://github.com/login/oauth/authorize?state=" + state
}
func (f *checkoutGit) InstallURL() string { return "https://github.com/apps/fixture/installations/new" }
func (f *checkoutGit) Exchange(context.Context, string, string) (gitprovider.Credentials, error) {
	return gitprovider.Credentials{AccessToken: "ghu_" + strings.Repeat("a", 36), RefreshToken: "ghr_" + strings.Repeat("a", 36), ExpiresAt: time.Now().Add(time.Hour), RefreshExpiresAt: time.Now().Add(24 * time.Hour)}, nil
}
func (f *checkoutGit) Refresh(ctx context.Context, c gitprovider.Credentials) (gitprovider.Credentials, error) {
	return f.Exchange(ctx, "", "")
}
func (f *checkoutGit) User(context.Context, string) (gitprovider.User, error) {
	return gitprovider.User{ID: 123, Login: "fixture-user"}, nil
}
func (f *checkoutGit) Revoke(context.Context, gitprovider.Credentials) error { return nil }
func (f *checkoutGit) Installations(context.Context, string, int) (gitprovider.Installations, error) {
	return gitprovider.Installations{Values: []gitprovider.Installation{{ID: 7, Contents: "write"}}}, nil
}
func (f *checkoutGit) Repositories(context.Context, string, gitprovider.Installation, int) (gitprovider.Repositories, error) {
	return gitprovider.Repositories{Values: []gitprovider.Repository{{ID: 5, InstallationID: 7, FullName: "fixture/repo", DefaultBranch: "main", Read: true, Write: true}}}, nil
}
func (f *checkoutGit) ResolveCommit(context.Context, string, gitprovider.Repository, string) (string, error) {
	return f.sha, nil
}
func (f *checkoutGit) ScopeRead(_ context.Context, _ string, _ time.Time, user int64, _ gitprovider.Installation, repo gitprovider.Repository) (gitprovider.ReadGrant, error) {
	f.scopes.Add(1)
	return gitprovider.ReadGrant{Token: "ghu_" + strings.Repeat("n", 36), ExpiresAt: time.Now().Add(time.Minute), UserID: user, RepositoryID: repo.ID}, nil
}

func workerWorkspaceFixture(t *testing.T, remote, sha string, wrap func(http.Handler) http.Handler) (fixture, *camp.GitStore, *checkoutGit, camp.Workspace) {
	t.Helper()
	f := workerFixture(t, "Codex", wrap)
	claim := uuid.NewString()
	if _, err := f.store.ClaimRunnerLogin(t.Context(), f.grant.Token, f.login.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReportRunnerLogin(t.Context(), f.grant.Token, f.login.ID, claim, camp.RunnerLoginReport{State: "connected", VerifiedSubject: "fixture-owner", Credentials: json.RawMessage(`{"fixture":"private-proof"}`)}); err != nil {
		t.Fatal(err)
	}
	f.owner.DeviceID = uuid.NewString()
	if _, err := f.api.Pool.Exec(t.Context(), `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Fixture',$3,now()+interval '1 day')`, f.owner.DeviceID, f.owner.UserID, auth.HashToken(f.owner.DeviceID)); err != nil {
		t.Fatal(err)
	}
	c, err := f.store.Create(t.Context(), f.owner, uuid.NewString(), "Fixture Camp")
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.store.CreateProjectWithRepositories(t.Context(), f.owner, c.ID, uuid.NewString(), "Fixture Project", []string{"https://github.com/fixture/repo"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := f.store.RegisterProfile(t.Context(), f.owner, uuid.NewString(), camp.ProfileInput{LocalID: strings.Repeat("a", 64), Provider: "Codex", Label: "Fixture"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.store.PublishSession(t.Context(), f.owner, c.ID, uuid.NewString(), camp.SessionInput{ID: uuid.NewString(), ProjectID: p.ID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: "Checkout", State: "idle", Stage: "Ready"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &checkoutGit{sha: sha}
	g, err := camp.NewGitStore(f.store, provider, bytes.Repeat([]byte{17}, 32))
	if err != nil {
		t.Fatal(err)
	}
	login, err := g.Begin(t.Context(), f.owner, uuid.NewString(), "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(login.URL)
	exchange, err := g.Claim(t.Context(), u.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Exchange(t.Context(), exchange, "fixture"); err != nil {
		t.Fatal(err)
	}
	f.api.Git = g
	workspace, err := g.CreateWorkspace(t.Context(), f.owner, c.ID, session.ID, uuid.NewString(), camp.WorkspaceInput{ProviderAccountID: f.account.ID, GitConnectionID: login.ConnectionID, GitHubInstallationID: 7, RepositoryID: 5, InstallationPage: 1, RepositoryPage: 1, Purpose: "planning", ExpectedSessionRevision: session.Revision, ExpectedProjectRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	co, err := openCheckout(privateTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	co.remote = func(camp.Workspace) string { return remote }
	co.gitConfig = []string{"-c", "protocol.http.allow=always"}
	f.worker.checkout = co
	return f, g, provider, workspace
}

func TestWorkerCheckoutLostAcknowledgmentsAndCancellation(t *testing.T) {
	remote, sha, fetches := gitFixtureServer(t, nil, nil)
	var claims, tokens, reports atomic.Int32
	f, g, provider, workspace := workerWorkspaceFixture(t, remote, sha, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var counter *atomic.Int32
			if strings.Contains(r.URL.Path, "/workspaces/") {
				switch {
				case strings.HasSuffix(r.URL.Path, "/claim"):
					counter = &claims
				case strings.HasSuffix(r.URL.Path, "/token"):
					counter = &tokens
				case strings.HasSuffix(r.URL.Path, "/report"):
					counter = &reports
				}
			}
			if counter != nil && counter.Add(1) == 1 {
				next.ServeHTTP(discardResponse{}, r)
				w.WriteHeader(503)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	cancel, done := startWorker(t, f.worker)
	eventually(t, func() bool {
		w, err := g.Workspace(t.Context(), f.owner, workspace.CampID, workspace.ID)
		return err == nil && w.State == "ready"
	})
	eventually(t, func() bool { r, err := f.worker.journal.loadWorkspace(); return err == nil && r == nil })
	if provider.scopes.Load() != 1 || claims.Load() != 2 || tokens.Load() != 2 || reports.Load() != 2 || fetches.Load() != 1 {
		t.Fatal("work repeated after lost reply", provider.scopes.Load(), claims.Load(), tokens.Load(), reports.Load(), fetches.Load())
	}
	w, err := g.Workspace(t.Context(), f.owner, workspace.CampID, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.CancelWorkspace(t.Context(), f.owner, w.CampID, w.ID, uuid.NewString(), camp.WorkspaceCancel{ExpectedRevision: w.Revision}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		_, err := os.Stat(f.worker.checkout.workdir(workspace))
		return errors.Is(err, os.ErrNotExist)
	})
	stopWorker(t, cancel, done)
}

type discardResponse struct{}

func (discardResponse) Header() http.Header         { return http.Header{} }
func (discardResponse) WriteHeader(int)             {}
func (discardResponse) Write(p []byte) (int, error) { return len(p), nil }

func TestWorkerCheckoutRestartUsesSameCredential(t *testing.T) {
	started := make(chan struct{}, 1)
	var blocked atomic.Bool
	remote, sha, _ := gitFixtureServer(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		if blocked.CompareAndSwap(false, true) {
			started <- struct{}{}
			<-r.Context().Done()
			return true
		}
		return false
	})
	f, g, provider, workspace := workerWorkspaceFixture(t, remote, sha, nil)
	cancel, done := startWorker(t, f.worker)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not start")
	}
	stopWorker(t, cancel, done)
	r, err := f.worker.journal.loadWorkspace()
	if err != nil || r == nil || r.Phase != "preparing" {
		t.Fatal("restart fence lost", err)
	}
	oldClaim := r.ClaimID
	cancel, done = startWorker(t, f.worker)
	eventually(t, func() bool {
		w, err := g.Workspace(t.Context(), f.owner, workspace.CampID, workspace.ID)
		return err == nil && w.State == "ready"
	})
	stopWorker(t, cancel, done)
	if provider.scopes.Load() != 1 {
		t.Fatal("restart minted another credential")
	}
	if _, err = g.RunnerWorkspaceStatus(t.Context(), f.grant.Token, workspace.ID, oldClaim); err != nil {
		t.Fatal("restart changed claim", err)
	}
}

func TestWorkerCheckoutCancellationStopsGitBeforeCleanup(t *testing.T) {
	started := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	remote, sha, _ := gitFixtureServer(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		select {
		case stopped <- struct{}{}:
		default:
		}
		return true
	})
	f, g, _, workspace := workerWorkspaceFixture(t, remote, sha, nil)
	cancel, done := startWorker(t, f.worker)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not start")
	}
	w, err := g.Workspace(t.Context(), f.owner, workspace.CampID, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.CancelWorkspace(t.Context(), f.owner, w.CampID, w.ID, uuid.NewString(), camp.WorkspaceCancel{ExpectedRevision: w.Revision}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not stop fetch")
	}
	eventually(t, func() bool { r, err := f.worker.journal.loadWorkspace(); return err == nil && r == nil })
	if _, err = os.Stat(f.worker.checkout.workdir(workspace)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancel retained checkout")
	}
	stopWorker(t, cancel, done)
}
