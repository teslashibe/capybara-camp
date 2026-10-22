package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	memorydb "github.com/teslashibe/capybara-camp/server/internal/memory"
)

func TestMemoryObservationHTTPAndReadiness(t *testing.T) {
	api, token, c, planning := planningHTTPFixture(t)
	var parent string
	if err := api.Pool.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	schema := memorydb.SchemaName(parent)
	t.Cleanup(func() {
		if _, err := api.Pool.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	if err := memorydb.Migrate(t.Context(), api.Pool); err != nil {
		t.Fatal(err)
	}
	if err := memorydb.Migrate(t.Context(), api.Pool); err != nil {
		t.Fatal("migration not replayable", err)
	}
	memories, err := memorydb.Open(t.Context(), api.Pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(memories.Close)
	api.Camps.SetMemoryStore(memories)
	owner, err := api.Auth.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	session, err := api.Camps.Session(t.Context(), owner, c.ID, planning.Planners[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.Camps.WriteTranscript(t.Context(), owner, c.ID, session.ID, uuid.NewString(), camp.TranscriptInput{Messages: []camp.MessageInput{{ID: "source", Ordinal: 0, Role: "assistant", Text: "Private observation content"}}}); err != nil {
		t.Fatal(err)
	}
	input := camp.MemoryObservationInput{ProjectID: planning.ProjectID, Visibility: "private", Source: camp.SourceRef{SessionID: session.ID, TranscriptRevision: 1, PermissionRevision: session.PermissionRevision, MessageIDs: []string{"source"}}}
	handler := api.Handler()
	send := func(method, path, bearer string, body any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", uuid.NewString())
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/v1/camps/" + c.ID + "/memory/observations"
	if r := send("POST", path, "", input); r.Code != 401 {
		t.Fatal("missing authentication", r.Code)
	}
	data, _ := json.Marshal(input)
	var extra map[string]any
	_ = json.Unmarshal(data, &extra)
	extra["user_id"] = uuid.NewString()
	if r := send("POST", path, token, extra); r.Code != 400 {
		t.Fatal("client-selected namespace accepted", r.Code)
	}
	created := send("POST", path, token, input)
	var value camp.MemoryObservation
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &value) != nil || !value.Stored {
		t.Fatal(created.Code, created.Body.String())
	}
	listed := send("GET", path+"?visibility=private&project_id="+planning.ProjectID, token, nil)
	if listed.Code != 200 || strings.Contains(listed.Body.String(), "Private observation content") {
		t.Fatal("list copied full content", listed.Code, listed.Body.String())
	}
	read := send("GET", path+"/"+value.ID, token, nil)
	if read.Code != 200 || !strings.Contains(read.Body.String(), "Private observation content") {
		t.Fatal(read.Code, read.Body.String())
	}
	if r := send("GET", path+"?visibility=private&project_id="+planning.ProjectID+"&limit=101", token, nil); r.Code != 400 {
		t.Fatal("unbounded page", r.Code)
	}
	jobPath := "/v1/camps/" + c.ID + "/memory/jobs"
	jobInput := camp.MemoryJobInput{ProjectID: planning.ProjectID, Visibility: "private", Target: camp.MemoryTarget{Location: "local", ProfileID: session.ProfileID}, Model: "fixture-model", ObservationIDs: []string{value.ID}}
	jobResult := send("POST", jobPath, token, jobInput)
	var job camp.MemoryJob
	if jobResult.Code != 201 || json.Unmarshal(jobResult.Body.Bytes(), &job) != nil || job.State != "queued" {
		t.Fatal("job create", jobResult.Code, jobResult.Body.String())
	}
	if r := send("GET", jobPath+"/"+job.ID, token, nil); r.Code != 200 {
		t.Fatal("job read", r.Code)
	}
	if r := send("GET", jobPath+"?visibility=private&project_id="+planning.ProjectID, token, nil); r.Code != 200 || !strings.Contains(r.Body.String(), job.ID) {
		t.Fatal("job list", r.Code, r.Body.String())
	}
	if r := send("POST", jobPath+"/"+job.ID+"/cancel", token, map[string]any{"expected_revision": 1}); r.Code != 200 || !strings.Contains(r.Body.String(), `"state":"cancelled"`) {
		t.Fatal("job cancel", r.Code, r.Body.String())
	}
	if r := send("GET", "/v1/camps/"+c.ID+"/memory/memories?visibility=private&project_id="+planning.ProjectID, token, nil); r.Code != 200 || !strings.Contains(r.Body.String(), `"memories":[]`) {
		t.Fatal("memory list", r.Code, r.Body.String())
	}
	if r := send("GET", "/ready", token, nil); r.Code != 200 {
		t.Fatal("ready schema rejected", r.Code)
	}
	if _, err = api.Pool.Exec(t.Context(), "DELETE FROM "+pgx.Identifier{schema, "schema_version"}.Sanitize()+" WHERE version=(SELECT max(version) FROM "+pgx.Identifier{schema, "schema_version"}.Sanitize()+")"); err != nil {
		t.Fatal(err)
	}
	if r := send("GET", "/ready", token, nil); r.Code != 503 {
		t.Fatal("partial memory schema accepted", r.Code)
	}
}
