package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

func TestAttachmentHTTPPublicationReadsAndBounds(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Open(t)
	store, err := camp.NewWithProviderKey(pool, "fixture-receipt-key", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Pool: pool, Auth: auth.New(pool, strings.Repeat("s", 32), "https://camp.example", unusedMail{}), Camps: store, Origin: "https://camp.example"}
	actor := auth.Actor{UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: "attachment@example.test", Name: "Owner"}
	token, err := auth.Token()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, actor.UserID, actor.Email, actor.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Fixture',$3,now()+interval '1 day')`, actor.DeviceID, actor.UserID, auth.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	c, err := store.Create(ctx, actor, uuid.NewString(), "Camp")
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreateProject(ctx, actor, c.ID, uuid.NewString(), "Project")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.RegisterProfile(ctx, actor, uuid.NewString(), camp.ProfileInput{LocalID: strings.Repeat("a", 64), Provider: "Claude", Label: "Fixture"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.PublishSession(ctx, actor, c.ID, uuid.NewString(), camp.SessionInput{ID: uuid.NewString(), ProjectID: p.ID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: "Images", State: "idle", Stage: "Ready"})
	if err != nil {
		t.Fatal(err)
	}
	handler := api.Handler()
	base := "/v1/camps/" + c.ID + "/sessions/" + session.ID
	call := func(method, path, bearer string, body io.Reader, status int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, body)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", uuid.NewString())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s: %d wanted %d: %s", path, w.Code, status, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("Attachment response is cacheable")
		}
		return w.Body.Bytes()
	}
	body := bytes.Repeat([]byte{0x5a}, (2<<20)+1)
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	raw, _ := json.Marshal(camp.AttachmentUpload{Data: body})
	path := base + "/attachments/" + hash
	call("PUT", path, "", bytes.NewReader(raw), 401)
	first := call("PUT", path, token, bytes.NewReader(raw), 200)
	if !bytes.Equal(first, call("PUT", path, token, bytes.NewReader(raw), 200)) {
		t.Fatal("Retry changed receipt")
	}
	ref := camp.AttachmentReference{ID: "a:0", Kind: "image", Source: "inline", SourceDigest: strings.Repeat("b", 64), State: "embedded", MediaType: "image/png", SHA256: hash, Size: len(body)}
	read, _ := json.Marshal(camp.AttachmentRead{Revision: 1, MessageID: "image", AttachmentID: ref.ID, SourceDigest: ref.SourceDigest})
	call("POST", base+"/attachment", token, bytes.NewReader(read), 409)
	message, _ := json.Marshal(camp.TranscriptInput{Messages: []camp.MessageInput{{ID: "image", Ordinal: 0, Role: "user", Attachments: []camp.AttachmentReference{ref}}}})
	call("POST", base+"/transcript", token, bytes.NewReader(message), 200)
	response := call("POST", base+"/attachment", token, bytes.NewReader(read), 200)
	var result camp.AttachmentBody
	if err = json.Unmarshal(response, &result); err != nil || !bytes.Equal(result.Data, body) || result.Attachment != ref {
		t.Fatal("Byte response changed", err)
	}
	call("PUT", path, token, strings.NewReader(`{"data":"eA==","url":"https://example.test"}`), 400)
	call("PUT", path, token, strings.NewReader(`{"data":"eA=="} {}`), 400)
	// Streaming a large body exercises MaxBytesReader without allocating a fixture.
	oversize := io.MultiReader(strings.NewReader(`{"data":"`), io.LimitReader(repeatedA{}, 24<<20), strings.NewReader(`"}`))
	call("PUT", path, token, oversize, 413)
	if err = api.Auth.Revoke(ctx, actor); err != nil {
		t.Fatal(err)
	}
	call("POST", base+"/attachment", token, bytes.NewReader(read), 401)
}

type repeatedA struct{}

func (repeatedA) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'A'
	}
	return len(p), nil
}
