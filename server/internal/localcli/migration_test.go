package localcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLegacyImportDefaultsWithoutTypedHeader(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, ".codex")
	for _, folder := range []string{"sessions", "archived_sessions"} {
		write(t, filepath.Join(root, folder, "one.jsonl"), `{"payload":{"id":"`+folder+`","cwd":"/tmp"}}`+"\n")
	}
	values, err := codexSessions(context.Background(), sourceFor(root, "Codex"))
	if err != nil || len(values) != 2 {
		t.Fatal(values, err)
	}
	for _, value := range values {
		if value.AccountID != "" || value.Model != "" || value.Archived != (value.SessionID == "archived_sessions") {
			t.Fatal(value)
		}
	}
}

func TestCustomSourceRejectsInvalidBeforeWritingAndDeduplicates(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, "any-profile-name")
	if err := a.AddSource(context.Background(), root); err == nil {
		t.Fatal("Missing source accepted")
	}
	if _, err := os.Stat(filepath.Join(a.Config.Support, "sources.json")); !os.IsNotExist(err) {
		t.Fatal("Invalid source persisted", err)
	}
	write(t, filepath.Join(root, "sessions", "one.jsonl"), `{"payload":{"id":"one","cwd":"/tmp"}}`+"\n")
	if err := a.AddSource(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := a.AddSource(context.Background(), filepath.Join(root, ".")); err != nil {
		t.Fatal(err)
	}
	var paths []string
	if err := readJSON(filepath.Join(a.Config.Support, "sources.json"), &paths); err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	values, err := a.Scan(context.Background())
	if err != nil || len(values.Sources) != 1 || len(values.Sessions) != 1 {
		t.Fatal(values, err)
	}
}

func TestNewestDuplicateSessionKeepsOneCount(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, ".claude")
	for i, name := range []string{"new", "old"} {
		path := filepath.Join(root, "projects", name, "one.jsonl")
		write(t, path, `{"type":"user","sessionId":"same","cwd":"/tmp","message":{"content":"`+name+`"}}`+"\n")
		stamp := time.Unix(int64(10-i), 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	values, err := a.Scan(context.Background())
	if err != nil || len(values.Sessions) != 1 || values.Sessions[0].Title != "new" || values.Sources[0].Count != 1 {
		t.Fatal(values, err)
	}
}

func TestSubagentTranscriptReadableButNeverResumed(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(a.Config.Home, "agent-child.jsonl")
	write(t, path, `{"type":"assistant","isSidechain":true,"uuid":"child","message":{"content":"Reviewing"}}`+"\n")
	session := Session{Provider: "Claude", Root: a.Config.Home, Path: path, SessionID: "child", IsSubagent: true}
	page, err := a.Transcript(context.Background(), session, 120)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "Reviewing" {
		t.Fatal(page, err)
	}
	if err := a.assertAvailable(context.Background(), session); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatal(err)
	}
}

func TestAllClaudePermissionModesAndSelectedModelsReachCommands(t *testing.T) {
	for _, mode := range []string{"manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"} {
		s := Session{Provider: "Claude", SessionID: "00000000-0000-0000-0000-000000000001"}
		base, err := runtimeCommand(s, "sonnet", "", "")
		if err != nil {
			t.Fatal(err)
		}
		command, err := executionCommand(s, base, false, PermissionSettings{"claudeMode": mode})
		index := slices.Index(command, "--permission-mode")
		if err != nil || index < 0 || command[index+1] != mode || slices.Contains(command, "--dangerously-skip-permissions") {
			t.Fatal(command, err)
		}
		index = slices.Index(command, "--model")
		if index < 0 || command[index+1] != "sonnet" {
			t.Fatal(command)
		}
	}
	command, err := runtimeCommand(Session{Provider: "Codex", SessionID: "one"}, "gpt-fixture", "", "")
	index := slices.Index(command, "--model")
	if err != nil || index < 0 || command[index+1] != "gpt-fixture" {
		t.Fatal(command, err)
	}
}

func TestOfferedSessionDecisionIsConsumedOnlyOnce(t *testing.T) {
	f, r := approvalTestRelay("Codex")
	if err := r.Request(approvalRequest("commandExecution/requestApproval", 1, map[string]any{"availableDecisions": []any{"acceptForSession", "cancel"}})); err != nil {
		t.Fatal(err)
	}
	key := f.events[0]["id"]
	for _, decision := range []string{"", "accept"} {
		if err := r.Resolve(map[string]any{"id": key, "allow": true, "decision": decision}); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sent) != 0 {
		t.Fatal("Unoffered decision was sent")
	}
	for i := 0; i < 2; i++ {
		if err := r.Resolve(map[string]any{"id": key, "allow": true, "decision": "acceptForSession"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sent) != 1 || object(f.sent[0]["result"])["decision"] != "acceptForSession" {
		t.Fatal(f.sent)
	}
}

func TestRolloutIgnoresOldCompletionAndTextMarkers(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(a.Config.Home, "sessions", "one.jsonl")
	write(t, path, `{"type":"session_meta","payload":{"id":"one"}}`+"\n"+`{"type":"event_msg","payload":{"type":"task_started","turn_id":"old"}}`+"\n")
	info, _ := os.Stat(path)
	stat := info.Sys().(*syscall.Stat_t)
	writer := rolloutWriter{Path: path, Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}
	index := &rolloutIndex{path: path}
	if state, _, err := index.update(context.Background(), writer, 1<<20); err != nil || state != "running" {
		t.Fatal(state, err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"new"}}` + "\n" + `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"old"}}` + "\n")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if state, _, err := index.update(context.Background(), writer, 1<<20); err != nil || state != "running" {
		t.Fatal(state, err)
	}
	write(t, path, `{"type":"response_item","payload":{"text":"task_started"}}`+"\n"+`["task_started"]`+"\n")
	index = &rolloutIndex{path: path}
	if state, _, err := index.update(context.Background(), writer, 1<<20); err != nil || state != "unknown" {
		t.Fatal(state, err)
	}
	observation := newCodexObserver([]Source{sourceFor(a.Config.Home, "Codex")}).snapshot(context.Background(), nil, nil, "")
	if len(observation.Activity) != 0 || len(observation.Sessions) != 0 {
		t.Fatal("A saved file invented live activity")
	}
}

func TestConversationProviderFailuresAndStderrRemainBounded(t *testing.T) {
	for _, test := range []struct{ provider, flow, expect string }{{"Codex", "failure", "session limit"}, {"Codex", "malformed", "closed before"}, {"Codex", "stderr", "Streaming reply"}, {"Claude", "failure", "Not logged in"}} {
		a := providerFixture(t, "conversation-"+strings.ToLower(test.provider))
		a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_FLOW="+test.flow)
		request := conversationRequest{Session: Session{Provider: test.provider, Root: a.Config.Home, CWD: a.Config.Home, SessionID: "one"}, Prompt: "Synthetic test"}
		events, emit := turnOutput()
		err := a.runConversation(context.Background(), request, nil, emit)
		data, _ := json.Marshal(*events)
		combined := string(data)
		if err != nil {
			combined += err.Error()
		}
		if !strings.Contains(combined, test.expect) || len(combined) > 10000 {
			t.Fatal(test, combined)
		}
		if test.flow != "stderr" && strings.Contains(string(data), `"type":"done"`) {
			t.Fatal("Provider failure reported success", string(data))
		}
	}
}

func TestTuningFlagsAndModelChoiceDoNotRewriteProviderSettings(t *testing.T) {
	s := tuningFixture(t)
	path := filepath.Join(s.Root, "config.toml")
	write(t, path, "# keep provider configuration\n")
	for _, choice := range []struct {
		effort, speed string
		flags         []string
	}{
		{"high", "fast", []string{`model_reasoning_effort="high"`, `service_tier="fast"`, "features.fast_mode=true"}},
		{"ultra", "ultrafast", []string{`model_reasoning_effort="ultra"`, `service_tier="ultrafast"`, "features.ultrafast_mode=true"}},
		{"", "standard", []string{`service_tier="default"`}},
	} {
		command, err := runtimeCommand(s, "sol", choice.effort, choice.speed)
		if err != nil {
			t.Fatal(err)
		}
		for _, flag := range choice.flags {
			if !slices.Contains(command, flag) {
				t.Fatal("Missing explicit tuning", flag, command)
			}
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "# keep provider configuration\n" {
		t.Fatal("Provider settings changed", err)
	}
}

func TestExecutionFingerprintTracksEachProvidersProjectRules(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		a := fixture(t)
		root := filepath.Join(a.Config.Home, "profile")
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		s := Session{Provider: provider, Root: root, CWD: root}
		before, err := a.executionContext(context.Background(), s, nil)
		if err != nil {
			t.Fatal(err)
		}
		folder, file, body := ".codex", "config.toml", "approval_policy='never'\n"
		if provider == "Claude" {
			folder, file, body = ".claude", "settings.json", `{"permissions":{"defaultMode":"plan"}}`
		}
		write(t, filepath.Join(root, folder, file), body)
		after, err := a.executionContext(context.Background(), s, nil)
		if err != nil || before.Fingerprint == after.Fingerprint {
			t.Fatal("Project rule change retained approval", provider, err)
		}
		encoded, _ := json.Marshal(after)
		if strings.Contains(string(encoded), "defaultMode") || strings.Contains(string(encoded), "approval_policy") {
			t.Fatal("Private configuration returned in fingerprint")
		}
	}
}

func TestPermissionWireRejectsInvalidShapesBeforeProviderWork(t *testing.T) {
	a := fixture(t)
	a.commandPath = func(string, []string) (string, error) { t.Fatal("Invalid request started a provider"); return "", nil }
	for _, body := range []string{`{"session":{"provider":"Codex"},"permissions":[]}`, `{"session":{"provider":"Claude"},"permissions":{"claudeMode":true}}`} {
		if _, err := a.Run(context.Background(), "permissions", strings.NewReader(body)); err == nil {
			t.Fatal("Invalid permission input accepted", body)
		}
	}
}

func TestCompletedConversationReleasesItsNativeSessionLock(t *testing.T) {
	a := providerFixture(t, "conversation-codex")
	request := conversationRequest{Session: Session{Provider: "Codex", Root: a.Config.Home, CWD: a.Config.Home, SessionID: "same-session"}, Prompt: "Synthetic test"}
	for attempt := 0; attempt < 2; attempt++ {
		events, emit := turnOutput()
		if err := a.runConversation(context.Background(), request, nil, emit); err != nil {
			t.Fatal("Native lock survived completion", attempt, err)
		}
		if len(*events) == 0 || (*events)[len(*events)-1]["type"] != "done" {
			t.Fatal(*events)
		}
	}
}
