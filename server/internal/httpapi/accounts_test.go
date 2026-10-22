package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

func TestCloudAccountHTTPSeparatesPeopleAndRunnerGrants(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	store, err := camp.NewWithProviderKey(pool, "receipts", bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Pool: pool, Auth: auth.New(pool, strings.Repeat("s", 32), "https://camp.example", unusedMail{}), Camps: store, Origin: "https://camp.example"}
	actors := make([]auth.Actor, 2)
	tokens := make([]string, 2)
	for i := range actors {
		actors[i] = auth.Actor{UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: []string{"owner@example.com", "member@example.com"}[i], Name: "Person"}
		u := actors[i]
		tokens[i], err = auth.Token()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, u.UserID, u.Email, u.Name); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Test',$3,now()+interval '1 day')`, u.DeviceID, u.UserID, auth.HashToken(tokens[i])); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	call := func(method, path, token, body string, status int) []byte {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", uuid.NewString())
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(io.LimitReader(res.Body, 512<<10))
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != status {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, status, data)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("response cacheable")
		}
		return data
	}
	raw := call("POST", "/v1/provider-accounts", tokens[0], `{"provider":"Codex","label":"Cloud Codex"}`, 201)
	var account camp.ProviderAccount
	if err = json.Unmarshal(raw, &account); err != nil {
		t.Fatal(err)
	}
	call("POST", "/v1/provider-accounts", tokens[0], `{"provider":"Codex","label":"x","owner_user_id":"spoof"}`, 400)
	call("POST", "/v1/provider-accounts/"+account.ID+"/runner-grants", tokens[1], "", 404)
	raw = call("POST", "/v1/provider-accounts/"+account.ID+"/runner-grants", tokens[0], "", 201)
	var grant camp.RunnerGrant
	if err = json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	call("GET", "/v1/me", grant.Token, "", 401)
	call("GET", "/v1/provider-accounts", grant.Token, "", 401)
	call("GET", "/v1/runner/account", tokens[0], "", 401)
	call("GET", "/v1/runner/credentials", grant.Token, "", 404)
	call("GET", "/v1/runner/account", grant.Token, "", 200)
	raw = call("POST", "/v1/provider-accounts/"+account.ID+"/logins", tokens[0], "", 201)
	var login camp.ProviderLogin
	if err = json.Unmarshal(raw, &login); err != nil {
		t.Fatal(err)
	}
	call("GET", "/v1/provider-accounts/"+account.ID+"/logins/"+login.ID, tokens[1], "", 404)
	claim := uuid.NewString()
	call("POST", "/v1/runner/logins/"+login.ID+"/claim", grant.Token, `{"claim_id":"`+claim+`"}`, 200)
	call("POST", "/v1/runner/logins/"+login.ID+"/report", grant.Token, `{"claim_id":"`+claim+`","state":"waiting","challenge":{"url":"https://evil.example","user_code":"CODE"}}`, 400)
	call("POST", "/v1/runner/logins/"+login.ID+"/report", grant.Token, `{"claim_id":"`+claim+`","state":"connected","verified_subject":"account-a","credentials":{"auth":"fixture-secret"}}`, 200)
	raw = call("GET", "/v1/provider-accounts", tokens[0], "", 200)
	if bytes.Contains(raw, []byte("fixture-secret")) || bytes.Contains(raw, []byte("credentials")) {
		t.Fatal("secret in account response")
	}
	raw = call("GET", "/v1/runner/credentials", grant.Token, "", 200)
	if !bytes.Contains(raw, []byte("fixture-secret")) {
		t.Fatal("runner missing own credentials")
	}
	raw = call("GET", "/v1/provider-accounts", tokens[1], "", 200)
	if string(bytes.TrimSpace(raw)) != "[]" {
		t.Fatal("other person's account list")
	}
	call("POST", "/v1/provider-accounts/"+account.ID+"/disconnect", tokens[0], "", 200)
	call("GET", "/v1/runner/credentials", grant.Token, "", 404)
	call("POST", "/v1/runner/erased", grant.Token, "", 204)
	call("GET", "/v1/runner/account", grant.Token, "", 401)

	claude, claudeGrant := func() (camp.ProviderAccount, camp.RunnerGrant) {
		account, err := store.CreateProviderAccount(ctx, actors[0], uuid.NewString(), camp.ProviderAccountInput{Provider: "Claude", Label: "Cloud Claude"})
		if err != nil {
			t.Fatal(err)
		}
		grant, err := store.CreateRunnerGrant(ctx, actors[0], account.ID, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		return account, grant
	}()
	login, err = store.BeginProviderLogin(ctx, actors[0], claude.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	claim = uuid.NewString()
	if _, err = store.ClaimRunnerLogin(ctx, claudeGrant.Token, login.ID, claim); err != nil {
		t.Fatal(err)
	}
	challenge := camp.LoginChallenge{URL: "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&state=fixture-login-state&code_challenge=" + strings.Repeat("a", 43) + "&code_challenge_method=S256"}
	if _, err = store.ReportRunnerLogin(ctx, claudeGrant.Token, login.ID, claim, camp.RunnerLoginReport{State: "waiting", Challenge: &challenge}); err != nil {
		t.Fatal(err)
	}
	ownerPath := "/v1/provider-accounts/" + claude.ID + "/logins/" + login.ID + "/code"
	runnerPath := "/v1/runner/logins/" + login.ID + "/code"
	delivery := uuid.NewString()
	takeBody := `{"claim_id":"` + claim + `","delivery_id":"` + delivery + `"}`
	call("POST", runnerPath, claudeGrant.Token, takeBody, 204)
	call("POST", ownerPath, tokens[1], `{"code":"fixture-oauth-code"}`, 404)
	call("POST", ownerPath, claudeGrant.Token, `{"code":"fixture-oauth-code"}`, 401)
	call("POST", ownerPath, tokens[0], `{"code":"fixture-oauth-code","account":"spoof"}`, 400)
	call("POST", ownerPath, tokens[0], `{"code":"bad\ncode"}`, 400)
	raw = call("POST", ownerPath, tokens[0], `{"code":"fixture-oauth-code"}`, 200)
	if bytes.Contains(raw, []byte("fixture-oauth-code")) || !bytes.Contains(raw, []byte(`"code_submitted":true`)) {
		t.Fatal("owner code response leaked or omitted submission state")
	}
	call("POST", runnerPath, tokens[0], takeBody, 401)
	raw = call("POST", runnerPath, claudeGrant.Token, takeBody, 200)
	var taken camp.RunnerLoginCode
	if json.Unmarshal(raw, &taken) != nil || taken.Code != "fixture-oauth-code" {
		t.Fatal("missing runner code")
	}
	ack := `{"claim_id":"` + claim + `","delivery_id":"` + delivery + `","code_id":"` + taken.CodeID + `"}`
	call("POST", runnerPath+"/ack", claudeGrant.Token, ack, 204)
	call("POST", runnerPath+"/ack", claudeGrant.Token, ack, 204)
	call("POST", runnerPath, claudeGrant.Token, takeBody, 204)
}
