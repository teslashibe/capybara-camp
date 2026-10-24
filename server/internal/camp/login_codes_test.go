package camp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func waitingClaude(t *testing.T, s *Store, grant RunnerGrant, login ProviderLogin, claim string) {
	t.Helper()
	challenge := LoginChallenge{URL: "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&state=fixture-login-state&code_challenge=" + strings.Repeat("a", 43) + "&code_challenge_method=S256"}
	if _, err := s.ReportRunnerLogin(context.Background(), grant.Token, login.ID, claim, RunnerLoginReport{State: "waiting", Challenge: &challenge}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderCodeOwnerIsolationDeliveryAndErasure(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	owner, other := actors[0], actors[1]
	account, grant := addCloud(t, s, owner, "Claude")
	foreign, foreignGrant := addCloud(t, s, other, "Claude")
	login, claim := startCloudLogin(t, s, owner, account, grant)
	code := ProviderCodeInput{Code: "fixture-authorization-code#fixture-login-state"}
	key, delivery := uuid.NewString(), uuid.NewString()
	if _, err := s.SubmitProviderLoginCode(ctx, owner, account.ID, login.ID, key, code); !errors.Is(err, ErrTransition) {
		t.Fatal("code accepted without challenge", err)
	}
	waitingClaude(t, s, grant, login, claim)
	if out, err := s.TakeRunnerLoginCode(ctx, grant.Token, login.ID, claim, delivery); err != nil || out != nil {
		t.Fatal("pending code", err)
	}
	if _, err := s.SubmitProviderLoginCode(ctx, other, account.ID, login.ID, key, code); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign owner submitted", err)
	}
	if _, err := s.SubmitProviderLoginCode(ctx, other, foreign.ID, login.ID, key, code); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign attempt submitted", err)
	}
	submitted, err := s.SubmitProviderLoginCode(ctx, owner, account.ID, login.ID, key, code)
	if err != nil || !submitted.CodeSubmitted || submitted.CodeConsumed {
		t.Fatal("submit", err)
	}
	if _, err = s.SubmitProviderLoginCode(ctx, owner, account.ID, login.ID, key, code); err != nil {
		t.Fatal("submission replay", err)
	}
	if _, err = s.SubmitProviderLoginCode(ctx, owner, account.ID, login.ID, key, ProviderCodeInput{Code: "changed-code"}); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay", err)
	}
	if _, err = s.SubmitProviderLoginCode(ctx, owner, account.ID, login.ID, uuid.NewString(), code); !errors.Is(err, ErrTransition) {
		t.Fatal("second submission", err)
	}
	var encrypted, receipt []byte
	if err = s.pool.QueryRow(ctx, `SELECT encrypted_code FROM provider_login_attempts WHERE id=$1`, login.ID).Scan(&encrypted); err != nil || len(encrypted) == 0 || bytes.Contains(encrypted, []byte(code.Code)) {
		t.Fatal("plaintext code stored", err)
	}
	if err = s.pool.QueryRow(ctx, `SELECT encrypted_result FROM command_results WHERE user_id=$1 AND request_id=$2`, owner.UserID, key).Scan(&receipt); err != nil || bytes.Contains(receipt, []byte(code.Code)) {
		t.Fatal("plaintext receipt", err)
	}
	encoded, _ := json.Marshal(submitted)
	if bytes.Contains(encoded, []byte(code.Code)) {
		t.Fatal("owner response contains code")
	}
	if _, err = s.TakeRunnerLoginCode(ctx, foreignGrant.Token, login.ID, claim, delivery); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign runner read", err)
	}
	if _, err = s.TakeRunnerLoginCode(ctx, grant.Token, login.ID, uuid.NewString(), delivery); !errors.Is(err, ErrDenied) {
		t.Fatal("wrong claim read", err)
	}
	taken, err := s.TakeRunnerLoginCode(ctx, grant.Token, login.ID, claim, delivery)
	if err != nil || taken == nil || taken.Code != code.Code || taken.CodeID == "" {
		t.Fatal("delivery missing", err)
	}
	retry, err := s.TakeRunnerLoginCode(ctx, grant.Token, login.ID, claim, delivery)
	if err != nil || retry == nil || *retry != *taken {
		t.Fatal("delivery replay changed", err)
	}
	if _, err = s.TakeRunnerLoginCode(ctx, grant.Token, login.ID, claim, uuid.NewString()); !errors.Is(err, ErrConflict) {
		t.Fatal("second delivery accepted", err)
	}
	if err = s.AcknowledgeRunnerLoginCode(ctx, grant.Token, login.ID, claim, uuid.NewString(), taken.CodeID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign acknowledgement", err)
	}
	for i := 0; i < 2; i++ {
		if err = s.AcknowledgeRunnerLoginCode(ctx, grant.Token, login.ID, claim, delivery, taken.CodeID); err != nil {
			t.Fatal("acknowledgement", err)
		}
	}
	if err = s.pool.QueryRow(ctx, `SELECT encrypted_code FROM provider_login_attempts WHERE id=$1`, login.ID).Scan(&encrypted); err != nil || len(encrypted) != 0 {
		t.Fatal("consumed code retained", err)
	}
	if out, err := s.TakeRunnerLoginCode(ctx, grant.Token, login.ID, claim, delivery); err != nil || out != nil {
		t.Fatal("consumed code reissued", err)
	}
	current, err := s.ProviderLogin(ctx, owner, account.ID, login.ID)
	if err != nil || !current.CodeConsumed {
		t.Fatal("missing consumed state", err)
	}
	// A repeated challenge is safe, but a replacement must not rebind a code.
	waitingClaude(t, s, grant, login, claim)
	changed := LoginChallenge{URL: strings.Replace(current.URL, "fixture-login-state", "changed-login-state", 1)}
	if _, err = s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, RunnerLoginReport{State: "waiting", Challenge: &changed}); !errors.Is(err, ErrConflict) {
		t.Fatal("challenge changed after code submission", err)
	}
}

func TestProviderCodeConcurrentClaimsAndTerminalCleanup(t *testing.T) {
	for _, finish := range []string{"cancel", "expire", "reap", "replace", "disconnect", "connected", "failed"} {
		t.Run(finish, func(t *testing.T) {
			s, actors := cloudFixture(t)
			ctx := context.Background()
			owner := actors[0]
			account, grant := addCloud(t, s, owner, "Claude")
			login, claim := startCloudLogin(t, s, owner, account, grant)
			waitingClaude(t, s, grant, login, claim)
			code := ProviderCodeInput{Code: "fixture-terminal-code"}
			if _, err := s.SubmitProviderLoginCode(ctx, owner, account.ID, login.ID, uuid.NewString(), code); err != nil {
				t.Fatal(err)
			}
			var err error
			switch finish {
			case "cancel":
				_, err = s.CancelProviderLogin(ctx, owner, account.ID, login.ID, uuid.NewString())
			case "expire", "reap":
				_, err = s.pool.Exec(ctx, `UPDATE provider_login_attempts SET expires_at=now()-interval '1 second' WHERE id=$1`, login.ID)
				if err == nil && finish == "reap" {
					err = s.ReapProviderLogins(ctx)
				} else if err == nil {
					_, err = s.ProviderLogin(ctx, owner, account.ID, login.ID)
				}
			case "replace":
				_, err = s.CreateRunnerGrant(ctx, owner, account.ID, uuid.NewString())
			case "disconnect":
				_, err = s.DisconnectProviderAccount(ctx, owner, account.ID, uuid.NewString())
			case "connected":
				_, err = s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, loginProof("fixture-subject"))
			case "failed":
				_, err = s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, RunnerLoginReport{State: "failed"})
			}
			if err != nil {
				t.Fatal(err)
			}
			var encrypted []byte
			if err = s.pool.QueryRow(ctx, `SELECT encrypted_code FROM provider_login_attempts WHERE id=$1`, login.ID).Scan(&encrypted); err != nil || len(encrypted) != 0 {
				t.Fatal("terminal code retained", err)
			}
			if _, err = s.TakeRunnerLoginCode(ctx, grant.Token, login.ID, claim, uuid.NewString()); err == nil {
				t.Fatal("terminal code accessible")
			}
		})
	}

	s, actors := cloudFixture(t)
	ctx := context.Background()
	account, grant := addCloud(t, s, actors[0], "Claude")
	login, claim := startCloudLogin(t, s, actors[0], account, grant)
	waitingClaude(t, s, grant, login, claim)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.SubmitProviderLoginCode(ctx, actors[0], account.ID, login.ID, uuid.NewString(), ProviderCodeInput{Code: "concurrent-code"})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrTransition) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d submissions accepted", winners)
	}
}

func TestProviderCodeValidation(t *testing.T) {
	for _, code := range []string{"", "short", "abcdefgh\n", " abcdefgh", "abcdefgh\x00", "abcd\tdefgh", strings.Repeat("a", 4097), "abcdefgh\u2003", "abcdefgh\xff"} {
		if validProviderCode(code) {
			t.Fatal("invalid code accepted")
		}
	}
	if !validProviderCode("abcdefgh-_#state") {
		t.Fatal("valid code refused")
	}
}
