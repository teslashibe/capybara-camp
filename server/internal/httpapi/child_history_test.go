package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

func TestChildHistoryHTTPAuthorizationReplayAndRevocation(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Open(t)
	store := camp.New(pool, "fixture-receipt-key")
	a := &API{Pool: pool, Auth: auth.New(pool, strings.Repeat("s", 32), "https://camp.example", unusedMail{}), Camps: store, Origin: "https://camp.example"}
	actors := []auth.Actor{{UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: "owner@example.com", Name: "Owner"}, {UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: "reader@example.com", Name: "Reader"}}
	tokens := make([]string, 2)
	for i, u := range actors {
		var err error
		tokens[i], err = auth.Token()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, u.UserID, u.Email, u.Name); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Test',$3,now()+interval '1 hour')`, u.DeviceID, u.UserID, auth.HashToken(tokens[i])); err != nil {
			t.Fatal(err)
		}
	}
	c, err := store.Create(ctx, actors[0], uuid.NewString(), "Camp")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.Invite(ctx, actors[0], c.ID, uuid.NewString(), actors[1].Email, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Accept(ctx, actors[1], uuid.NewString(), invite.Token); err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, actors[0], c.ID, uuid.NewString(), "Project")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.RegisterProfile(ctx, actors[0], uuid.NewString(), camp.ProfileInput{LocalID: strings.Repeat("a", 64), Provider: "Claude", Label: "Claude"})
	if err != nil {
		t.Fatal(err)
	}
	input := camp.SessionInput{ID: uuid.NewString(), ProjectID: project.ID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: "Parent", State: "idle", Stage: "Ready", Shared: true}
	parent, err := store.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	handler := a.Handler()
	call := func(method, path, token, key string, body []byte, status int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s: %d, wanted %d: %s", method, path, w.Code, status, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("Missing private response header")
		}
		return w.Body.Bytes()
	}
	id, key := uuid.NewString(), uuid.NewString()
	path := "/v1/camps/" + c.ID + "/sessions/" + parent.ID + "/children/" + id + "/transcript"
	write := camp.ChildHistoryInput{SourceKey: strings.Repeat("b", 64), SourceRevision: strings.Repeat("c", 64), Name: "Child", State: "done", Complete: true, Messages: []camp.MessageInput{{ID: "m", Ordinal: 0, Role: "assistant", Text: strings.Repeat("x", 80000)}}}
	raw, _ := json.Marshal(write)
	call("POST", path, "", key, raw, 401)
	call("POST", path, tokens[1], key, raw, 404)
	first := call("POST", path, tokens[0], key, raw, 200)
	repeated := call("POST", path, tokens[0], key, raw, 200)
	if !bytes.Equal(first, repeated) {
		t.Fatal("HTTP retry changed its receipt")
	}
	call("GET", path, tokens[1], "", nil, 200)
	call("POST", strings.TrimSuffix(path, "/transcript")+"/message", tokens[1], uuid.NewString(), []byte(`{"revision":1,"message_id":"m"}`), 200)
	call("GET", "/v1/camps/"+c.ID+"/sessions/"+parent.ID+"/children", tokens[1], "", nil, 200)
	call("GET", "/v1/camps/"+c.ID+"/sessions/"+parent.ID+"/children/publication", tokens[1], "", nil, 404)
	call("GET", "/v1/camps/"+c.ID+"/sessions/"+parent.ID+"/children/publication", tokens[0], "", nil, 200)
	call("GET", "/v1/camps/"+c.ID+"/sessions/"+id, tokens[0], "", nil, 404)
	call("POST", path, tokens[0], uuid.NewString(), []byte(`{"unknown":true}`), 400)
	call("GET", path+"?before=-1", tokens[0], "", nil, 400)
	input.ExpectedRevision = parent.Revision
	input.Shared = false
	if _, err = store.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	call("GET", path, tokens[1], "", nil, 404)
}

func TestChildHistoryLimitHasItsOwnProblemCode(t *testing.T) {
	recorder := httptest.NewRecorder()
	if !fail(recorder, camp.ErrChildCapacity) {
		t.Fatal("Child limit was not handled")
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || recorder.Code != 409 || body["error"] != "child_history_limit" {
		t.Fatal("Child limit looks like a retryable conflict", recorder.Code, recorder.Body.String(), err)
	}
}
