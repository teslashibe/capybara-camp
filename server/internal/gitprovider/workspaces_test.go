package gitprovider

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReadGrantIsNarrowedAndVerified(t *testing.T) {
	root := "ghu_" + strings.Repeat("r", 36)
	scoped := "ghu_" + strings.Repeat("s", 36)
	for _, tc := range []struct {
		name                                        string
		wrongUser, broad, wrongRepository, rootEcho bool
	}{{name: "valid"}, {name: "wrong user", wrongUser: true}, {name: "broad permissions", broad: true}, {name: "wrong repository", wrongRepository: true}, {name: "root echo", rootEcho: true}} {
		t.Run(tc.name, func(t *testing.T) {
			revoked := 0
			c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					revoked++
					w.WriteHeader(204)
					return
				}
				if r.URL.Path == "/applications/Iv1.fixture/token/scoped" {
					user, _, ok := r.BasicAuth()
					if !ok || user != "Iv1.fixture" {
						t.Error("scoping needs the app's Basic authentication")
					}
					var in struct {
						Access       string            `json:"access_token"`
						Target       int64             `json:"target_id"`
						Repositories []int64           `json:"repository_ids"`
						Permissions  map[string]string `json:"permissions"`
					}
					_ = json.NewDecoder(r.Body).Decode(&in)
					if in.Access != root || in.Target != 20 || len(in.Repositories) != 1 || in.Repositories[0] != 30 || len(in.Permissions) != 2 || in.Permissions["contents"] != "read" || in.Permissions["metadata"] != "read" {
						t.Error("scoping request widened access")
					}
					permissions := map[string]string{"metadata": "read", "contents": "read"}
					if tc.broad {
						permissions["contents"] = "write"
					}
					uid := int64(10)
					if tc.wrongUser {
						uid = 99
					}
					value := scoped
					if tc.rootEcho {
						value = root
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"token": value, "user": map[string]any{"id": uid}, "installation": map[string]any{"account": map[string]any{"id": 20}, "repository_selection": "selected", "permissions": permissions}})
					return
				}
				if r.URL.Path != "/user/installations/7/repositories" || r.Header.Get("Authorization") != "Bearer "+scoped {
					t.Error("scope check did not use candidate token")
				}
				id := 30
				if tc.wrongRepository {
					id = 999
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "repositories": []any{map[string]any{"id": id}}})
			})
			grant, err := c.ScopeRead(t.Context(), root, time.Now().Add(time.Hour), 10, Installation{ID: 7, Account: User{ID: 20, Login: "org"}, Contents: "read"}, Repository{ID: 30, InstallationID: 7, Read: true})
			if tc.name == "valid" {
				if err != nil || grant.Token != scoped || revoked != 0 {
					t.Fatal("valid grant refused", err)
				}
			} else {
				wantRevoked := 1
				if tc.rootEcho {
					wantRevoked = 0
				}
				if !errors.Is(err, ErrResponse) || revoked != wantRevoked || grant.Token != "" {
					t.Fatal("unsafe candidate survived", err)
				}
			}
		})
	}
}

func TestCommitResolutionPinsExactSHA(t *testing.T) {
	c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/repos/owner/repo/commits/feature%2Fchange" {
			t.Error("ref was not path escaped")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": strings.Repeat("a", 40)})
	})
	sha, err := c.ResolveCommit(t.Context(), "fixture", Repository{ID: 1, FullName: "owner/repo", Read: true}, "feature/change")
	if err != nil || sha != strings.Repeat("a", 40) {
		t.Fatal(err)
	}
	if _, err = c.ResolveCommit(t.Context(), "fixture", Repository{ID: 1, FullName: "../repo", Read: true}, "main"); !errors.Is(err, ErrAccess) {
		t.Fatal("invalid repository allowed")
	}
	if _, err = c.ResolveCommit(t.Context(), "fixture", Repository{ID: 1, FullName: "owner/repo", Read: true}, "../user"); !errors.Is(err, ErrResponse) {
		t.Fatal("traversing ref allowed")
	}
}

func TestReadGrantHonorsBothExpiryBounds(t *testing.T) {
	root := "ghu_" + strings.Repeat("r", 36)
	scoped := "ghu_" + strings.Repeat("s", 36)
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name       string
		expires    time.Time
		wantExpiry time.Time
		wantError  bool
	}{
		{name: "root bounds a longer scoped token", expires: now.Add(2 * time.Hour), wantExpiry: now.Add(time.Hour)},
		{name: "scoped token can expire first", expires: now.Add(10 * time.Minute), wantExpiry: now.Add(10 * time.Minute)},
		{name: "expired scoped token is revoked", expires: now.Add(-time.Minute), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			revoked := false
			c := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "DELETE":
					revoked = true
					w.WriteHeader(http.StatusNoContent)
				case r.Method == "POST":
					_ = json.NewEncoder(w).Encode(map[string]any{"token": scoped, "expires_at": tc.expires, "user": map[string]any{"id": 10}, "installation": map[string]any{"account": map[string]any{"id": 20}, "repository_selection": "selected", "permissions": map[string]string{"contents": "read", "metadata": "read"}}})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "repositories": []any{map[string]any{"id": 30}}})
				}
			})
			grant, err := c.ScopeRead(t.Context(), root, now.Add(time.Hour), 10, Installation{ID: 7, Account: User{ID: 20}, Contents: "read"}, Repository{ID: 30, InstallationID: 7, Read: true})
			if tc.wantError {
				if !errors.Is(err, ErrAuthorization) || !revoked || grant.Token != "" {
					t.Fatal("expired grant survived", err)
				}
			} else if err != nil || revoked || !grant.ExpiresAt.Equal(tc.wantExpiry) {
				t.Fatal("grant lifetime was extended", err)
			}
		})
	}
}
