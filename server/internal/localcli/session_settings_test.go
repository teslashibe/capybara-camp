package localcli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestProviderReportedEffortIgnoresDefaultsAndNextTurn(t *testing.T) {
	var codex settingsReader
	feed := func(raw string) {
		var d map[string]any
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatal(err)
		}
		codex.consume("Codex", d)
	}
	feed(`{"type":"event_msg","payload":{"thread_settings":{"model":"sol","reasoning_effort":"high","service_tier":"ultrafast"}}}`)
	feed(`{"type":"turn_context","payload":{"model":"sol","effort":"ultra"}}`)
	if codex.current.Effort != "ultra" || codex.current.Speed != "ultrafast" {
		t.Fatal(codex.current)
	}
	feed(`{"type":"event_msg","payload":{"thread_settings":{"model":"luna","reasoning_effort":"low","service_tier":"default"}}}`)
	if codex.current.Model != "sol" || codex.current.Effort != "ultra" {
		t.Fatal("next-turn settings replaced running effort", codex.current)
	}
	feed(`{"type":"turn_context","payload":{"model":"luna","effort":"low"}}`)
	if codex.current.Effort != "low" || codex.current.Speed != "standard" {
		t.Fatal(codex.current)
	}
	feed(`{"type":"turn_context","payload":{"model":"unknown"}}`)
	if codex.current.Effort != "" {
		t.Fatal("missing effort inherited previous turn")
	}
	root := t.TempDir()
	path := filepath.Join(root, "claude.jsonl")
	write(t, path, `{"type":"assistant","effort":"max","message":{"model":"claude-opus","usage":{"speed":"fast"}}}`+"\n"+`{"type":"assistant","isSidechain":true,"effort":"low","message":{"model":"child"}}`+"\n")
	actual := lastSettings(context.Background(), "Claude", path)
	if actual.Effort != "max" || actual.Model != "claude-opus" || actual.Speed != "fast" {
		t.Fatal(actual)
	}
	write(t, path, `{"type":"assistant","message":{"model":"claude-opus"}}`+"\n")
	if lastSettings(context.Background(), "Claude", path).Effort != "" {
		t.Fatal("unknown effort became a default")
	}
}
func TestClaudeLiveActivityReportsEffort(t *testing.T) {
	a := providerFixture(t, "reply")
	root := filepath.Join(a.Config.Home, ".claude")
	write(t, filepath.Join(root, "sessions/current.json"), `{}`)
	write(t, filepath.Join(root, "projects/p/parent.jsonl"), `{"type":"assistant","effort":"xhigh","message":{"model":"actual-opus"}}`+"\n")
	a.Config.Environment = append(a.Config.Environment, `CAMP_LOCAL_TEST_REPLY=[{"sessionId":"parent","status":"busy"}]`)
	result, err := a.Activity(context.Background(), ActivityRequest{Sources: []Source{sourceFor(root, "Claude")}})
	if err != nil || len(result.Activity) != 1 || result.Activity[0].Effort != "xhigh" || result.Activity[0].Model != "actual-opus" || result.Sessions[0].Effort != "xhigh" {
		t.Fatal(result, err)
	}
}
func TestClaudeTurnReportsActualEffortOverRequested(t *testing.T) {
	f := &turnFixture{events: []map[string]any{
		{"type": "system", "subtype": "init", "model": "claude-opus", "effort": "high"},
		{"type": "assistant", "effort": "max", "message": map[string]any{"model": "claude-opus", "usage": map[string]any{"speed": "standard"}}},
		{"type": "result", "is_error": false},
	}}
	events, emit := turnOutput()
	requireApprovalOK(t, claudeTurn(f, conversationRequest{Session: Session{SessionID: "original"}, Mode: "chat", Effort: "low", Speed: "fast"}, nil, emit))
	var actual map[string]any
	for _, e := range *events {
		if e["type"] == "settings" {
			actual = e
		}
	}
	if actual["effort"] != "max" || actual["speed"] != "standard" {
		t.Fatal(actual)
	}
}

func TestCodexRolloutCarriesReportedEffortAcrossOutput(t *testing.T) {
	r := rolloutIndex{}
	for _, raw := range []string{
		`{"type":"session_meta","payload":{"id":"parent"}}`,
		`{"type":"event_msg","payload":{"thread_settings":{"model":"sol","reasoning_effort":"low","service_tier":"ultrafast"}}}`,
		`{"type":"turn_context","payload":{"model":"sol","effort":"ultra"}}`,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"current"}}`,
		`{"type":"response_item","payload":{"content":[{"text":"effort=low is untrusted reply text"}]}}`,
	} {
		r.consume([]byte(raw))
	}
	if r.state != "running" || r.settings.current.Effort != "ultra" || r.settings.current.Speed != "ultrafast" {
		t.Fatal(r.state, r.settings.current)
	}
	r.consume([]byte(`{"type":"event_msg","payload":{"thread_settings":{"reasoning_effort":"medium","service_tier":"default"}}}`))
	if r.settings.current.Effort != "ultra" {
		t.Fatal("future settings replaced current run")
	}
}
