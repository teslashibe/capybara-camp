package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func TestTransferHTTPBoundsAndOwnerAuthorization(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	store, err := camp.NewWithProviderKey(pool, "fixture-receipt-key", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	a := &API{Pool: pool, Auth: auth.New(pool, strings.Repeat("s", 32), "https://camp.example", unusedMail{}), Camps: store, Origin: "https://camp.example"}
	actors := []auth.Actor{{UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: "owner@example.com", Name: "Owner"}, {UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: "member@example.com", Name: "Member"}}
	tokens := make([]string, len(actors))
	for i, u := range actors {
		tokens[i], err = auth.Token()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, u.UserID, u.Email, u.Name); err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Test Mac',$3,now()+interval '1 day')`, u.DeviceID, u.UserID, auth.HashToken(tokens[i])); err != nil {
			t.Fatal(err)
		}
	}
	owner := actors[0]
	c, err := store.Create(ctx, owner, uuid.NewString(), "Camp")
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := store.Invite(ctx, owner, c.ID, uuid.NewString(), actors[1].Email, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Accept(ctx, actors[1], uuid.NewString(), invitation.Token); err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, owner, c.ID, uuid.NewString(), "Project")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.RegisterProfile(ctx, owner, uuid.NewString(), camp.ProfileInput{LocalID: strings.Repeat("a", 64), Provider: "Codex", Label: "Codex"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.PublishSession(ctx, owner, c.ID, uuid.NewString(), camp.SessionInput{ID: uuid.NewString(), ProjectID: project.ID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: "Session", State: "idle", Stage: "Ready", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	encode := func(value any) []byte {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	digest := func(value []byte) string {
		sum := sha256.Sum256(value)
		return hex.EncodeToString(sum[:])
	}
	call := func(method, path, token string, body []byte, expected int) []byte {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", uuid.NewString())
		if len(body) > 0 {
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
		if res.StatusCode != expected {
			t.Fatalf("%s %s: %d, expected %d: %s", method, path, res.StatusCode, expected, data)
		}
		if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing private response headers")
		}
		return data
	}
	file := bytes.Repeat([]byte("source\n"), 40000)
	m := transfer.Manifest{Version: 1, BaseCommit: strings.Repeat("a", 40), Files: []transfer.File{{Path: "src/main.txt", Data: file, SHA256: digest(file)}}}
	m.History = &transfer.HistoryBinding{Version: 1, ParentID: strings.Repeat("c", 64), MainRevision: strings.Repeat("d", 64), Children: []transfer.HistoryChild{}, TranscriptRevision: session.TranscriptRevision}
	raw := encode(m)
	review, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	input := camp.TransferInput{TargetLocation: "local", ExpectedEpoch: session.ExecutionEpoch, ExpectedRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision, ExpectedPermissionRevision: session.PermissionRevision, PayloadSHA256: digest(raw), ReviewSHA256: review, PayloadBytes: len(raw)}
	base := "/v1/camps/" + c.ID + "/sessions/" + session.ID + "/transfers"
	call("POST", base, "", encode(input), 401)
	call("POST", base, tokens[1], encode(input), 404)
	var value camp.Transfer
	if err = json.Unmarshal(call("POST", base, tokens[0], encode(input), 201), &value); err != nil {
		t.Fatal(err)
	}
	path := base + "/" + value.ID
	call("GET", path, tokens[1], nil, 404)
	call("GET", base, tokens[0], nil, 200)
	call("GET", path+"/chunks/0", tokens[0], nil, 409)
	call("PUT", path+"/chunks/nope", tokens[0], []byte(`{}`), 400)
	call("PUT", path+"/chunks/-1", tokens[0], []byte(`{}`), 400)
	oversized := bytes.Repeat([]byte("x"), 384<<10)
	call("PUT", path+"/chunks/0", tokens[0], encode(camp.TransferChunk{Data: oversized, SHA256: digest(oversized)}), 413)
	call("POST", path+"/complete", tokens[0], []byte(`{"unknown":true}`), 400)
	call("POST", path+"/complete", tokens[0], []byte(`{}`), 409)
	for offset, index := 0, 0; offset < len(raw); offset, index = offset+camp.TransferChunkBytes, index+1 {
		part := raw[offset:min(offset+camp.TransferChunkBytes, len(raw))]
		call("PUT", path+fmt.Sprintf("/chunks/%d", index), tokens[0], encode(camp.TransferChunk{Data: part, SHA256: digest(part)}), 200)
	}
	call("POST", path+"/complete", tokens[0], []byte(`{}`), 200)
	call("GET", path+"/chunks/0", tokens[1], nil, 404)
	var chunk camp.TransferChunk
	if err = json.Unmarshal(call("GET", path+"/chunks/0", tokens[0], nil, 200), &chunk); err != nil || !bytes.Equal(chunk.Data, raw[:camp.TransferChunkBytes]) {
		t.Fatal("chunk response", err)
	}
	call("POST", path+"/cancel", tokens[0], []byte(`{} {}`), 400)
	call("POST", path+"/cancel", tokens[0], []byte(`{}`), 200)
	call("GET", path+"/chunks/0", tokens[0], nil, 409)
}
