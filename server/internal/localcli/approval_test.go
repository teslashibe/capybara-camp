package localcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type approvalFixture struct {
	sent   []map[string]any
	events []map[string]any
	fail   bool
}

func (f *approvalFixture) Write(value map[string]any) error {
	if f.fail {
		return errors.New("closed input")
	}
	copy, err := cloneObject(value)
	f.sent = append(f.sent, copy)
	return err
}

func (f *approvalFixture) RejectTool(value map[string]any) error {
	return f.Write(map[string]any{"id": value["id"], "error": map[string]any{"message": "unsupported"}})
}

func approvalTestRelay(provider string) (*approvalFixture, *approvalRelay) {
	f := &approvalFixture{}
	return f, newApprovalRelay(f, func(value map[string]any) error {
		copy, err := cloneObject(value)
		f.events = append(f.events, copy)
		return err
	}, provider, "session", "turn")
}

func approvalRequest(method string, id any, params map[string]any) map[string]any {
	p := map[string]any{"threadId": "session", "turnId": "turn", "itemId": "item"}
	for key, value := range params {
		p[key] = value
	}
	return map[string]any{"id": id, "method": "item/" + method, "params": p}
}

func requireApprovalOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestApprovalExplicitDecisionAndSingleConsumption(t *testing.T) {
	f, r := approvalTestRelay("Codex")
	request := approvalRequest("commandExecution/requestApproval", 1, map[string]any{"command": "echo example"})
	requireApprovalOK(t, r.Request(request))
	requireApprovalOK(t, r.Request(request))
	if len(f.events) != 1 || len(f.sent) != 0 {
		t.Fatal("Request auto-approved or duplicated", f)
	}
	key := str(f.events[0]["id"])
	requireApprovalOK(t, r.Resolve(map[string]any{"id": key, "allow": true, "decision": "acceptForSession"}))
	if len(f.sent) != 0 {
		t.Fatal("Unoffered session decision accepted")
	}
	requireApprovalOK(t, r.Resolve(map[string]any{"id": key, "allow": true}))
	requireApprovalOK(t, r.Resolve(map[string]any{"id": key, "allow": true}))
	if len(f.sent) != 1 || str(object(f.sent[0]["result"])["decision"]) != "accept" || len(r.pending) != 0 {
		t.Fatal(f, r.pending)
	}
}

func TestApprovalScopeCancellationAndUnsupportedRequests(t *testing.T) {
	f, r := approvalTestRelay("Codex")
	for _, params := range []map[string]any{{"threadId": "other"}, {"turnId": "other"}} {
		requireApprovalOK(t, r.Request(approvalRequest("commandExecution/requestApproval", 1, params)))
	}
	requireApprovalOK(t, r.Request(approvalRequest("unknown", 2, nil)))
	if len(f.sent) != 3 || len(r.pending) != 0 {
		t.Fatal("Foreign or unknown request admitted")
	}
	requireApprovalOK(t, r.Request(approvalRequest("commandExecution/requestApproval", "provider-id", nil)))
	key := str(f.events[len(f.events)-1]["id"])
	requireApprovalOK(t, r.Cancelled("provider-id"))
	requireApprovalOK(t, r.Resolve(map[string]any{"id": key, "allow": true}))
	if len(f.sent) != 3 || len(r.pending) != 0 {
		t.Fatal("Cancelled approval delivered")
	}
}

func TestApprovalFilePreviewBoundedAndReplaced(t *testing.T) {
	f, r := approvalTestRelay("Codex")
	requireApprovalOK(t, r.Request(approvalRequest("fileChange/requestApproval", 1, nil)))
	key := str(f.events[0]["id"])
	requireApprovalOK(t, r.Resolve(map[string]any{"id": key, "allow": true}))
	if f.events[0]["canApprove"] != false || len(f.sent) != 0 {
		t.Fatal("Unseen file changes approved")
	}
	requireApprovalOK(t, r.Resolve(map[string]any{"id": key, "allow": false}))
	r.Remember(map[string]any{"id": "item", "type": "fileChange", "changes": []any{map[string]any{"diff": "+example"}}})
	requireApprovalOK(t, r.Request(approvalRequest("fileChange/requestApproval", 2, nil)))
	if f.events[len(f.events)-1]["canApprove"] != true || !strings.Contains(str(f.events[len(f.events)-1]["detail"]), "+example") {
		t.Fatal("Complete diff missing")
	}
	r.Remember(map[string]any{"id": "item", "type": "fileChange", "changes": strings.Repeat("x", 70<<10)})
	requireApprovalOK(t, r.Request(approvalRequest("fileChange/requestApproval", 3, nil)))
	if f.events[len(f.events)-1]["canApprove"] != false {
		t.Fatal("Stale preview reused for oversized replacement")
	}
	for index := 0; index < 100; index++ {
		r.Remember(map[string]any{"id": fmt.Sprint(index), "type": "fileChange"})
	}
	if len(r.items) != 16 || len(r.itemOrder) != 16 {
		t.Fatal("File preview cache grew without bound")
	}
}

func TestApprovalExactTurnPermissionsAndOfferedRules(t *testing.T) {
	f, r := approvalTestRelay("Codex")
	permissions := map[string]any{"network": map[string]any{"enabled": true}}
	requireApprovalOK(t, r.Request(approvalRequest("permissions/requestApproval", 1, map[string]any{"permissions": permissions})))
	permissions["fileSystem"] = map[string]any{"write": []any{"/"}}
	requireApprovalOK(t, r.Resolve(map[string]any{"id": f.events[0]["id"], "allow": true, "permissions": permissions}))
	result := object(f.sent[0]["result"])
	if result["scope"] != "turn" || len(object(result["permissions"])) != 1 {
		t.Fatal("Approved permissions expanded", result)
	}
	for _, action := range []string{"allow", "deny"} {
		rule := map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"action": action, "host": "fixture.invalid"}}}
		requireApprovalOK(t, r.Request(approvalRequest("commandExecution/requestApproval", action, map[string]any{"availableDecisions": []any{rule}})))
		event := f.events[len(f.events)-1]
		decision := object(event["decisions"].([]any)[0])
		object(object(rule["applyNetworkPolicyAmendment"])["network_policy_amendment"])["host"] = "changed.invalid"
		requireApprovalOK(t, r.Resolve(map[string]any{"id": event["id"], "allow": action == "allow", "decision": decision["id"]}))
		data, _ := json.Marshal(f.sent[len(f.sent)-1])
		if strings.Contains(string(data), "changed.invalid") || !strings.Contains(string(data), "fixture.invalid") {
			t.Fatal("Offered rule mutated", string(data))
		}
	}
}

func TestApprovalQuestionValidation(t *testing.T) {
	f, r := approvalTestRelay("Codex")
	requireApprovalOK(t, r.Request(approvalRequest("tool/requestUserInput", 1, map[string]any{"questions": []any{
		map[string]any{"id": "db", "question": "Database?", "options": []any{map[string]any{"label": "Postgres"}}},
	}})))
	key := f.events[0]["id"]
	for _, answers := range []any{nil, map[string]any{}, map[string]any{"db": []any{"other"}}, map[string]any{"db": []any{"Postgres"}, "extra": []any{"x"}}, map[string]any{"db": []any{true}}, map[string]any{"db": []any{strings.Repeat("x", 4097)}}} {
		requireApprovalOK(t, r.Resolve(map[string]any{"id": key, "allow": true, "answers": answers}))
	}
	if len(f.sent) != 0 {
		t.Fatal("Invalid answers sent")
	}
	requireApprovalOK(t, r.Resolve(map[string]any{"id": key, "allow": true, "answers": map[string]any{"db": []any{"Postgres"}}}))
	if object(object(f.sent[0]["result"])["answers"])["db"] == nil {
		t.Fatal("Original question ID lost")
	}
}

func TestApprovalClaudeInputAndQuestionMapping(t *testing.T) {
	f, r := approvalTestRelay("Claude")
	input := map[string]any{"file_path": "/tmp/fixture", "content": "example"}
	request := map[string]any{"type": "control_request", "request_id": "one", "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Write", "input": input}}
	requireApprovalOK(t, r.Request(request))
	input["file_path"] = "/changed"
	requireApprovalOK(t, r.Resolve(map[string]any{"id": f.events[0]["id"], "allow": true}))
	result := object(object(f.sent[0]["response"])["response"])
	if result["behavior"] != "allow" || len(result) != 2 || object(result["updatedInput"])["file_path"] != "/tmp/fixture" {
		t.Fatal("Claude input or rules expanded", result)
	}
	question := map[string]any{"subtype": "can_use_tool", "tool_name": "AskUserQuestion", "input": map[string]any{"questions": []any{map[string]any{"question": "What next?", "options": []any{map[string]any{"label": "Plan"}}}}}}
	requireApprovalOK(t, r.Request(map[string]any{"request_id": "two", "request": question}))
	requireApprovalOK(t, r.Resolve(map[string]any{"id": f.events[len(f.events)-1]["id"], "allow": true, "answers": map[string]any{"0": []any{"Custom response"}}}))
	result = object(object(f.sent[1]["response"])["response"])
	if object(object(result["updatedInput"])["answers"])["What next?"] != "Custom response" {
		t.Fatal("Claude other answer lost", result)
	}
}

func TestApprovalBoundsMalformedRequestsAndFailedWrite(t *testing.T) {
	f, r := approvalTestRelay("Codex")
	for _, params := range []map[string]any{
		{"command": strings.Repeat("x", 70<<10)},
		{"availableDecisions": "accept"},
		{"availableDecisions": make([]any, 17)},
	} {
		requireApprovalOK(t, r.Request(approvalRequest("commandExecution/requestApproval", len(f.sent), params)))
	}
	for _, questions := range []any{nil, "wrong", []any{map[string]any{"id": "same", "question": "One"}, map[string]any{"id": "same", "question": "Two"}}, []any{map[string]any{"question": "One", "options": []any{false}}}} {
		requireApprovalOK(t, r.Request(approvalRequest("tool/requestUserInput", len(f.sent), map[string]any{"questions": questions})))
	}
	if len(r.pending) != 0 || len(f.sent) != 7 {
		t.Fatal("Malformed requests admitted", f)
	}
	for index := 0; index < 17; index++ {
		requireApprovalOK(t, r.Request(approvalRequest("commandExecution/requestApproval", index+100, nil)))
	}
	if len(r.pending) != 16 || len(f.sent) != 8 {
		t.Fatal("Pending approvals unbounded")
	}
	var key string
	for key = range r.pending {
		break
	}
	f.fail = true
	if r.Resolve(map[string]any{"id": key, "allow": true}) == nil || len(r.pending) != 16 || f.events[len(f.events)-1]["type"] == "approvalResolved" {
		t.Fatal("Failed write reported success")
	}
}

func TestApprovalMatchesNativePreviewLimits(t *testing.T) {
	for _, question := range []map[string]any{
		{"question": strings.Repeat("x", 8193)},
		{"question": "Pick", "options": make([]any, 17)},
	} {
		f, r := approvalTestRelay("Codex")
		requireApprovalOK(t, r.Request(approvalRequest("tool/requestUserInput", 1, map[string]any{"questions": []any{question}})))
		if len(r.pending) != 0 || len(f.sent) != 1 {
			t.Fatal("Unrenderable approval admitted")
		}
	}
	f, r := approvalTestRelay("Claude")
	requireApprovalOK(t, r.Request(map[string]any{"request_id": "long", "request": map[string]any{"subtype": "can_use_tool", "tool_name": strings.Repeat("x", 513), "input": map[string]any{}}}))
	if len(r.pending) != 0 || len(f.sent) != 1 {
		t.Fatal("Unrenderable Claude title admitted")
	}
	requireApprovalOK(t, r.Request(map[string]any{
		"request_id": "duplicate",
		"request": map[string]any{
			"subtype": "can_use_tool", "tool_name": "AskUserQuestion",
			"input": map[string]any{"questions": []any{map[string]any{"question": "Same"}, map[string]any{"question": "Same"}}},
		},
	}))
	if len(r.pending) != 0 || len(f.sent) != 2 {
		t.Fatal("Claude answer keys would overwrite each other")
	}
}
