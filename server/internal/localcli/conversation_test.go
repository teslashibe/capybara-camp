package localcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

type turnCall struct {
	method string
	params map[string]any
}

type turnFixture struct {
	approvalFixture
	calls  []turnCall
	events []map[string]any
	rpc    func(string, map[string]any) (map[string]any, error)
	next   func() (map[string]any, error)
}

func (f *turnFixture) RPC(method string, params map[string]any, _ time.Duration) (map[string]any, error) {
	copy, _ := cloneObject(params)
	f.calls = append(f.calls, turnCall{method, copy})
	if f.rpc != nil {
		return f.rpc(method, params)
	}
	switch method {
	case "config/read":
		return map[string]any{"config": map[string]any{"mcp_servers": map[string]any{"server.with.dot": map[string]any{"enabled": true}}}}, nil
	case "thread/start":
		return map[string]any{"thread": map[string]any{"id": "new-session"}}, nil
	case "turn/start":
		return map[string]any{"turn": map[string]any{"id": "active"}}, nil
	case "turn/steer":
		return map[string]any{"turnId": "active"}, nil
	default:
		return map[string]any{}, nil
	}
}

func (f *turnFixture) Next() (map[string]any, error) {
	if f.next != nil {
		return f.next()
	}
	if len(f.events) == 0 {
		return nil, io.EOF
	}
	event := f.events[0]
	f.events = f.events[1:]
	return event, nil
}

func turnOutput() (*[]map[string]any, func(map[string]any) error) {
	events := []map[string]any{}
	return &events, func(value map[string]any) error {
		copy, err := cloneObject(value)
		events = append(events, copy)
		return err
	}
}

func codexEvent(method string, params map[string]any) map[string]any {
	return map[string]any{"method": method, "params": params}
}

func codexCompleted() map[string]any {
	return codexEvent("turn/completed", map[string]any{"turn": map[string]any{"id": "active", "status": "completed"}})
}

func TestCodexTurnPreviewPlanningAndOrderedStreaming(t *testing.T) {
	for _, mode := range []string{"chat", "planning"} {
		t.Run(mode, func(t *testing.T) {
			f := &turnFixture{events: []map[string]any{
				codexEvent("item/agentMessage/delta", map[string]any{"threadId": "foreign", "itemId": "other", "delta": "private"}),
				codexEvent("item/agentMessage/delta", map[string]any{"itemId": "one", "delta": "partial"}),
				codexEvent("item/completed", map[string]any{"item": map[string]any{"type": "agentMessage", "id": "one", "text": "Complete"}}),
				codexEvent("item/agentMessage/delta", map[string]any{"itemId": "two", "delta": "Second"}),
				codexEvent("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"total": map[string]any{"inputTokens": json.Number("12"), "outputTokens": json.Number("8")}}}),
				codexCompleted(),
			}}
			events, emit := turnOutput()
			grant := &contextGrant{sources: contextFixture(), server: map[string]any{"command": "/fixture/camp-local", "args": []any{"context-tool", "/fixture/context"}}}
			request := conversationRequest{Session: Session{SessionID: "original", CWD: "/tmp"}, Mode: mode, Prompt: "The original prompt", Model: "chosen", Effort: "high", Speed: "ultrafast", grant: grant}
			requireApprovalOK(t, codexTurn(f, request, make(chan map[string]any), emit))
			start := f.calls[2]
			if mode == "planning" && (start.method != "thread/start" || start.params["ephemeral"] != true || start.params["threadId"] != nil) {
				t.Fatal("Planning reused a real session", start)
			}
			if mode == "chat" && start.method != "thread/resume" {
				t.Fatal(start)
			}
			if start.params["sandbox"] != "read-only" || start.params["approvalPolicy"] != "on-request" {
				t.Fatal("Preview enabled project writes", start)
			}
			config := object(start.params["config"])
			if object(object(config["mcp_servers"])["server.with.dot"])["enabled"] != false || config["features.shell_tool"] != false || !strings.Contains(str(start.params["developerInstructions"]), "read these exact snapshots") {
				t.Fatal("Preview tools or context contract missing", start)
			}
			turn := f.calls[3].params
			if turn["serviceTier"] != "ultrafast" || object(turn["input"].([]any)[0])["text"] != request.Prompt || turn["effort"] != "high" {
				t.Fatal("Prompt or tuning changed", turn)
			}
			if str((*events)[len(*events)-1]["type"]) != "done" || str((*events)[len(*events)-3]["text"]) != "Complete\n\nSecond" {
				t.Fatal(*events)
			}
			for _, event := range *events {
				if strings.Contains(str(event["text"]), "private") || str(event["type"]) == "session" {
					t.Fatal("Foreign output or ephemeral ID exposed", event)
				}
			}
		})
	}
}

func TestCodexSteeringAndPreviewToolRejection(t *testing.T) {
	f := &turnFixture{events: []map[string]any{{"id": 1, "method": "item/commandExecution/requestApproval"}, codexCompleted()}}
	controls := make(chan map[string]any, 1)
	events, emit := turnOutput()
	onEvent := func(event map[string]any) error {
		if str(event["type"]) == "started" {
			controls <- map[string]any{"type": "steer", "id": "steering", "text": "Change direction"}
		}
		return emit(event)
	}
	requireApprovalOK(t, codexTurn(f, conversationRequest{Session: Session{SessionID: "original"}, Mode: "chat", Prompt: "Hello"}, controls, onEvent))
	steer := f.calls[4]
	if steer.method != "turn/steer" || steer.params["expectedTurnId"] != "active" || steer.params["clientUserMessageId"] != "steering" || len(f.sent) != 2 || f.sent[1]["error"] == nil {
		t.Fatal(f.calls, f.sent)
	}
	if (*events)[1]["type"] != "steerAccepted" {
		t.Fatal(*events)
	}
}

func TestCodexManagedPermissionsFailBeforeTurnStart(t *testing.T) {
	for _, failure := range []string{"restricted", "unconfirmed", "malformed"} {
		f := &turnFixture{}
		f.rpc = func(method string, params map[string]any) (map[string]any, error) {
			switch method {
			case "config/read":
				return map[string]any{"config": map[string]any{}}, nil
			case "configRequirements/read":
				if failure == "restricted" {
					return map[string]any{"requirements": map[string]any{"allowedApprovalPolicies": []any{}}}, nil
				}
				if failure == "malformed" {
					return map[string]any{"requirements": []any{}}, nil
				}
				return map[string]any{}, nil
			default:
				return map[string]any{}, nil
			}
		}
		_, emit := turnOutput()
		err := codexTurn(f, conversationRequest{Session: Session{SessionID: "original"}, Mode: "implementation", Permissions: PermissionSettings{"approval": "never"}}, nil, emit)
		if err == nil || slices.ContainsFunc(f.calls, func(call turnCall) bool { return call.method == "turn/start" }) {
			t.Fatal("Permission validation failed open", failure, err, f.calls)
		}
	}
}

func TestClaudeSteeringWaitsForBothAcknowledgments(t *testing.T) {
	for _, boundaryFirst := range []bool{false, true} {
		f := &turnFixture{}
		controls := make(chan map[string]any, 1)
		step := 0
		f.next = func() (map[string]any, error) {
			step++
			switch step {
			case 1:
				return map[string]any{"type": "system", "subtype": "init"}, nil
			case 2, 3:
				if len(f.sent) != 2 || str(object(f.sent[1]["request"])["subtype"]) != "interrupt" {
					t.Fatal("Prompt resent before both acknowledgments", f.sent)
				}
				if (step == 2) == boundaryFirst {
					return map[string]any{"type": "result", "is_error": true}, nil
				}
				return map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": "steering"}}, nil
			case 4:
				if len(f.sent) != 3 || str(object(f.sent[2]["message"])["content"]) != "Change direction" {
					t.Fatal("Steering prompt missing", f.sent)
				}
				return f.sent[2], nil
			default:
				return map[string]any{"type": "result", "is_error": false, "result": "Redirected"}, nil
			}
		}
		events, emit := turnOutput()
		onEvent := func(event map[string]any) error {
			if str(event["type"]) == "started" {
				controls <- map[string]any{"type": "steer", "id": "steering", "text": "Change direction"}
			}
			return emit(event)
		}
		requireApprovalOK(t, claudeTurn(f, conversationRequest{Session: Session{SessionID: "original"}, Mode: "chat", Prompt: "Hello"}, controls, onEvent))
		kinds := []string{}
		for _, event := range *events {
			kinds = append(kinds, str(event["type"]))
		}
		if !reflect.DeepEqual(kinds, []string{"started", "boundary", "steerAccepted", "text", "done"}) {
			t.Fatal(kinds)
		}
	}
}

func TestClaudeStreamingUsageAndContextConnection(t *testing.T) {
	f := &turnFixture{events: []map[string]any{
		{"type": "system", "subtype": "init", "mcp_servers": []any{map[string]any{"name": "camp_context", "status": "connected"}}, "fast_mode_state": "off"},
		{"type": "stream_event", "parent_tool_use_id": "child", "event": map[string]any{"delta": map[string]any{"type": "text_delta", "text": "private"}}},
		{"type": "stream_event", "event": map[string]any{"delta": map[string]any{"type": "text_delta", "text": "Partial"}}},
		{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "Complete"}}, "usage": map[string]any{"speed": "standard"}}},
		{"type": "result", "is_error": false, "result": "Complete", "usage": map[string]any{"input_tokens": json.Number("10"), "cache_read_input_tokens": json.Number("3"), "output_tokens": json.Number("8")}},
	}}
	events, emit := turnOutput()
	requireApprovalOK(t, claudeTurn(f, conversationRequest{Session: Session{SessionID: "original"}, Mode: "chat", Speed: "fast", grant: &contextGrant{}}, nil, emit))
	foundUsage, foundText := false, false
	for _, event := range *events {
		foundText = foundText || event["text"] == "Partial"
		foundUsage = foundUsage || event["type"] == "usage" && sameID(event["inputTokens"], 13)
		if strings.Contains(str(event["text"]), "private") {
			t.Fatal("Child text mixed into parent output")
		}
	}
	if !foundUsage || !foundText || (*events)[len(*events)-1]["type"] != "done" {
		t.Fatal(*events)
	}
	missing := &turnFixture{events: []map[string]any{{"type": "system", "subtype": "init"}}}
	if claudeTurn(missing, conversationRequest{grant: &contextGrant{}}, nil, emit) == nil {
		t.Fatal("Disconnected context accepted")
	}
}

func TestClaudeApprovalInitializationAndModeConfirmation(t *testing.T) {
	f := &turnFixture{}
	step := 0
	f.next = func() (map[string]any, error) {
		step++
		switch step {
		case 1:
			if len(f.sent) != 1 || object(f.sent[0]["request"])["subtype"] != "initialize" {
				t.Fatal("Prompt sent before approval channel initialization", f.sent)
			}
			return map[string]any{"type": "control_response", "response": map[string]any{"request_id": f.sent[0]["request_id"], "subtype": "success"}}, nil
		case 2:
			if len(f.sent) != 2 || f.sent[1]["type"] != "user" {
				t.Fatal(f.sent)
			}
			return map[string]any{"type": "system", "subtype": "init", "permissionMode": "acceptEdits"}, nil
		default:
			return nil, io.EOF
		}
	}
	events, emit := turnOutput()
	err := claudeTurn(f, conversationRequest{Session: Session{SessionID: "original"}, Mode: "implementation", NewSession: true, Permissions: PermissionSettings{"claudeMode": "manual"}}, nil, emit)
	if err == nil || !strings.Contains(err.Error(), "did not confirm") || len(*events) != 1 || (*events)[0]["type"] != "session" {
		t.Fatal("Mode mismatch allowed execution", err, *events)
	}
}

func TestConversationLimitsAndMissingUsageStayHonest(t *testing.T) {
	events, emit := turnOutput()
	requireApprovalOK(t, emitUsage(emit, map[string]any{"inputTokens": nil, "outputTokens": json.Number("-1")}, true))
	requireApprovalOK(t, emitUsage(emit, map[string]any{"inputTokens": true, "outputTokens": json.Number("1.5")}, true))
	if len(*events) != 0 {
		t.Fatal("Invented token usage", *events)
	}
	if emitConversationText(conversationRequest{Mode: "planning"}, emit, strings.Repeat("🌱", 3000)) == nil {
		t.Fatal("Unbounded planning text")
	}
	f := &turnFixture{events: []map[string]any{{"type": "system", "subtype": "init"}, {"type": "result", "result": strings.Repeat("x", 8193)}}}
	if claudeTurn(f, conversationRequest{Mode: "planning"}, nil, emit) == nil {
		t.Fatal("Final planning result bypassed limit")
	}
}

func TestConversationConfigurationAndCommands(t *testing.T) {
	a := fixture(t)
	root, cwd := filepath.Join(a.Config.Home, "profile"), filepath.Join(a.Config.Home, "project")
	write(t, filepath.Join(root, "config.toml"), "[mcp_servers.\"desktop.server\"]\nurl='app-only://placeholder'\n[mcp_servers.external]\ncommand='outside'\n")
	write(t, filepath.Join(cwd, ".codex/config.toml"), "[mcp_servers.project]\nurl='private://placeholder'\n")
	command, err := codexChatCommand(context.Background(), Session{Root: root, CWD: cwd}, &contextGrant{server: map[string]any{"command": "/path with space/camp-local", "args": []string{"context-tool", "/path with space/sources.json"}}})
	requireApprovalOK(t, err)
	joined := strings.Join(command, " ")
	if strings.Contains(joined, "app-only") || strings.Contains(joined, "outside") || !strings.Contains(joined, `"desktop.server"={enabled=false,url="http://127.0.0.1:9"}`) || !strings.Contains(joined, "features.multi_agent=false") {
		t.Fatal(command)
	}
	s := Session{Provider: "Claude", SessionID: "00000000-0000-0000-0000-000000000001"}
	validated, err := runtimeCommand(s, "", "", "")
	requireApprovalOK(t, err)
	command, err = executionCommand(s, validated, true, PermissionSettings{"claudeMode": "auto"})
	requireApprovalOK(t, err)
	for _, removed := range []string{"--safe-mode", "--strict-mcp-config", "--tools", "--resume", "--dangerously-skip-permissions"} {
		if slices.Contains(command, removed) {
			t.Fatal("Preview restrictions or unchosen bypass in implementation", command)
		}
	}
	if !slices.Contains(command, "--permission-prompt-tool") || !slices.Contains(command, "auto") || !slices.Contains(command, "--session-id") {
		t.Fatal(command)
	}
	request := conversationRequest{Mode: "planning", grant: &contextGrant{server: map[string]any{}, instructionsPath: "/context"}}
	command, err = a.conversationCommand(context.Background(), s, request, validated)
	requireApprovalOK(t, err)
	if slices.Contains(command, "--resume") || slices.Contains(command, "--safe-mode") || !slices.Contains(command, "--no-session-persistence") || !slices.Contains(command, "mcp__camp_context__read_session") || !slices.Contains(command, "--restricted") {
		t.Fatal(command)
	}
}

func TestConversationApprovalAndSessionLockBeforeProviderLaunch(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, "profile")
	requireApprovalOK(t, os.MkdirAll(root, 0700))
	session := Session{Provider: "Claude", Root: root, CWD: root, SessionID: "native"}
	request := conversationRequest{Session: session, Mode: "implementation", Prompt: "Implement", ExecutionApproved: true}
	_, emit := turnOutput()
	calls := 0
	a.commandPath = func(string, []string) (string, error) { calls++; return "", errors.New("should not run") }
	for _, bad := range []conversationRequest{
		{Session: session, Mode: "implementation", Prompt: "Implement"}, request,
		{Session: session, Prompt: "Hello", Permissions: PermissionSettings{"claudeMode": "auto"}},
		{Session: Session{Provider: "Claude", Root: root}, Prompt: "Hello"},
	} {
		if a.runConversation(context.Background(), bad, nil, emit) == nil || calls != 0 {
			t.Fatal("Invalid execution started a provider")
		}
	}
	approved, err := a.executionContext(context.Background(), session, nil)
	requireApprovalOK(t, err)
	request.ExecutionContext = approved.Fingerprint
	canonicalRoot, err := filepath.EvalSymlinks(root)
	requireApprovalOK(t, err)
	key := session.Provider + ":" + canonicalRoot + ":" + session.SessionID
	digest := sha256.Sum256([]byte(key))
	lockPath := filepath.Join(a.Config.Support, "run-"+hex.EncodeToString(digest[:])+".lock")
	requireApprovalOK(t, os.MkdirAll(a.Config.Support, 0700))
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	requireApprovalOK(t, err)
	defer lock.Close()
	requireApprovalOK(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	request.Session.ID = "different-logical-ticket"
	err = a.runConversation(context.Background(), request, nil, emit)
	if err == nil || !strings.Contains(err.Error(), "already running") || calls != 0 {
		t.Fatal("Logical ticket bypassed the native lock", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.runConversation(ctx, request, nil, emit); err == nil {
		t.Fatal("Cancelled execution admitted")
	}
}
