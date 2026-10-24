package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
)

// This test-only provider never contacts GitHub or executes a checkout. It
// drives the real setup/authorization contract for native UI inspection.
type nativeGitProvider struct{ gitHTTPProvider }

func (nativeGitProvider) Installations(context.Context, string, int) (gitprovider.Installations, error) {
	return gitprovider.Installations{Values: []gitprovider.Installation{{ID: 10, Account: gitprovider.User{ID: 123, Login: "test-user"}, Contents: "write", PullRequests: "write"}}}, nil
}
func (nativeGitProvider) Repositories(context.Context, string, gitprovider.Installation, int) (gitprovider.Repositories, error) {
	return gitprovider.Repositories{Values: []gitprovider.Repository{{ID: 20, InstallationID: 10, FullName: "test-user/project", DefaultBranch: "main", Read: true, Write: true, PullRequests: true}}}, nil
}
func (nativeGitProvider) ResolveCommit(context.Context, string, gitprovider.Repository, string) (string, error) {
	return strings.Repeat("a", 40), nil
}
func (nativeGitProvider) ScopeRead(_ context.Context, _ string, _ time.Time, user int64, _ gitprovider.Installation, repository gitprovider.Repository) (gitprovider.ReadGrant, error) {
	return gitprovider.ReadGrant{Token: "ghu_" + strings.Repeat("f", 36), ExpiresAt: time.Now().Add(5 * time.Minute), RepositoryID: repository.ID, UserID: user}, nil
}

func seedNativeAccounts(t *testing.T, store *camp.Store, owner auth.Actor) map[string]camp.RunnerGrant {
	t.Helper()
	ctx := t.Context()
	grants := map[string]camp.RunnerGrant{}
	for _, provider := range []string{"Codex", "Claude"} {
		account, err := store.CreateProviderAccount(ctx, owner, uuid.NewString(), camp.ProviderAccountInput{Provider: provider, Label: "Demo " + provider})
		if err != nil {
			t.Fatal(err)
		}
		grant, err := store.CreateRunnerGrant(ctx, owner, account.ID, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		login, err := store.BeginProviderLogin(ctx, owner, account.ID, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		claim := uuid.NewString()
		if _, err = store.ClaimRunnerLogin(ctx, grant.Token, login.ID, claim); err != nil {
			t.Fatal(err)
		}
		if _, err = store.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, camp.RunnerLoginReport{State: "connected", VerifiedSubject: "native-fixture:" + account.ID, Credentials: json.RawMessage(`{"fixture_only":true}`)}); err != nil {
			t.Fatal(err)
		}
		grants[account.ID] = grant
	}
	return grants
}

func prepareNativeWorkspace(ctx context.Context, git *camp.GitStore, grant camp.RunnerGrant) {
	workspace, err := git.NextRunnerWorkspace(ctx, grant.Token)
	if err != nil || workspace == nil || workspace.State != "queued" {
		return
	}
	claim := uuid.NewString()
	if _, err = git.ClaimRunnerWorkspace(ctx, grant.Token, workspace.ID, claim); err != nil {
		return
	}
	if _, err = git.RunnerWorkspaceToken(ctx, grant.Token, workspace.ID, claim); err != nil {
		return
	}
	_, _ = git.ReportRunnerWorkspace(ctx, grant.Token, workspace.ID, claim, camp.WorkspaceReport{State: "ready", BaseCommit: workspace.BaseCommit, Branch: workspace.Branch})
}
