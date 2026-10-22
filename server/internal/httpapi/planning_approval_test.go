package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func TestPlanningApprovalHTTPReviewAndImplementationDispatch(t *testing.T) {
	api, token, c, input := planningHTTPFixture(t)
	actor, err := api.Auth.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	input.BudgetPolicy = "calls"
	input.Mode = "single"
	input.Planners = input.Planners[:1]
	input.Start = true
	plan, err := api.Camps.CreatePlanning(t.Context(), actor, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := api.Camps.ClaimPlanningRequest(t.Context(), actor, c.ID, uuid.NewString(), []string{input.Planners[0].SessionID})
	if err != nil || request == nil {
		t.Fatal("missing planning request", err)
	}
	if _, err = api.Camps.ActOnRequest(t.Context(), actor, c.ID, request.ID, uuid.NewString(), camp.RequestAction{Action: "start", LeaseToken: request.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	if _, err = api.Camps.ActOnRequest(t.Context(), actor, c.ID, request.ID, uuid.NewString(), camp.RequestAction{Action: "finish", LeaseToken: request.LeaseToken, Outcome: "completed", Evidence: "Synthetic draft", Output: "Implement the synthetic fixture"}); err != nil {
		t.Fatal(err)
	}
	plan, err = api.Camps.Planning(t.Context(), actor, c.ID, plan.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	target, err := api.Camps.Session(t.Context(), actor, c.ID, input.Planners[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	in := camp.PlanningApprovalInput{ExpectedRevision: plan.Revision, ExpectedVersion: plan.ActiveVersion, ExpectedSessionRevision: target.Revision, ExpectedTranscriptRevision: target.TranscriptRevision, Executor: camp.PlannerInput{SessionID: target.ID, Model: "independent-executor", Speed: "standard", Execution: &camp.PlanningExecution{Location: "local", Epoch: target.ExecutionEpoch}}, Permissions: localcli.PermissionSettings{"sandbox": "workspace-write"}}
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	client := newJourneyClient(t, server.URL, t.TempDir())
	client.token = token
	path := "/v1/camps/" + c.ID + "/planning/" + plan.ID + "/execution-approvals"
	anonymous := newJourneyClient(t, server.URL, t.TempDir())
	anonymous.call("POST", path, "", in, nil, 401)
	var approval camp.PlanningApproval
	client.call("POST", path, "", in, &approval, 201)
	var statuses []camp.PlanningApprovalStatus
	client.call("GET", path, "", nil, &statuses, 200)
	if len(statuses) != 1 || statuses[0].ReviewRequired || statuses[0].State != "pending" {
		t.Fatal("review status")
	}
	detail := path + "/" + approval.ID
	client.call("GET", detail, "", nil, &approval, 200)
	client.call("POST", detail+"/approve", "", camp.PlanningApprovalAction{ExpectedRevision: 1}, nil, 400)
	client.call("POST", detail+"/approve", "", camp.PlanningApprovalAction{ExpectedRevision: 1, ExecutionContext: strings.Repeat("a", 64)}, &approval, 200)
	if approval.State != "approved" {
		t.Fatal("approval not saved")
	}
	var claimed *camp.Request
	client.call("POST", "/v1/camps/"+c.ID+"/requests/claim", "", map[string]any{"session_ids": []string{target.ID}, "include_planning": true}, &claimed, 200)
	if claimed != nil {
		t.Fatal("metadata approval started provider work")
	}
	var dispatched, replay camp.Request
	key := uuid.NewString()
	client.call("POST", detail+"/dispatch", key, map[string]int64{"expected_revision": approval.Revision}, &dispatched, 201)
	client.call("POST", detail+"/dispatch", key, map[string]int64{"expected_revision": approval.Revision}, &replay, 201)
	if dispatched.ID != replay.ID || dispatched.ImplementationID == "" || dispatched.Kind != "implementation" {
		t.Fatal("dispatch/replay")
	}
	client.call("POST", "/v1/camps/"+c.ID+"/requests/claim", "", map[string]any{"session_ids": []string{target.ID}, "include_planning": true}, &claimed, 200)
	if claimed != nil {
		t.Fatal("old worker admitted implementation")
	}
	client.call("POST", "/v1/camps/"+c.ID+"/requests/claim", "", map[string]any{"session_ids": []string{target.ID}, "include_implementation": true}, &claimed, 200)
	if claimed == nil || claimed.ID != dispatched.ID {
		t.Fatal("capable worker missing request")
	}
	var implementation camp.PlanningImplementation
	client.call("GET", "/v1/camps/"+c.ID+"/planning/"+plan.ID+"/implementation-runs/"+dispatched.ImplementationID, "", nil, &implementation, 200)
	if len(implementation.Requests) != 1 || implementation.Requests[0].LeaseToken != "" {
		t.Fatal("implementation read contract")
	}
	runsPath := "/v1/camps/" + c.ID + "/planning/" + plan.ID + "/implementation-runs"
	var runs camp.PlanningImplementationPage
	client.call("GET", runsPath+"?version=1", "", nil, &runs, 200)
	if len(runs.Implementations) != 1 || runs.Implementations[0].ID != dispatched.ImplementationID {
		t.Fatal("implementation discovery")
	}
	anonymous.call("GET", runsPath+"?version=1", "", nil, nil, 401)
	client.call("GET", runsPath+"?version=1&limit=21", "", nil, nil, 400)
	client.call("GET", runsPath+"?version=1&before=invalid", "", nil, nil, 400)
	client.call("GET", runsPath+"/"+dispatched.ImplementationID+"/requests?limit=20", "", nil, &implementation, 200)
	if len(implementation.Requests) != 1 || implementation.RequestsBefore != "" {
		t.Fatal("request history route")
	}
	requestPath := "/v1/camps/" + c.ID + "/requests/" + claimed.ID
	lease := claimed.LeaseToken
	client.call("POST", requestPath+"/actions", "", camp.RequestAction{Action: "start", LeaseToken: lease, ExecutionContext: strings.Repeat("a", 64)}, nil, 200)
	raw, _ := json.Marshal(map[string]any{"type": "session", "sessionID": uuid.NewString()})
	client.call("POST", requestPath+"/implementation-events", "", camp.LocalPlanningEvent{StreamEvent: camp.StreamEvent{Seq: 1, Event: raw}, LeaseToken: lease}, nil, 200)
	// Escaped output can exceed the ordinary JSON body limit while remaining
	// within the stream's per-event bound.
	raw, _ = json.Marshal(map[string]any{"type": "text", "text": strings.Repeat("\"", 40<<10)})
	client.call("POST", requestPath+"/implementation-events", "", camp.LocalPlanningEvent{StreamEvent: camp.StreamEvent{Seq: 2, Event: raw}, LeaseToken: lease}, nil, 200)
	id := uuid.NewString()
	client.call("POST", requestPath+"/controls", "", camp.ControlInput{ID: id, Control: map[string]any{"type": "steer", "id": id, "text": "Use the regression test"}}, nil, 200)
	anonymous.call("POST", requestPath+"/local-controls", "", map[string]string{"lease_token": lease}, nil, 401)
	client.call("POST", requestPath+"/local-controls", "", map[string]string{"lease_token": uuid.NewString()}, nil, 404)
	var controls []camp.RunnerControl
	client.call("POST", requestPath+"/local-controls", "", map[string]string{"lease_token": lease}, &controls, 200)
	if len(controls) != 1 || controls[0].ID != id {
		t.Fatal("local control transport")
	}
}
