package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

// Test binary only. No browser, network provider request or CLI is invoked.
// This drives the real owner/runner API records for interactive native QA.
func serveFixtureProviderAccounts(ctx context.Context, store *camp.Store, owner auth.Actor) {
	grants := map[string]camp.RunnerGrant{}
	claims := map[string]string{}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		accounts, err := store.ProviderAccounts(ctx, owner)
		if err != nil {
			continue
		}
		for _, account := range accounts {
			if account.State == "disconnected" {
				continue
			}
			grant, exists := grants[account.ID]
			if !exists {
				grant, err = store.CreateRunnerGrant(ctx, owner, account.ID, uuid.NewString())
				if err != nil {
					continue
				}
				grants[account.ID] = grant
			}
			state, err := store.RunnerAccount(ctx, grant.Token)
			if err != nil {
				if account.State != "disconnecting" {
					fresh, e := store.CreateRunnerGrant(ctx, owner, account.ID, uuid.NewString())
					if e == nil {
						grants[account.ID] = fresh
					}
				}
				continue
			}
			if state.Action == "erase" {
				_ = store.AcknowledgeRunnerErased(ctx, grant.Token)
				continue
			}
			if state.Login == nil {
				continue
			}
			login := *state.Login
			claim := claims[login.ID]
			if claim == "" {
				claim = uuid.NewString()
				claims[login.ID] = claim
			}
			if _, err = store.ClaimRunnerLogin(ctx, grant.Token, login.ID, claim); err != nil {
				continue
			}
			if login.State != "waiting" {
				challenge := camp.LoginChallenge{URL: "https://auth.openai.com/codex/device", UserCode: "DEMO-1234"}
				if account.Provider == "Claude" {
					challenge = camp.LoginChallenge{URL: "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&state=fixture-login-state&code_challenge=" + strings.Repeat("a", 43) + "&code_challenge_method=S256"}
				}
				_, _ = store.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, camp.RunnerLoginReport{State: "waiting", Challenge: &challenge})
			} else if account.Provider == "Claude" && login.CodeSubmitted {
				delivery := uuid.NewString()
				code, e := store.TakeRunnerLoginCode(ctx, grant.Token, login.ID, claim, delivery)
				if e != nil || code == nil {
					continue
				}
				if e = store.AcknowledgeRunnerLoginCode(ctx, grant.Token, login.ID, claim, delivery, code.CodeID); e != nil {
					continue
				}
				proof := camp.RunnerLoginReport{State: "failed"}
				if code.Code == "demo-authorization-code" {
					proof = camp.RunnerLoginReport{State: "connected", VerifiedSubject: "claude:demo-organization:" + account.ID, Credentials: json.RawMessage(`{"fixture_only":true}`)}
				}
				_, _ = store.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, proof)
			}
		}
	}
}
