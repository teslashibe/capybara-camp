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
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

type unusedMail struct{}

func (unusedMail) Send(context.Context, string, string, string) error { return nil }
func TestHTTPAuthorizationAndRevokedStream(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	secret := strings.Repeat("test", 10)
	a := &API{Pool: pool, Auth: auth.New(pool, secret, "https://camp.example", unusedMail{}), Camps: camp.New(pool, "fixture-receipt-key"), Origin: "https://camp.example"}
	owner := auth.Actor{UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: "owner@example.com", Name: "Owner"}
	member := auth.Actor{UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: "member@example.com", Name: "Member"}
	tokens := map[string]string{}
	for _, u := range []auth.Actor{owner, member} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, u.UserID, u.Email, u.Name); err != nil {
			t.Fatal(err)
		}
		token, err := auth.Token()
		if err != nil {
			t.Fatal(err)
		}
		tokens[u.UserID] = token
		if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Test Mac',$3,now()+interval '1 day')`, u.DeviceID, u.UserID, auth.HashToken(token)); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	request := func(method, path, token, body, origin string) *http.Response {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for _, tc := range []struct {
		path, token, origin string
		status              int
	}{{"/health", "", "", 200}, {"/ready", "", "", 200}, {"/v1/camps", "", "", 401}, {"/v1/me", tokens[owner.UserID], "https://attacker.example", 403}} {
		res := request("GET", tc.path, tc.token, "", tc.origin)
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("%s: %d", tc.path, res.StatusCode)
		}
	}
	for _, body := range []string{`{"name":"Camp","unexpected":true}`, `{"name":"Camp"}{}`} {
		res := request("POST", "/v1/camps", tokens[owner.UserID], body, "")
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("invalid JSON status %d", res.StatusCode)
		}
	}
	c, err := a.Camps.Create(ctx, owner, uuid.NewString(), "Stream test")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := a.Camps.Invite(ctx, owner, c.ID, uuid.NewString(), member.Email, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Camps.Accept(ctx, member, uuid.NewString(), inv.Token); err != nil {
		t.Fatal(err)
	}
	res := request("GET", "/v1/camps/"+c.ID+"/events", tokens[member.UserID], "", "")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream headers: %d", res.StatusCode)
	}
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, res.Body); res.Body.Close(); done <- err }()
	if err = a.Camps.RemoveMember(ctx, owner, c.ID, uuid.NewString(), member.UserID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("revoked stream stayed open")
	}
	res = request("GET", "/v1/camps/"+c.ID+"/snapshot", tokens[member.UserID], "", "")
	defer res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("revoked snapshot status: %d", res.StatusCode)
	}
	var body map[string]any
	if err = json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["error"] != "not_available" {
		t.Fatalf("unexpected public error: %v", body)
	}
}
