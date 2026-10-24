package camp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
)

type fixtureGit struct {
	mu                                          sync.Mutex
	user                                        int64
	exchanges, refreshes, revokes, repositories int
	started                                     chan struct{}
	proceed                                     chan struct{}
	refreshError                                error
	userError                                   error
}

func (f *fixtureGit) AuthorizeURL(state, verifier string) string {
	return "https://github.com/login/oauth/authorize?" + url.Values{"state": {state}, "code_challenge": {verifier}}.Encode()
}
func (f *fixtureGit) InstallURL() string { return "https://github.com/apps/fixture/installations/new" }
func fixtureGitCredentials(ch string) gitprovider.Credentials {
	return gitprovider.Credentials{AccessToken: "ghu_" + strings.Repeat(ch, 36), RefreshToken: "ghr_" + strings.Repeat(ch, 36), ExpiresAt: time.Now().Add(8 * time.Hour), RefreshExpiresAt: time.Now().Add(180 * 24 * time.Hour)}
}
func (f *fixtureGit) Exchange(ctx context.Context, code, verifier string) (gitprovider.Credentials, error) {
	f.mu.Lock()
	f.exchanges++
	started, proceed := f.started, f.proceed
	f.mu.Unlock()
	if started != nil {
		close(started)
		select {
		case <-ctx.Done():
			return gitprovider.Credentials{}, ctx.Err()
		case <-proceed:
		}
	}
	return fixtureGitCredentials("a"), nil
}
func (f *fixtureGit) Refresh(ctx context.Context, c gitprovider.Credentials) (gitprovider.Credentials, error) {
	f.mu.Lock()
	f.refreshes++
	started, proceed, err := f.started, f.proceed, f.refreshError
	f.mu.Unlock()
	if started != nil {
		close(started)
		select {
		case <-ctx.Done():
			return gitprovider.Credentials{}, ctx.Err()
		case <-proceed:
		}
	}
	return fixtureGitCredentials("b"), err
}
func (f *fixtureGit) User(ctx context.Context, token string) (gitprovider.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.userError != nil {
		return gitprovider.User{}, f.userError
	}
	return gitprovider.User{ID: f.user, Login: "fixture-user"}, nil
}
func (f *fixtureGit) Revoke(context.Context, gitprovider.Credentials) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokes++
	return nil
}
func (f *fixtureGit) Installations(context.Context, string, int) (gitprovider.Installations, error) {
	return gitprovider.Installations{Values: []gitprovider.Installation{{ID: 7, Contents: "write"}}}, nil
}
func (f *fixtureGit) Repositories(context.Context, string, gitprovider.Installation, int) (gitprovider.Repositories, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repositories++
	return gitprovider.Repositories{Values: []gitprovider.Repository{{ID: 5, FullName: "fixture/repo", Read: true, Write: true}}}, nil
}
func gitFixture(t *testing.T) (*GitStore, *fixtureGit, []auth.Actor) {
	s, actors := cloudFixture(t)
	f := &fixtureGit{user: 123}
	g, err := NewGitStore(s, f, bytes.Repeat([]byte{17}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return g, f, actors
}
func claimGit(t *testing.T, g *GitStore, login GitLogin) GitExchange {
	t.Helper()
	u, err := url.Parse(login.URL)
	if err != nil {
		t.Fatal(err)
	}
	x, err := g.Claim(t.Context(), u.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	return x
}
func connectGit(t *testing.T, g *GitStore, a auth.Actor) GitConnection {
	t.Helper()
	login, err := g.Begin(t.Context(), a, uuid.NewString(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Exchange(t.Context(), claimGit(t, g, login), "code"); err != nil {
		t.Fatal(err)
	}
	out, err := g.Status(t.Context(), a, login.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	return out.Connection
}

func TestGitLoginOwnerIsolationReplayAndEncryption(t *testing.T) {
	g, f, a := gitFixture(t)
	ctx := t.Context()
	key := uuid.NewString()
	login, err := g.Begin(ctx, a[0], key, "")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := g.Begin(ctx, a[0], key, "")
	if err != nil || replayed.URL != login.URL || replayed.ID != login.ID {
		t.Fatal("lost begin response not recoverable", err)
	}
	if _, err = g.Status(ctx, a[1], login.ConnectionID); !errors.Is(err, ErrDenied) {
		t.Fatal("other owner read connection", err)
	}
	if _, err = g.Cancel(ctx, a[1], login.ConnectionID, login.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
		t.Fatal("other owner cancelled login", err)
	}
	u, _ := url.Parse(login.URL)
	state := u.Query().Get("state")
	var encrypted []byte
	if err = g.store.pool.QueryRow(ctx, `SELECT encrypted_context FROM git_logins WHERE id=$1`, login.ID).Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte(state)) {
		t.Fatal("state stored in cleartext", err)
	}
	x := claimGit(t, g, login)
	if err = g.Exchange(ctx, x, "code"); err != nil {
		t.Fatal(err)
	}
	if _, err = g.Claim(ctx, state); !errors.Is(err, ErrTransition) {
		t.Fatal("callback replay accepted", err)
	}
	status, err := g.Status(ctx, a[0], login.ConnectionID)
	if err != nil || status.Connection.State != "connected" || status.Connection.GitHubUserID != 123 || status.Login.URL != "" {
		t.Fatal("wrong connected state", err)
	}
	raw, _ := json.Marshal(status)
	if bytes.Contains(raw, []byte("ghu_")) || bytes.Contains(raw, []byte("ghr_")) || bytes.Contains(raw, []byte(state)) {
		t.Fatal("private values in status")
	}
	if _, err = g.Disconnect(ctx, a[1], login.ConnectionID, uuid.NewString()); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign disconnect", err)
	}
	if _, err = g.Repositories(ctx, a[0], login.ConnectionID, 999, 1, 1); !errors.Is(err, ErrDenied) {
		t.Fatal("uninstalled repository accepted", err)
	}
	f.mu.Lock()
	calls := f.repositories
	f.mu.Unlock()
	if calls != 0 {
		t.Fatal("called repository API outside allowed installation")
	}
}
func TestGitCancellationFencesLateOAuthAndRevokesCandidate(t *testing.T) {
	g, f, a := gitFixture(t)
	ctx := t.Context()
	login, err := g.Begin(ctx, a[0], uuid.NewString(), "")
	if err != nil {
		t.Fatal(err)
	}
	x := claimGit(t, g, login)
	f.started = make(chan struct{})
	f.proceed = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- g.Exchange(ctx, x, "code") }()
	<-f.started
	if _, err = g.Cancel(ctx, a[0], login.ConnectionID, login.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	close(f.proceed)
	if err = <-done; !errors.Is(err, ErrTransition) {
		t.Fatal("late callback connected", err)
	}
	status, err := g.Status(ctx, a[0], login.ConnectionID)
	if err != nil || status.Connection.State == "connected" || status.Login.State != "cancelled" {
		t.Fatal("cancellation lost", err)
	}
	if err = g.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revokes != 1 || f.exchanges != 1 {
		t.Fatal("candidate cleanup or exchange count wrong")
	}
}
func TestGitReconnectPinsIdentityAndKeepsExistingAuthorization(t *testing.T) {
	g, f, a := gitFixture(t)
	ctx := t.Context()
	c := connectGit(t, g, a[0])
	login, err := g.Begin(ctx, a[0], uuid.NewString(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.user = 999
	if err = g.Exchange(ctx, claimGit(t, g, login), "code"); !errors.Is(err, gitprovider.ErrAuthorization) {
		t.Fatal("identity changed", err)
	}
	status, err := g.Status(ctx, a[0], c.ID)
	if err != nil || status.Connection.GitHubUserID != 123 || status.Connection.State != "connected" || status.Login.FailureCode != "identity_mismatch" {
		t.Fatal("working identity lost", err)
	}
}
func setGitExpiry(t *testing.T, g *GitStore, c GitConnection) {
	t.Helper()
	creds := fixtureGitCredentials("a")
	creds.ExpiresAt = time.Now().Add(-time.Minute)
	encrypted, err := g.seal(creds, gitCredentialBinding(c))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.pool.Exec(t.Context(), `UPDATE git_connections SET encrypted_credentials=$2,access_expires_at=$3 WHERE id=$1`, c.ID, encrypted, creds.ExpiresAt); err != nil {
		t.Fatal(err)
	}
}
func TestGitRefreshSerializesAndDisconnectFencesItsCompletion(t *testing.T) {
	g, f, a := gitFixture(t)
	ctx := t.Context()
	c := connectGit(t, g, a[0])
	setGitExpiry(t, g, c)
	f.started = make(chan struct{})
	f.proceed = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := g.Installations(ctx, a[0], c.ID, 1); done <- err }()
	<-f.started
	if _, err := g.Installations(ctx, a[0], c.ID, 1); !errors.Is(err, ErrTransition) {
		t.Fatal("concurrent refresh was not serialized", err)
	}
	if _, err := g.Disconnect(ctx, a[0], c.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	close(f.proceed)
	if err := <-done; !errors.Is(err, ErrTransition) {
		t.Fatal("refresh revived disconnected connection", err)
	}
	status, err := g.Status(ctx, a[0], c.ID)
	if err != nil || status.Connection.State != "disconnected" {
		t.Fatal(err)
	}
}
func TestGitAmbiguousRefreshRequiresNewConsent(t *testing.T) {
	g, f, a := gitFixture(t)
	ctx := t.Context()
	c := connectGit(t, g, a[0])
	setGitExpiry(t, g, c)
	f.refreshError = gitprovider.ErrUnavailable
	for i := 0; i < 2; i++ {
		if _, err := g.Installations(ctx, a[0], c.ID, 1); !errors.Is(err, gitprovider.ErrAuthorization) {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	calls := f.refreshes
	f.mu.Unlock()
	if calls != 1 {
		t.Fatal("ambiguous refresh repeated")
	}
	status, err := g.Status(ctx, a[0], c.ID)
	if err != nil || status.Connection.State != "needs_login" {
		t.Fatal("ambiguous refresh not visible", err)
	}
}

func TestGitRefreshKeepsCredentialsWhenIdentityCheckIsUnavailable(t *testing.T) {
	g, f, a := gitFixture(t)
	ctx := t.Context()
	c := connectGit(t, g, a[0])
	setGitExpiry(t, g, c)
	f.mu.Lock()
	f.userError = gitprovider.ErrUnavailable
	f.mu.Unlock()
	if _, err := g.Installations(ctx, a[0], c.ID, 1); err != nil {
		t.Fatal("a brief identity failure discarded refreshed credentials", err)
	}
	status, err := g.Status(ctx, a[0], c.ID)
	if err != nil || status.Connection.State != "connected" || status.Connection.Epoch != c.Epoch {
		t.Fatal("connection was forced to sign in again", status.Connection.State, err)
	}
	f.mu.Lock()
	f.userError = gitprovider.ErrAuthorization
	f.mu.Unlock()
	setGitExpiry(t, g, c)
	if _, err = g.Installations(ctx, a[0], c.ID, 1); !errors.Is(err, gitprovider.ErrAuthorization) {
		t.Fatal("a rejected token kept the connection", err)
	}
}

func TestGitExpiryAndAbandonedRefreshDoNotRepeatProvider(t *testing.T) {
	g, f, a := gitFixture(t)
	ctx := t.Context()
	login, err := g.Begin(ctx, a[0], uuid.NewString(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.pool.Exec(ctx, `UPDATE git_logins SET expires_at=now()-interval '1 minute' WHERE id=$1`, login.ID); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(login.URL)
	if _, err = g.Claim(ctx, u.Query().Get("state")); !errors.Is(err, ErrTransition) {
		t.Fatal("expired callback accepted", err)
	}
	status, err := g.Status(ctx, a[0], login.ConnectionID)
	if err != nil || status.Login.State != "expired" || status.Login.URL != "" {
		t.Fatal("expired challenge survived", err)
	}
	login, err = g.Begin(ctx, a[0], uuid.NewString(), login.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Exchange(ctx, claimGit(t, g, login), "code"); err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.pool.Exec(ctx, `UPDATE git_connections SET refresh_claim_id=$2,refresh_started_at=now()-interval '1 minute' WHERE id=$1`, login.ConnectionID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if err = g.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = g.Status(ctx, a[0], login.ConnectionID)
	if err != nil || status.Connection.State != "needs_login" {
		t.Fatal("abandoned refresh was not fenced", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refreshes != 0 || f.revokes != 1 {
		t.Fatal("recovery repeated provider refresh or lost cleanup")
	}
}

func TestGitBrowserDisconnectRecordsUnknownExchange(t *testing.T) {
	g, f, a := gitFixture(t)
	login, err := g.Begin(t.Context(), a[0], uuid.NewString(), "")
	if err != nil {
		t.Fatal(err)
	}
	x := claimGit(t, g, login)
	f.started = make(chan struct{})
	f.proceed = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- g.Exchange(ctx, x, "code") }()
	<-f.started
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	status, err := g.Status(t.Context(), a[0], login.ConnectionID)
	if err != nil || status.Login.State != "failed" || status.Login.FailureCode != "exchange_unknown" {
		t.Fatal("browser cancellation left exchange pending", err)
	}
}
