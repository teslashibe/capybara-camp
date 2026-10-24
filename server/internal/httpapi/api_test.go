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
		if method != "GET" {
			r.Header.Set("Idempotency-Key", uuid.NewString())
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
	var openStreams []*http.Response
	for i := 0; i < 4; i++ {
		stream := request("GET", "/v1/camps/"+c.ID+"/events", tokens[owner.UserID], "", "")
		defer stream.Body.Close()
		if stream.StatusCode != 200 {
			t.Fatalf("opening stream %d: %d", i, stream.StatusCode)
		}
		openStreams = append(openStreams, stream)
	}
	excess := request("GET", "/v1/camps/"+c.ID+"/events", tokens[owner.UserID], "", "")
	excess.Body.Close()
	if excess.StatusCode != 429 {
		t.Fatalf("stream limit missing: %d", excess.StatusCode)
	}
	for _, stream := range openStreams {
		stream.Body.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.streamMu.Lock()
		count := a.streams[owner.DeviceID]
		a.streamMu.Unlock()
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected streams retained")
		}
		time.Sleep(10 * time.Millisecond)
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
	project, err := a.Camps.CreateProject(ctx, owner, c.ID, uuid.NewString(), "Test project")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := a.Camps.RegisterProfile(ctx, owner, uuid.NewString(), camp.ProfileInput{LocalID: strings.Repeat("b", 64), Provider: "Codex", Label: "Test profile"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := uuid.NewString()
	sessionBody, _ := json.Marshal(camp.SessionInput{ProjectID: project.ID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: "HTTP session", State: "idle", Stage: "Ready", Shared: true})
	res = request("PUT", "/v1/camps/"+c.ID+"/sessions/"+sessionID, tokens[owner.UserID], string(sessionBody), "")
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("publish status: %d", res.StatusCode)
	}
	res = request("POST", "/v1/camps/"+c.ID+"/sessions/"+sessionID+"/requests", tokens[owner.UserID], `{"prompt":"HTTP request","model":"selected-model","speed":"fast"}`, "")
	var queued camp.Request
	if err = json.NewDecoder(res.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 201 || queued.State != "queued" || queued.Revision != 1 {
		t.Fatalf("queue response: %d %+v", res.StatusCode, queued)
	}
	res = request("POST", "/v1/camps/"+c.ID+"/requests/claim", tokens[owner.UserID], "", "")
	var claimed camp.Request
	if err = json.NewDecoder(res.Body).Decode(&claimed); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || claimed.LeaseToken == "" || claimed.ID != queued.ID {
		t.Fatal("claim did not deliver the request")
	}
	path := "/v1/camps/" + c.ID + "/requests/" + queued.ID + "/actions"
	for _, action := range []camp.RequestAction{{Action: "start", LeaseToken: claimed.LeaseToken}, {Action: "cancel"}, {Action: "finish", LeaseToken: claimed.LeaseToken, Outcome: "cancelled", Evidence: "Confirmed provider cancellation"}} {
		encoded, _ := json.Marshal(action)
		res = request("POST", path, tokens[owner.UserID], string(encoded), "")
		var current camp.Request
		if err = json.NewDecoder(res.Body).Decode(&current); err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 || current.LeaseToken != "" {
			t.Fatalf("action %s: %d %+v", action.Action, res.StatusCode, current)
		}
	}
}
