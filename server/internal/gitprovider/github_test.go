package gitprovider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/teslashibe/capybara-camp/server/internal/config"
)

func clientFixture(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	c, err := New(config.GitHub{ClientID: "Iv1.fixture", ClientSecret: strings.Repeat("s", 40), AppSlug: "fixture-camp", EncryptionKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), WebhookSecret: strings.Repeat("w", 32)}, "https://camp.example", s.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.web = s.URL
	c.api = s.URL
	return c
}
func TestGitHubOAuthAndUserScope(t *testing.T) {
	var calls int
	c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/login/oauth/access_token" {
			if r.Header.Get("Accept") != "application/json" || r.Method != "POST" || r.URL.RawQuery != "" {
				t.Error("unsafe OAuth transport")
			}
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["client_secret"] != strings.Repeat("s", 40) || in["code_verifier"] != "fixture-verifier" {
				t.Error("missing PKCE or client binding")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "ghu_" + strings.Repeat("a", 36), "refresh_token": "ghr_" + strings.Repeat("b", 36), "expires_in": 28800, "refresh_token_expires_in": 15897600, "token_type": "bearer"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer ghu_"+strings.Repeat("a", 36) {
			t.Error("wrong user token")
		}
		switch r.URL.Path {
		case "/user":
			_, _ = io.WriteString(w, `{"id":123,"login":"my-user"}`)
		case "/user/installations":
			_, _ = io.WriteString(w, `{"total_count":1,"installations":[{"id":10,"account":{"id":123,"login":"my-user"},"app_slug":"fixture-camp","permissions":{"contents":"read","pull_requests":"write"}}]}`)
		case "/user/installations/10/repositories":
			_, _ = io.WriteString(w, `{"total_count":1,"repositories":[{"id":30,"full_name":"my-user/project","default_branch":"main","permissions":{"pull":true,"push":true}}]}`)
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
		}
	})
	u, _ := url.Parse(c.AuthorizeURL("state", "verifier"))
	if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("scope") != "offline_access" || u.Query().Get("prompt") != "select_account" {
		t.Fatal("authorization does not bind identity and PKCE")
	}
	creds, err := c.Exchange(t.Context(), "fixture-code", "fixture-verifier")
	if err != nil || !creds.Valid() {
		t.Fatal(err)
	}
	user, err := c.User(t.Context(), creds.AccessToken)
	if err != nil || user.ID != 123 {
		t.Fatal(err)
	}
	installs, err := c.Installations(t.Context(), creds.AccessToken, 1)
	if err != nil || len(installs.Values) != 1 {
		t.Fatal(err)
	}
	repos, err := c.Repositories(t.Context(), creds.AccessToken, installs.Values[0], 1)
	if err != nil || len(repos.Values) != 1 || !repos.Values[0].Read || repos.Values[0].Write {
		t.Fatal("app read-only permission widened", err)
	}
	if calls != 4 {
		t.Fatal("unexpected provider operations")
	}
}
func TestGitHubRejectsRedirectsAndMalformedCredentials(t *testing.T) {
	var destinationCalls int
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls++ }))
	defer destination.Close()
	c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) })
	if _, err := c.User(context.Background(), "ghu_fixture_token"); !errors.Is(err, ErrResponse) || destinationCalls != 0 {
		t.Fatal("redirect followed")
	}
	for _, body := range []string{`{"error":"bad_verification_code"}`, `{"access_token":"gho_wrong_kind","token_type":"bearer"}`, `{"access_token":"ghu_ok","refresh_token":"ghr_short","expires_in":0}`} {
		c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
		if _, err := c.Exchange(t.Context(), "code", "verifier"); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
}
func TestGitHubRevokeUsesAppBasicAndTokenBody(t *testing.T) {
	c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || user != "Iv1.fixture" || secret != strings.Repeat("s", 40) || r.Method != "DELETE" || r.URL.RawQuery != "" {
			t.Error("unsafe revocation auth")
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["access_token"] != "fixture-token" {
			t.Error("wrong token")
		}
		w.WriteHeader(204)
	})
	if err := c.Revoke(t.Context(), Credentials{AccessToken: "fixture-token"}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubRateLimitIsNotAPermissionFailure(t *testing.T) {
	c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(403)
	})
	if _, err := c.User(t.Context(), "fixture-token"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("rate limit misclassified", err)
	}
}
