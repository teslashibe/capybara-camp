package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

type gitHTTPProvider struct{}

func (gitHTTPProvider) AuthorizeURL(state, verifier string) string {
	return "https://github.com/login/oauth/authorize?state=" + url.QueryEscape(state)
}
func (gitHTTPProvider) InstallURL() string {
	return "https://github.com/apps/fixture/installations/new"
}
func (gitHTTPProvider) Exchange(context.Context, string, string) (gitprovider.Credentials, error) {
	return gitprovider.Credentials{AccessToken: "ghu_" + strings.Repeat("a", 36), RefreshToken: "ghr_" + strings.Repeat("b", 36), ExpiresAt: time.Now().Add(time.Hour), RefreshExpiresAt: time.Now().Add(24 * time.Hour)}, nil
}
func (gitHTTPProvider) Refresh(context.Context, gitprovider.Credentials) (gitprovider.Credentials, error) {
	return gitprovider.Credentials{}, gitprovider.ErrAuthorization
}
func (gitHTTPProvider) User(context.Context, string) (gitprovider.User, error) {
	return gitprovider.User{ID: 123, Login: "test-user"}, nil
}
func (gitHTTPProvider) Revoke(context.Context, gitprovider.Credentials) error { return nil }
func (gitHTTPProvider) Installations(context.Context, string, int) (gitprovider.Installations, error) {
	return gitprovider.Installations{Values: []gitprovider.Installation{{ID: 10, Account: gitprovider.User{ID: 123, Login: "test-user"}, Contents: "read"}}}, nil
}
func (gitHTTPProvider) Repositories(context.Context, string, gitprovider.Installation, int) (gitprovider.Repositories, error) {
	return gitprovider.Repositories{Values: []gitprovider.Repository{{ID: 20, InstallationID: 10, FullName: "test-user/project", Read: true}}}, nil
}

func TestGitHTTPConsentAndSignedRevocation(t *testing.T) {
	pool := testdb.Open(t)
	ctx := t.Context()
	store := camp.New(pool, "fixture-secret")
	g, err := camp.NewGitStore(store, gitHTTPProvider{}, bytes.Repeat([]byte{37}, 32))
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Pool: pool, Auth: auth.New(pool, strings.Repeat("s", 32), "https://camp.example", unusedMail{}), Camps: store, Git: g, Origin: "https://camp.example", GitWebhookSecret: strings.Repeat("w", 32)}
	tokens := make([]string, 2)
	for i := range tokens {
		u := uuid.NewString()
		tokens[i], err = auth.Token()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,'Person')`, u, uuid.NewString()+"@example.com"); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Test',$3,now()+interval '1 day')`, uuid.NewString(), u, auth.HashToken(tokens[i])); err != nil {
			t.Fatal(err)
		}
	}
	handler := api.Handler()
	call := func(method, path, token, body string, headers map[string]string, status int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:12345"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set("Idempotency-Key", uuid.NewString())
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d (%s)", method, strings.Split(path, "?")[0], w.Code, status, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("auth response cacheable")
		}
		return w.Body.Bytes()
	}
	call("GET", "/v1/git/connections", "", "", nil, 401)
	raw := call("POST", "/v1/git/logins", tokens[0], `{}`, nil, 201)
	var login camp.GitLogin
	if json.Unmarshal(raw, &login) != nil {
		t.Fatal("login decode")
	}
	call("GET", "/v1/git/connections/"+login.ConnectionID, tokens[1], "", nil, 404)
	u, _ := url.Parse(login.URL)
	callback := "/v1/git/github/callback?state=" + url.QueryEscape(u.Query().Get("state")) + "&code=fixture-code"
	response := call("GET", callback, "", "", nil, 200)
	if !bytes.Contains(response, []byte("GitHub is connected")) || bytes.Contains(response, []byte("fixture-code")) {
		t.Fatal("unsafe callback page")
	}
	call("GET", "/v1/git/connections/"+login.ConnectionID+"/installations", tokens[0], "", nil, 200)
	call("GET", "/v1/git/connections/"+login.ConnectionID+"/installations/999/repositories", tokens[0], "", nil, 404)
	call("GET", "/v1/git/connections/"+login.ConnectionID+"/installations/10/repositories?page=0", tokens[0], "", nil, 400)
	raw = call("GET", "/v1/git/connections/"+login.ConnectionID+"/installations/10/repositories", tokens[0], "", nil, 200)
	if bytes.Contains(raw, []byte("ghu_")) || !bytes.Contains(raw, []byte("test-user/project")) {
		t.Fatal("repository response leaked auth or missed project")
	}
	body := `{"action":"revoked","sender":{"id":123}}`
	delivery := uuid.NewString()
	headers := map[string]string{"X-GitHub-Event": "github_app_authorization", "X-GitHub-Delivery": delivery, "X-Hub-Signature-256": "sha256=bad"}
	call("POST", "/v1/git/github/events", "", body, headers, 401)
	mac := hmac.New(sha256.New, []byte(api.GitWebhookSecret))
	_, _ = io.WriteString(mac, body)
	headers["X-Hub-Signature-256"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	for i := 0; i < 2; i++ {
		call("POST", "/v1/git/github/events", "", body, headers, 204)
	}
	raw = call("GET", "/v1/git/connections/"+login.ConnectionID, tokens[0], "", nil, 200)
	var state camp.GitStatus
	if json.Unmarshal(raw, &state) != nil || state.Connection.State != "needs_login" {
		t.Fatal("signed revocation did not fence authorization")
	}
	call("GET", "/v1/git/connections/"+login.ConnectionID+"/installations", tokens[0], "", nil, 409)
	if state.Connection.Epoch != 3 {
		t.Fatal("webhook replay changed epoch")
	}
	raw = call("POST", "/v1/git/logins", tokens[0], `{"connection_id":"`+login.ConnectionID+`"}`, nil, 201)
	if json.Unmarshal(raw, &login) != nil {
		t.Fatal("retry login decode")
	}
	u, _ = url.Parse(login.URL)
	call("GET", "/v1/git/github/callback?state="+url.QueryEscape(u.Query().Get("state"))+"&error=access_denied", "", "", nil, 200)
	raw = call("GET", "/v1/git/connections/"+login.ConnectionID, tokens[0], "", nil, 200)
	if json.Unmarshal(raw, &state) != nil || state.Login.State != "failed" || state.Login.FailureCode != "provider_rejected" {
		t.Fatal("denied OAuth left pending login")
	}
}
