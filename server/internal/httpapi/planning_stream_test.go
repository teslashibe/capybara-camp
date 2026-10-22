package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"net/http/httptest"
	"testing"
)

func TestPlanningStreamHTTPContract(t *testing.T) {
	api, token, c, input := planningHTTPFixture(t)
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
	base := "/v1/camps/" + c.ID
	input.Mode = "single"
	input.Planners = input.Planners[:1]
	input.BudgetPolicy = "calls"
	input.Start = true
	created := send("POST", base+"/planning", token, input)
	var plan camp.PlanningTicket
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &plan) != nil {
		t.Fatal(created.Code, created.Body.String())
	}
	claim := send("POST", base+"/planning-requests/claim", token, map[string]any{"session_ids": []string{input.Planners[0].SessionID}})
	var request camp.Request
	if claim.Code != 200 || json.Unmarshal(claim.Body.Bytes(), &request) != nil || request.ID == "" {
		t.Fatal(claim.Code, claim.Body.String())
	}
	path := base + "/requests/" + request.ID
	started := send("POST", path+"/actions", token, camp.RequestAction{Action: "start", LeaseToken: request.LeaseToken})
	if started.Code != 200 {
		t.Fatal(started.Code, started.Body.String())
	}
	event := camp.LocalPlanningEvent{StreamEvent: camp.StreamEvent{Seq: 1, Event: json.RawMessage(`{"type":"text","text":"Still writing"}`)}, LeaseToken: request.LeaseToken}
	if result := send("POST", path+"/planning-events", "", event); result.Code != 401 {
		t.Fatal("unauthenticated writer", result.Code)
	}
	for range 2 {
		if result := send("POST", path+"/planning-events", token, event); result.Code != 200 {
			t.Fatal(result.Code, result.Body.String())
		}
	}
	headPath := base + "/planning/" + plan.ID + "/steps/0/stream?version=1"
	if result := send("GET", headPath, "", nil); result.Code != 401 {
		t.Fatal("unauthenticated reader", result.Code)
	}
	var head camp.StreamHead
	result := send("GET", headPath, token, nil)
	if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &head) != nil || head.ID != request.ID || head.Seq != 1 {
		t.Fatal(result.Code, result.Body.String())
	}
	result = send("GET", base+"/planning/"+plan.ID+"/steps/5/stream?version=1", token, nil)
	if result.Code != 400 {
		t.Fatal("invalid ordinal", result.Code)
	}
	var page camp.StreamPage
	result = send("GET", path+"/events?after=0", token, nil)
	if result.Code != 200 || json.Unmarshal(result.Body.Bytes(), &page) != nil || len(page.Events) != 1 {
		t.Fatal(result.Code, result.Body.String())
	}
}
