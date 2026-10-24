package localcli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriptSettingsBelongToTheRecordedMessage(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(a.Config.Home, "claude-settings.jsonl")
	write(t, path, strings.Join([]string{
		`{"type":"assistant","uuid":"first","effort":"high","message":{"model":"claude-first","usage":{"speed":"fast"},"content":[{"type":"text","text":"First answer"}]}}`,
		`{"type":"assistant","uuid":"second","message":{"model":"claude-second","content":[{"type":"text","text":"Second answer"}]}}`,
		`{"type":"user","uuid":"user","effort":"ultra","message":{"model":"not-an-assistant","content":[{"type":"text","text":"User text"}]}}`,
		`{"type":"assistant","uuid":"invalid","effort":"invented","message":{"model":"bad\nmodel","usage":{"speed":"turbo"},"content":[{"type":"text","text":"Unknown metadata"}]}}`,
	}, "\n")+"\n")
	s := Session{Provider: "Claude", Root: a.Config.Home, SessionID: "metadata", Path: path}
	transcript, err := a.Transcript(context.Background(), s, 120)
	if err != nil || len(transcript.Messages) != 4 {
		t.Fatal(transcript, err)
	}
	first, second := transcript.Messages[0], transcript.Messages[1]
	if first.Model != "claude-first" || first.Effort != "high" || first.Speed != "fast" || second.Model != "claude-second" || second.Effort != "" || second.Speed != "" {
		t.Fatal("Recorded settings changed or leaked between messages", transcript.Messages)
	}
	for _, m := range transcript.Messages[2:] {
		if m.Model != "" || m.Effort != "" || m.Speed != "" {
			t.Fatal("User or invalid metadata was presented as assistant settings", m)
		}
	}
	page, err := a.ExportPage(context.Background(), s, -1, 200)
	if err != nil || page.Messages[0].Effort != first.Effort || page.Messages[0].Speed != first.Speed || page.Messages[1].Effort != "" {
		t.Fatal("Shared transcript dropped or invented recorded settings", page, err)
	}
}

func TestCodexTranscriptIgnoresNextTurnSettings(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(a.Config.Home, "codex-settings.jsonl")
	write(t, path, strings.Join([]string{
		`{"type":"event_msg","payload":{"thread_settings":{"model":"first","reasoning_effort":"high","service_tier":"ultrafast"}}}`,
		`{"type":"turn_context","payload":{"model":"first","effort":"high"}}`,
		`{"type":"event_msg","payload":{"thread_settings":{"model":"next","reasoning_effort":"low","service_tier":"default"}}}`,
		`{"type":"response_item","payload":{"id":"first","type":"message","role":"assistant","content":[{"type":"output_text","text":"First answer"}]}}`,
		`{"type":"turn_context","payload":{"model":"next"}}`,
		`{"type":"response_item","payload":{"id":"next","type":"message","role":"assistant","content":[{"type":"output_text","text":"Next answer"}]}}`,
		`{"type":"response_item","payload":{"id":"user","type":"message","role":"user","content":[{"type":"input_text","text":"User text"}]}}`,
	}, "\n")+"\n")
	s := Session{Provider: "Codex", Root: a.Config.Home, SessionID: "metadata", Path: path}
	transcript, err := a.Transcript(context.Background(), s, 120)
	if err != nil || len(transcript.Messages) != 3 {
		t.Fatal(transcript, err)
	}
	first, next, user := transcript.Messages[0], transcript.Messages[1], transcript.Messages[2]
	if first.Model != "first" || first.Effort != "high" || first.Speed != "ultrafast" || next.Model != "next" || next.Effort != "" || next.Speed != "standard" || user.Model != "" || user.Effort != "" {
		t.Fatal("Future or missing settings were attributed to the wrong message", transcript.Messages)
	}
}

func TestCodexDatabaseHistoryDoesNotUseSessionDefaults(t *testing.T) {
	a := fixture(t)
	db := makeDB(t, filepath.Join(a.Config.Home, "thread_history_1.sqlite"), `CREATE TABLE thread_items(thread_id,item_id,item_json,item_type,rollout_ordinal)`)
	defer db.Close()
	for i, raw := range []string{
		`{"type":"agentMessage","text":"Recorded settings","model":"actual-model","effort":"high","service_tier":"priority"}`,
		`{"type":"agentMessage","text":"Unknown settings"}`,
	} {
		if _, err := db.Exec("INSERT INTO thread_items VALUES('thread',?,?, 'agentMessage',?)", i, raw, i); err != nil {
			t.Fatal(err)
		}
	}
	s := Session{Provider: "Codex", Root: a.Config.Home, SessionID: "thread", Model: "session-default", Effort: "ultra", Speed: "ultrafast"}
	transcript, err := a.Transcript(context.Background(), s, 120)
	if err != nil || len(transcript.Messages) != 2 {
		t.Fatal(transcript, err)
	}
	first, second := transcript.Messages[0], transcript.Messages[1]
	if first.Model != "actual-model" || first.Effort != "high" || first.Speed != "fast" || second.Model != "" || second.Effort != "" || second.Speed != "" {
		t.Fatal("Database messages inherited the session default", transcript.Messages)
	}
}
