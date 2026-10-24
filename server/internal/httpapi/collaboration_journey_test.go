package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

// Only email delivery is substituted. Accounts, OTP verification, credentials,
// Camp membership, requests and replay all go through the actual HTTP API/DB.
type journeyMailbox struct {
	mu    sync.Mutex
	codes map[string]string
}

func (m *journeyMailbox) Send(_ context.Context, email, _, body string) error {
	match := regexp.MustCompile(`<strong>([0-9]{6})</strong>`).FindStringSubmatch(body)
	if len(match) != 2 {
		return errors.New("fixture email has no code")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[email] = match[1]
	return nil
}

type journeyClient struct {
	t                  *testing.T
	http               *http.Client
	base, token, state string
	actor              auth.Actor
}

func newJourneyClient(t *testing.T, base, state string) *journeyClient {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	c := &journeyClient{t: t, http: &http.Client{Transport: transport, Timeout: 10 * time.Second}, base: base, state: state}
	if data, err := os.ReadFile(filepath.Join(state, "login.json")); err == nil {
		var login auth.Login
		if err = json.Unmarshal(data, &login); err != nil {
			t.Fatal(err)
		}
		c.token, c.actor = login.Token, login.Actor
	}
	return c
}
func (c *journeyClient) call(method, path, key string, input, output any, status int) []byte {
	c.t.Helper()
	var data []byte
	var err error
	if input != nil {
		data, err = json.Marshal(input)
		if err != nil {
			c.t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.base+path, bytes.NewReader(data))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key == "" {
		key = uuid.NewString()
	}
	req.Header.Set("Idempotency-Key", key)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var raw json.RawMessage
	if res.StatusCode != http.StatusNoContent {
		if err = json.NewDecoder(res.Body).Decode(&raw); err != nil {
			c.t.Fatal(method, path, err)
		}
	}
	if res.StatusCode != status {
		c.t.Fatalf("%s %s: status=%d expected=%d", method, path, res.StatusCode, status)
	}
	if output != nil {
		if err = json.Unmarshal(raw, output); err != nil {
			c.t.Fatal(method, path, err)
		}
	}
	return raw
}
func (c *journeyClient) signIn(mail *journeyMailbox, email string) {
	c.t.Helper()
	c.call("POST", "/v1/auth/code", "", map[string]string{"email": email}, nil, 202)
	mail.mu.Lock()
	code := mail.codes[email]
	mail.mu.Unlock()
	if code == "" {
		c.t.Fatal("sign-in code was not delivered")
	}
	var login auth.Login
	body := map[string]string{"email": email, "code": code, "device_name": "Isolated test Mac"}
	c.call("POST", "/v1/auth/verify", "", body, &login, 200)
	if login.Token == "" || login.Actor.UserID == "" || login.Actor.DeviceID == "" {
		c.t.Fatal("incomplete login")
	}
	c.token, c.actor = login.Token, login.Actor
	data, err := json.Marshal(login)
	if err != nil {
		c.t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(c.state, "login.json"), data, 0600); err != nil {
		c.t.Fatal(err)
	}
	c.call("POST", "/v1/auth/verify", "", body, nil, 401)
}
func (c *journeyClient) replay(path string, after, until int64) []camp.Event {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+path, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Last-Event-ID", strconv.FormatInt(after, 10))
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		c.t.Fatal("event stream unavailable", res.StatusCode)
	}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	events := []camp.Event{}
	last := after
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event camp.Event
		if err = json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event); err != nil {
			c.t.Fatal(err)
		}
		if event.Cursor <= last {
			c.t.Fatal("event replay duplicated or reordered a cursor")
		}
		last = event.Cursor
		events = append(events, event)
		if last >= until {
			return events
		}
	}
	c.t.Fatal("event replay ended before expected cursor", scanner.Err())
	return nil
}

func TestTwoAccountsHTTPJourneyWithRestartAndRevocation(t *testing.T) {
	pool := testdb.Open(t)
	mail := &journeyMailbox{codes: map[string]string{}}
	start := func() *httptest.Server {
		secret := strings.Repeat("fixture", 8)
		api := &API{Pool: pool, Auth: auth.New(pool, secret, "https://camp.example", mail), Camps: camp.New(pool, secret), Origin: "https://camp.example"}
		server := httptest.NewServer(api.Handler())
		t.Cleanup(server.Close)
		return server
	}
	server := start()
	owner := newJourneyClient(t, server.URL, t.TempDir())
	member := newJourneyClient(t, server.URL, t.TempDir())
	owner.signIn(mail, "owner@example.com")
	member.signIn(mail, "member@example.com")
	if owner.actor.UserID == member.actor.UserID || owner.token == member.token || owner.actor.DeviceID == member.actor.DeviceID {
		t.Fatal("accounts are not independent")
	}
	var group camp.Camp
	owner.call("POST", "/v1/camps", "", map[string]string{"name": "Local collaboration"}, &group, 201)
	base := "/v1/camps/" + group.ID
	member.call("GET", base+"/snapshot", "", nil, nil, 404)
	var invite camp.Invitation
	owner.call("POST", base+"/invitations", "", map[string]string{"email": "member@example.com", "role": "editor"}, &invite, 201)
	owner.call("POST", "/v1/invitations/accept", "", map[string]string{"token": invite.Token}, nil, 404)
	member.call("POST", "/v1/invitations/accept", "", map[string]string{"token": invite.Token}, nil, 200)
	var snapshot camp.Snapshot
	member.call("GET", base+"/snapshot", "", nil, &snapshot, 200)
	if len(snapshot.Members) != 2 {
		t.Fatal("membership not synchronized")
	}
	var project camp.Project
	owner.call("POST", base+"/projects", "", map[string]any{"name": "Client project", "repositories": []string{}}, &project, 201)
	var profiles [2]camp.Profile
	for i, client := range []*journeyClient{owner, member} {
		client.call("POST", "/v1/profiles", "", camp.ProfileInput{LocalID: strings.Repeat("a", 64), Provider: "Codex", Label: "Same local label"}, &profiles[i], 201)
	}
	if profiles[0].ID == profiles[1].ID {
		t.Fatal("same profile label merged independent owners")
	}
	in := camp.SessionInput{ID: uuid.NewString(), ProjectID: project.ID, ProfileID: profiles[0].ID, LocalSessionID: uuid.NewString(), Title: "Shared fixture", State: "idle", Stage: "Ready", Shared: true, AllowRequests: true}
	var shared camp.Session
	path := base + "/sessions/" + in.ID
	owner.call("PUT", path, "", in, &shared, 200)
	member.call("GET", path, "", nil, nil, 200)
	private := in
	private.ID = uuid.NewString()
	private.LocalSessionID = uuid.NewString()
	private.Shared = false
	private.Title = "Private fixture"
	owner.call("PUT", base+"/sessions/"+private.ID, "", private, nil, 200)
	member.call("GET", base+"/sessions/"+private.ID, "", nil, nil, 404)
	// A second owner cannot publish a session against someone else's profile.
	spoof := in
	spoof.ID = uuid.NewString()
	spoof.LocalSessionID = uuid.NewString()
	member.call("PUT", base+"/sessions/"+spoof.ID, "", spoof, nil, 404)
	member.call("GET", base+"/snapshot", "", nil, &snapshot, 200)
	transcript := camp.TranscriptInput{Messages: []camp.MessageInput{{ID: "one", Ordinal: 0, Role: "assistant", Text: "Synthetic shared result"}}}
	owner.call("POST", path+"/transcript", "", transcript, nil, 200)
	owner.call("GET", base+"/snapshot", "", nil, &snapshot, 200)
	firstEvents := member.replay(base+"/events", 0, snapshot.Camp.Cursor)
	for _, event := range firstEvents {
		wire, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(wire, []byte(private.ID)) || bytes.Contains(wire, []byte(private.Title)) {
			t.Fatal("private session leaked through event replay")
		}
	}
	if len(firstEvents) == 0 {
		t.Fatal("no shared transcript event")
	}
	requestKey := uuid.NewString()
	prompt := camp.RequestInput{Prompt: "Synthetic follow-up", Model: "fixture-model", Speed: "standard"}
	var pending camp.Request
	member.call("POST", path+"/requests", requestKey, prompt, &pending, 201)
	if pending.State != "pending_approval" || pending.OwnerUserID != owner.actor.UserID || pending.RequesterUserID != member.actor.UserID {
		t.Fatal("cross-owner prompt was not held for approval")
	}
	actions := base + "/requests/" + pending.ID + "/actions"
	member.call("POST", actions, "", camp.RequestAction{Action: "approve"}, nil, 404)
	// Restart the API and reconstruct both client instances from separate saved logins.
	server.Close()
	server = start()
	owner = newJourneyClient(t, server.URL, owner.state)
	member = newJourneyClient(t, server.URL, member.state)
	var retry camp.Request
	member.call("POST", path+"/requests", requestKey, prompt, &retry, 201)
	if retry.ID != pending.ID || retry.State != "pending_approval" {
		t.Fatal("reconnect duplicated or approved a prompt")
	}
	changed := prompt
	changed.Prompt = "Changed payload"
	member.call("POST", path+"/requests", requestKey, changed, nil, 409)
	member.call("GET", path+"/transcript", "", nil, nil, 200)
	owner.call("POST", actions, "", camp.RequestAction{Action: "approve"}, &retry, 200)
	var wrongClaim *camp.Request
	member.call("POST", base+"/requests/claim", "", map[string]any{}, &wrongClaim, 200)
	if wrongClaim != nil {
		t.Fatal("member claimed owner's execution")
	}
	var claim camp.Request
	owner.call("POST", base+"/requests/claim", "", map[string]any{}, &claim, 200)
	if claim.ID != pending.ID || claim.LeaseToken == "" {
		t.Fatal("owner did not receive the queued request")
	}
	owner.call("POST", actions, "", camp.RequestAction{Action: "start", LeaseToken: claim.LeaseToken}, nil, 200)
	owner.call("POST", actions, "", camp.RequestAction{Action: "finish", LeaseToken: claim.LeaseToken, Outcome: "completed", Evidence: "Synthetic worker completion; no provider call"}, nil, 200)
	member.call("GET", base+"/requests/"+pending.ID, "", nil, &retry, 200)
	if retry.State != "completed" || retry.LeaseToken != "" {
		t.Fatal("completion missing or lease exposed")
	}
	member.call("POST", path+"/requests", requestKey, prompt, &retry, 201)
	if retry.ID != pending.ID || retry.State != "completed" {
		t.Fatal("late retry created another turn")
	}
	var requests []camp.Request
	member.call("GET", base+"/requests", "", nil, &requests, 200)
	if len(requests) != 1 {
		t.Fatal("duplicate request after reconnect")
	}
	owner.call("GET", base+"/snapshot", "", nil, &snapshot, 200)
	member.replay(base+"/events", firstEvents[len(firstEvents)-1].Cursor, snapshot.Camp.Cursor)
	owner.call("DELETE", base+"/members/"+member.actor.UserID, "", nil, nil, 204)
	for _, route := range []string{base + "/snapshot", path, path + "/transcript", base + "/requests/" + pending.ID, base + "/events"} {
		member.call("GET", route, "", nil, nil, 404)
	}
	member.call("POST", path+"/requests", requestKey, prompt, nil, 404)
	owner.call("GET", path, "", nil, nil, 200)
	member.call("POST", "/v1/auth/logout", "", nil, nil, 204)
	member.call("GET", "/v1/me", "", nil, nil, 401)
}
