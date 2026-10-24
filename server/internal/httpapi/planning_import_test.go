package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func TestPlanningImportHTTPBoundsAndPrivateHistory(t *testing.T) {
	api, token, c, definition := planningHTTPFixture(t)
	input := camp.PlanningImportInput{SourceID: strings.Repeat("d", 64), Title: "Imported test", ProjectID: definition.ProjectID,
		Versions: []camp.ImportedPlanningVersion{{Version: 1, Mode: "single", State: "ready", Brief: "A saved plan", BudgetPolicy: "calls", Planners: definition.Planners[:1],
			Steps: []camp.ImportedPlanningStep{{Ordinal: 0, State: "completed", Output: "Saved result"}},
			LocalSources: []camp.ImportedPlanningSource{{ID: strings.Repeat("e", 64), Title: "Quoted local source", Revision: strings.Repeat("f", 64), Messages: []camp.ImportedPlanningMessage{
				{ID: "one", Role: "assistant", Text: strings.Repeat("a", 40<<10)}, {ID: "two", Role: "assistant", Text: strings.Repeat("b", 40<<10)},
			}}}}}}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	handler := api.Handler()
	path := "/v1/camps/" + c.ID + "/planning/import"
	send := func(path, token, key string, data []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if r := send(path, "", uuid.NewString(), data); r.Code != http.StatusUnauthorized {
		t.Fatal("Import skipped authentication", r.Code)
	}
	if r := send("/v1/camps/"+uuid.NewString()+"/planning/import", token, uuid.NewString(), data); r.Code != http.StatusNotFound {
		t.Fatal("Import skipped Camp authorization", r.Code)
	}
	if r := send(path, token, "", data); r.Code != http.StatusBadRequest {
		t.Fatal("Import accepted a missing idempotency key", r.Code)
	}
	key := uuid.NewString()
	created := send(path, token, key, data)
	if created.Code != http.StatusCreated {
		t.Fatal("Bounded import above the ordinary 64 KiB limit failed", created.Code, created.Body.String())
	}
	var plan camp.PlanningTicket
	if err = json.Unmarshal(created.Body.Bytes(), &plan); err != nil || plan.Shared || plan.Version.Imported == nil || plan.Version.Steps[0].RequestID != "" {
		t.Fatal("Imported history did not retain its private provenance", err)
	}
	for _, replayKey := range []string{key, uuid.NewString()} {
		response := send(path, token, replayKey, data)
		var replay camp.PlanningTicket
		if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &replay) != nil || replay.ID != plan.ID {
			t.Fatal("Import response loss duplicated a ticket", response.Code)
		}
	}
	unknown := append(append([]byte{}, data[:len(data)-1]...), []byte(`,"start":true}`)...)
	if r := send(path, token, uuid.NewString(), unknown); r.Code != http.StatusBadRequest {
		t.Fatal("Import accepted an execution directive", r.Code)
	}
	if r := send(path, token, uuid.NewString(), append(append([]byte{}, data...), []byte(`{}`)...)); r.Code != http.StatusBadRequest {
		t.Fatal("Import accepted trailing JSON", r.Code)
	}
	oversized := []byte(`{"source_id":"` + strings.Repeat("a", camp.MaxPlanningImportBytes) + `"}`)
	if r := send(path, token, uuid.NewString(), oversized); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("Import body limit was bypassed", r.Code)
	}
	if r := send("/v1/camps/"+c.ID+"/planning", token, uuid.NewString(), data); r.Code != http.StatusRequestEntityTooLarge && r.Code != http.StatusBadRequest {
		t.Fatal("Import changed ordinary planning admission", r.Code)
	}
}
