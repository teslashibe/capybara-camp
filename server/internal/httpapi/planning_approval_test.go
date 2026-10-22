package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func TestPlanningApprovalHTTPReviewDoesNotExecute(t *testing.T) {
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
	client.call("POST", detail+"/cancel", "", map[string]int64{"expected_revision": approval.Revision}, nil, 204)
	client.call("GET", detail, "", nil, &approval, 200)
	if approval.State != "cancelled" {
		t.Fatal("cancellation missing")
	}
}
