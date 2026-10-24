package camp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

func cloudFixture(t *testing.T) (*Store, []auth.Actor) {
	t.Helper()
	pool := testdb.Open(t)
	s, err := NewWithProviderKey(pool, "fixture-receipts", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	actors := []auth.Actor{{UserID: uuid.NewString(), Email: "owner@example.com", Name: "Owner"}, {UserID: uuid.NewString(), Email: "kate@example.com", Name: "K"}}
	for _, a := range actors {
		if _, err = pool.Exec(context.Background(), `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, a.UserID, a.Email, a.Name); err != nil {
			t.Fatal(err)
		}
	}
	return s, actors
}
func addCloud(t *testing.T, s *Store, a auth.Actor, provider string) (ProviderAccount, RunnerGrant) {
	t.Helper()
	ctx := context.Background()
	account, err := s.CreateProviderAccount(ctx, a, uuid.NewString(), ProviderAccountInput{Provider: provider, Label: "Cloud " + provider})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.CreateRunnerGrant(ctx, a, account.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return account, grant
}
func startCloudLogin(t *testing.T, s *Store, a auth.Actor, account ProviderAccount, grant RunnerGrant) (ProviderLogin, string) {
	t.Helper()
	ctx := context.Background()
	key := uuid.NewString()
	login, err := s.BeginProviderLogin(ctx, a, account.ID, key)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.BeginProviderLogin(ctx, a, account.ID, key)
	if err != nil || retry.ID != login.ID {
		t.Fatal("login replay", err)
	}
	claim := uuid.NewString()
	login, err = s.ClaimRunnerLogin(ctx, grant.Token, login.ID, claim)
	if err != nil || login.State != "running" {
		t.Fatal(login, err)
	}
	return login, claim
}
func loginProof(subject string) RunnerLoginReport {
	return RunnerLoginReport{State: "connected", VerifiedSubject: subject, Credentials: json.RawMessage(`{"fixture_token":"secret-not-for-members"}`)}
}
func TestCloudAccountsStayOwnerBoundAcrossSharedCamp(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	owner, other := actors[0], actors[1]
	c, err := s.Create(ctx, owner, uuid.NewString(), "Shared Camp")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := s.Invite(ctx, owner, c.ID, uuid.NewString(), other.Email, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(ctx, other, uuid.NewString(), inv.Token); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		provider := "Codex"
		if i >= 4 {
			provider = "Claude"
		}
		account, grant := addCloud(t, s, owner, provider)
		if seen[account.InstallationID] {
			t.Fatal("installations merged")
		}
		seen[account.InstallationID] = true
		if _, err = s.CreateRunnerGrant(ctx, other, account.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign grant", err)
		}
		if _, err = s.BeginProviderLogin(ctx, other, account.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign login", err)
		}
		if _, err = s.DisconnectProviderAccount(ctx, other, account.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign disconnect", err)
		}
		state, err := s.RunnerAccount(ctx, grant.Token)
		if err != nil || state.InstallationID != account.InstallationID {
			t.Fatal(state, err)
		}
	}
	accounts, err := s.ProviderAccounts(ctx, owner)
	if err != nil || len(accounts) != 6 {
		t.Fatal(accounts, err)
	}
	accounts, err = s.ProviderAccounts(ctx, other)
	if err != nil || len(accounts) != 0 {
		t.Fatal("Camp membership leaked accounts", accounts, err)
	}
	snapshot, err := s.Snapshot(ctx, other, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(snapshot)
	if bytes.Contains(encoded, []byte("installation_id")) {
		t.Fatal("accounts in shared snapshot")
	}
}
func TestCloudLoginCancellationIdentityAndEncryption(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	owner := actors[0]
	account, grant := addCloud(t, s, owner, "Codex")
	login, claim := startCloudLogin(t, s, owner, account, grant)
	challenge := RunnerLoginReport{State: "waiting", Challenge: &LoginChallenge{URL: "https://auth.openai.com/codex/device", UserCode: "ABCD-1234"}}
	waiting, err := s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, challenge)
	if err != nil || waiting.UserCode != "ABCD-1234" {
		t.Fatal(waiting, err)
	}
	var encrypted []byte
	if err = s.pool.QueryRow(ctx, `SELECT encrypted_challenge FROM provider_login_attempts WHERE id=$1`, login.ID).Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte("ABCD-1234")) {
		t.Fatal("plaintext challenge", err)
	}
	cancelled, err := s.CancelProviderLogin(ctx, owner, account.ID, login.ID, uuid.NewString())
	if err != nil || cancelled.State != "cancelled" || cancelled.URL != "" {
		t.Fatal(cancelled, err)
	}
	if _, err = s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, loginProof("subject-a")); !errors.Is(err, ErrTransition) {
		t.Fatal("late completion", err)
	}
	login, claim = startCloudLogin(t, s, owner, account, grant)
	connected, err := s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, loginProof("subject-a"))
	if err != nil || connected.State != "connected" {
		t.Fatal(connected, err)
	}
	if _, err = s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, loginProof("subject-a")); err != nil {
		t.Fatal("proof replay", err)
	}
	if _, err = s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, loginProof("different")); !errors.Is(err, ErrConflict) {
		t.Fatal("changed proof accepted", err)
	}
	credentials, err := s.RunnerCredentialSnapshot(ctx, grant.Token)
	if err != nil || !bytes.Equal(credentials.Credentials, loginProof("").Credentials) {
		t.Fatal("restore", err)
	}
	if err = s.pool.QueryRow(ctx, `SELECT encrypted_credentials FROM provider_installations WHERE id=$1`, account.InstallationID).Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte("secret-not-for-members")) {
		t.Fatal("plaintext credentials", err)
	}
	foreign, _ := addCloud(t, s, actors[1], "Codex")
	if _, err = s.pool.Exec(ctx, `UPDATE provider_installations SET state='connected',credential_revision=1,encrypted_credentials=$2 WHERE id=$1`, foreign.InstallationID, encrypted); err != nil {
		t.Fatal(err)
	}
	foreignGrant, err := s.CreateRunnerGrant(ctx, actors[1], foreign.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RunnerCredentialSnapshot(ctx, foreignGrant.Token); err == nil {
		t.Fatal("owner-swapped credential envelope decrypted")
	}
	login, claim = startCloudLogin(t, s, owner, account, grant)
	mismatch, err := s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, loginProof("subject-b"))
	if err != nil || mismatch.State != "failed" || mismatch.FailureCode != "identity_mismatch" {
		t.Fatal(mismatch, err)
	}
	credentials, err = s.RunnerCredentialSnapshot(ctx, grant.Token)
	if err != nil || credentials.Revision != 1 {
		t.Fatal("mismatch replaced working credentials", err)
	}
	login, _ = startCloudLogin(t, s, owner, account, grant)
	if _, err = s.CancelProviderLogin(ctx, owner, account.ID, login.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	accounts, err := s.ProviderAccounts(ctx, owner)
	if err != nil || accounts[0].State != "connected" || accounts[0].VerifiedSubject != "subject-a" {
		t.Fatal("cancelled reconnect discarded working login", accounts, err)
	}
	encoded, _ := json.Marshal(accounts)
	if bytes.Contains(encoded, []byte("fixture_token")) {
		t.Fatal("credentials leaked in account list")
	}
}
func TestCloudGrantReplacementExpiryAndDisconnectFence(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	owner := actors[0]
	account, grant := addCloud(t, s, owner, "Codex")
	login, claim := startCloudLogin(t, s, owner, account, grant)
	key := uuid.NewString()
	replacement, err := s.CreateRunnerGrant(ctx, owner, account.ID, key)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.CreateRunnerGrant(ctx, owner, account.ID, key)
	if err != nil || retry.InstallationID != replacement.InstallationID || retry.Token != replacement.Token || retry.Epoch != replacement.Epoch || !retry.ExpiresAt.Equal(replacement.ExpiresAt) {
		t.Fatal("grant replay", err)
	}
	if _, err = s.RunnerAccount(ctx, grant.Token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("old grant active", err)
	}
	old, err := s.ProviderLogin(ctx, owner, account.ID, login.ID)
	if err != nil || old.FailureCode != "runner_replaced" {
		t.Fatal(old, err)
	}
	login, claim = startCloudLogin(t, s, owner, account, replacement)
	if _, err = s.pool.Exec(ctx, `UPDATE provider_login_attempts SET expires_at=now()-interval '1 second' WHERE id=$1`, login.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReportRunnerLogin(ctx, replacement.Token, login.ID, claim, loginProof("subject-a")); !errors.Is(err, ErrTransition) {
		t.Fatal("expired login connected", err)
	}
	expired, err := s.ProviderLogin(ctx, owner, account.ID, login.ID)
	if err != nil || expired.State != "expired" {
		t.Fatal(expired, err)
	}
	login, claim = startCloudLogin(t, s, owner, account, replacement)
	if _, err = s.ReportRunnerLogin(ctx, replacement.Token, login.ID, claim, loginProof("subject-a")); err != nil {
		t.Fatal(err)
	}
	gone, err := s.DisconnectProviderAccount(ctx, owner, account.ID, uuid.NewString())
	if err != nil || gone.State != "disconnecting" || gone.Epoch != 2 {
		t.Fatal(gone, err)
	}
	if _, err = s.RunnerCredentialSnapshot(ctx, replacement.Token); !errors.Is(err, ErrDenied) {
		t.Fatal("cleanup credential read", err)
	}
	if _, err = s.ReportRunnerLogin(ctx, replacement.Token, login.ID, claim, loginProof("subject-a")); !errors.Is(err, ErrDenied) {
		t.Fatal("cleanup login completion", err)
	}
	if _, err = s.CreateRunnerGrant(ctx, owner, account.ID, key); !errors.Is(err, ErrTransition) {
		t.Fatal("grant receipt revived cleanup token", err)
	}
	state, err := s.RunnerAccount(ctx, replacement.Token)
	if err != nil || state.Action != "erase" {
		t.Fatal(state, err)
	}
	if err = s.AcknowledgeRunnerErased(ctx, replacement.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RunnerAccount(ctx, replacement.Token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("erased runner retained access", err)
	}
	accounts, err := s.ProviderAccounts(ctx, owner)
	if err != nil || accounts[0].State != "disconnected" {
		t.Fatal(accounts, err)
	}
	var exists bool
	if err = s.pool.QueryRow(ctx, `SELECT encrypted_credentials IS NOT NULL FROM provider_installations WHERE id=$1`, account.InstallationID).Scan(&exists); err != nil || exists {
		t.Fatal("credentials retained after disconnect", err)
	}
}
func TestCloudConcurrentCreateAndClaim(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	owner := actors[0]
	key := uuid.NewString()
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := s.CreateProviderAccount(ctx, owner, key, ProviderAccountInput{Provider: "Codex", Label: "Same account"})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- a.ID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for value := range ids {
		if id == "" {
			id = value
		} else if id != value {
			t.Fatal("duplicate account")
		}
	}
	if _, err := s.CreateProviderAccount(ctx, owner, key, ProviderAccountInput{Provider: "Claude", Label: "Same account"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	grant, err := s.CreateRunnerGrant(ctx, owner, id, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	login, err := s.BeginProviderLogin(ctx, owner, id, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	successes := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim := uuid.NewString()
			if _, err := s.ClaimRunnerLogin(ctx, grant.Token, login.ID, claim); err == nil {
				successes <- claim
			} else if !errors.Is(err, ErrTransition) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(successes)
	winner := ""
	for claim := range successes {
		if winner != "" {
			t.Fatal("multiple claims")
		}
		winner = claim
	}
	if winner == "" {
		t.Fatal("no claim")
	}
	if _, err = s.ClaimRunnerLogin(ctx, grant.Token, login.ID, winner); err != nil {
		t.Fatal("claim retry", err)
	}
}
func TestCloudAuthURLsAndDisabledKey(t *testing.T) {
	good := LoginChallenge{URL: "https://auth.openai.com/codex/device", UserCode: "ABCD-1234"}
	if !validateLoginChallenge("Codex", good) {
		t.Fatal("device challenge rejected")
	}
	for _, raw := range []string{"http://auth.openai.com/codex/device", "https://auth.openai.com.evil.test/codex/device", "https://secret@auth.openai.com/codex/device", "https://auth.openai.com:443/codex/device", "https://auth.openai.com/codex/%64evice", "https://auth.openai.com/codex/device?redirect=https://evil.test", "https://auth.openai.com/codex/device#token"} {
		bad := good
		bad.URL = raw
		if validateLoginChallenge("Codex", bad) {
			t.Fatal("unsafe URL", raw)
		}
	}
	q := url.Values{"client_id": {"9d1c250a-e61b-44d9-88ed-5944d1962f5e"}, "response_type": {"code"}, "state": {strings.Repeat("s", 32)}, "code_challenge": {strings.Repeat("c", 43)}, "code_challenge_method": {"S256"}, "redirect_uri": {"http://localhost:54545/callback"}}
	claude := LoginChallenge{URL: "https://claude.ai/oauth/authorize?" + q.Encode()}
	if !validateLoginChallenge("Claude", claude) {
		t.Fatal("Claude callback rejected")
	}
	q.Set("redirect_uri", "https://evil.test/callback")
	claude.URL = "https://claude.ai/oauth/authorize?" + q.Encode()
	if validateLoginChallenge("Claude", claude) {
		t.Fatal("external callback allowed")
	}
	if _, err := NewWithProviderKey(nil, "receipt", []byte("too short")); !errors.Is(err, ErrCloudUnavailable) {
		t.Fatal(err)
	}
	if _, err := New(nil, "receipt").ProviderAccounts(context.Background(), auth.Actor{}); !errors.Is(err, ErrCloudUnavailable) {
		t.Fatal(err)
	}
	for i, p := range []RunnerLoginReport{{State: "connected", VerifiedSubject: "a", Credentials: json.RawMessage(`[]`)}, {State: "connected", VerifiedSubject: "a", Credentials: json.RawMessage(`{}`)}, {State: "connected", VerifiedSubject: "a\nb", Credentials: json.RawMessage(`{"a":1}`)}, {State: "waiting", Challenge: &good, Credentials: json.RawMessage(`{"a":1}`)}} {
		if validLoginReport("Codex", p) {
			t.Fatal(fmt.Sprint("bad report ", i))
		}
	}
}

func TestCloudGrantExpiryAndForeignAttempt(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	account, grant := addCloud(t, s, actors[0], "Codex")
	_, other := addCloud(t, s, actors[0], "Codex")
	login, claim := startCloudLogin(t, s, actors[0], account, grant)
	if _, err := s.ClaimRunnerLogin(ctx, other.Token, login.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign installation claim", err)
	}
	if _, err := s.ReportRunnerLogin(ctx, other.Token, login.ID, claim, loginProof("subject")); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign installation proof", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE runner_grants SET expires_at=now()-interval '1 second' WHERE installation_id=$1`, account.InstallationID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, loginProof("subject")); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("expired grant completed login", err)
	}
}

func TestCloudCancelAndCompleteSerialize(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	owner := actors[0]
	account, grant := addCloud(t, s, owner, "Codex")
	for i := 0; i < 10; i++ {
		login, claim := startCloudLogin(t, s, owner, account, grant)
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, err := s.CancelProviderLogin(ctx, owner, account.ID, login.ID, uuid.NewString())
			results <- err
		}()
		go func() {
			<-start
			_, err := s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, loginProof("subject"))
			results <- err
		}()
		close(start)
		for j := 0; j < 2; j++ {
			if err := <-results; err != nil && !errors.Is(err, ErrTransition) {
				t.Fatal(err)
			}
		}
		final, err := s.ProviderLogin(ctx, owner, account.ID, login.ID)
		if err != nil {
			t.Fatal(err)
		}
		if final.State != "connected" && final.State != "cancelled" {
			t.Fatal(final.State)
		}
		var revision int64
		if err = s.pool.QueryRow(ctx, `SELECT credential_revision FROM provider_installations WHERE id=$1`, account.InstallationID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if final.State == "cancelled" {
			if _, err = s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, loginProof("subject")); !errors.Is(err, ErrTransition) {
				t.Fatal("cancelled race reactivated", err)
			}
			var after int64
			if err = s.pool.QueryRow(ctx, `SELECT credential_revision FROM provider_installations WHERE id=$1`, account.InstallationID).Scan(&after); err != nil || after != revision {
				t.Fatal("late proof changed credentials", err)
			}
		}
	}
}

func TestCloudMaintenanceClearsExpiredChallenge(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	account, grant := addCloud(t, s, actors[0], "Codex")
	login, claim := startCloudLogin(t, s, actors[0], account, grant)
	_, err := s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, RunnerLoginReport{State: "waiting", Challenge: &LoginChallenge{URL: "https://auth.openai.com/codex/device", UserCode: "ABCD-1234"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE provider_login_attempts SET expires_at=now()-interval '1 second' WHERE id=$1`, login.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ReapProviderLogins(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var encrypted []byte
	if err = s.pool.QueryRow(ctx, `SELECT state,encrypted_challenge FROM provider_login_attempts WHERE id=$1`, login.ID).Scan(&state, &encrypted); err != nil || state != "expired" || encrypted != nil {
		t.Fatal(state, "challenge retained", err)
	}
	if err = s.ReapProviderLogins(ctx); err != nil {
		t.Fatal("repeated maintenance", err)
	}
}

func TestCloudUnprovisionedAccountDisconnectsImmediately(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	key := uuid.NewString()
	account, err := s.CreateProviderAccount(ctx, actors[0], key, ProviderAccountInput{Provider: "Codex", Label: "Not connected yet"})
	if err != nil {
		t.Fatal(err)
	}
	disconnected, err := s.DisconnectProviderAccount(ctx, actors[0], account.ID, uuid.NewString())
	if err != nil || disconnected.State != "disconnected" {
		t.Fatal(disconnected, err)
	}
	replay, err := s.CreateProviderAccount(ctx, actors[0], key, ProviderAccountInput{Provider: "Codex", Label: "Not connected yet"})
	if err != nil || replay.State != "disconnected" || replay.ID != account.ID {
		t.Fatal("create replay revived account", replay, err)
	}
	if _, err = s.CreateRunnerGrant(ctx, actors[0], account.ID, uuid.NewString()); !errors.Is(err, ErrTransition) {
		t.Fatal("disconnected account accepted runner", err)
	}
}
