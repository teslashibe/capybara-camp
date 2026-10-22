package localcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func fixtureProtocol() (func() map[string]any, func(map[string]any)) {
	decoder := json.NewDecoder(os.Stdin)
	decoder.UseNumber()
	encoder := json.NewEncoder(os.Stdout)
	return func() map[string]any {
			var value map[string]any
			if decoder.Decode(&value) != nil {
				os.Exit(0)
			}
			return value
		}, func(value map[string]any) {
			if encoder.Encode(value) != nil {
				os.Exit(0)
			}
		}
}

func fixturePID() {
	if path := os.Getenv("CAMP_LOCAL_TEST_PID"); path != "" {
		_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600)
	}
}

func runConversationCodexFixture() {
	fixturePID()
	read, out := fixtureProtocol()
	flow := os.Getenv("CAMP_LOCAL_TEST_FLOW")
	for {
		message := read()
		method, params := str(message["method"]), object(message["params"])
		if method == "initialized" {
			continue
		}
		result := map[string]any{}
		switch method {
		case "config/read":
			result["config"] = map[string]any{}
		case "configRequirements/read":
			result["requirements"] = map[string]any{}
		case "thread/start", "thread/resume":
			if flow == "inference" && (method != "thread/start" || params["ephemeral"] != true || params["threadId"] != nil || str(params["baseInstructions"]) != "Synthetic memory system" || str(params["developerInstructions"]) != "" || params["config"].(map[string]any)["project_doc_max_bytes"] != json.Number("0")) {
				os.Exit(5)
			}
			if flow == "inference" {
				result["model"] = "gpt-5.4"
			}
			id := str(params["threadId"])
			if id == "" {
				id = "created"
			}
			result["thread"] = map[string]any{"id": id}
			for _, key := range []string{"approvalPolicy", "approvalsReviewer"} {
				if params[key] != nil {
					result[key] = params[key]
				}
			}
			if params["sandbox"] != nil {
				result["sandbox"] = map[string]any{"type": map[string]string{"read-only": "readOnly", "workspace-write": "workspaceWrite", "danger-full-access": "dangerFullAccess"}[str(params["sandbox"])]}
			}
			if params["permissions"] != nil {
				result["activePermissionProfile"] = map[string]any{"id": params["permissions"]}
			}
		case "turn/start":
			if flow == "inference" {
				checkInferencePrompt(str(object(array(params["input"])[0])["text"]))
				out(map[string]any{"id": message["id"], "result": map[string]any{"turn": map[string]any{"id": "active"}}})
				out(codexEvent("item/completed", map[string]any{"item": map[string]any{"id": "memory", "type": "agentMessage", "text": inferenceFixtureReply()}}))
				out(codexEvent("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 12, "outputTokens": 9}}}))
				out(codexCompleted())
				continue
			}
			if flow == "approval" {
				// Collision with the outgoing RPC ID must stay a server request.
				out(map[string]any{"id": message["id"], "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": params["threadId"], "turnId": "active", "itemId": "cmd", "command": "echo synthetic"}})
			}
			out(map[string]any{"id": message["id"], "result": map[string]any{"turn": map[string]any{"id": "active"}}})
			if flow == "stall" {
				continue
			}
			if flow == "failure" {
				out(codexEvent("turn/completed", map[string]any{"turn": map[string]any{"id": "active", "status": "failed", "error": map[string]any{"message": "session limit reached"}}}))
				continue
			}
			if flow == "malformed" {
				fmt.Fprintln(os.Stdout, "not json\n[]\n{}")
				return
			}
			if flow == "stderr" {
				_, _ = io.WriteString(os.Stderr, strings.Repeat("x", 300000))
			}
			if flow == "approval" {
				approval := read()
				if object(approval["result"])["decision"] != "accept" {
					os.Exit(4)
				}
			}
			out(codexEvent("item/agentMessage/delta", map[string]any{"threadId": params["threadId"], "turnId": "active", "itemId": "reply", "delta": "Streaming reply"}))
			out(codexCompleted())
			continue
		}
		out(map[string]any{"id": message["id"], "result": result})
	}
}

func runConversationClaudeFixture() {
	fixturePID()
	read, out := fixtureProtocol()
	message := read()
	if message["type"] == "control_request" {
		out(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": message["request_id"]}})
		message = read()
	}
	if message["type"] != "user" {
		os.Exit(4)
	}
	if os.Getenv("CAMP_LOCAL_TEST_FLOW") == "inference" {
		if slices.Contains(os.Args, "--resume") || !slices.Contains(os.Args, "--no-session-persistence") || !slices.Contains(os.Args, "--safe-mode") || !slices.Contains(os.Args, "--strict-mcp-config") {
			os.Exit(5)
		}
		index := slices.Index(os.Args, "--system-prompt-file")
		if index < 0 || index+1 >= len(os.Args) {
			os.Exit(5)
		}
		system, err := os.ReadFile(os.Args[index+1])
		if err != nil || string(system) != "Synthetic memory system" {
			os.Exit(5)
		}
		checkInferencePrompt(str(object(message["message"])["content"]))
		out(map[string]any{"type": "system", "subtype": "init", "model": "claude-opus-4-6", "effort": "high", "fast_mode_state": "off"})
		out(map[string]any{"type": "result", "is_error": false, "result": inferenceFixtureReply(), "usage": map[string]any{"input_tokens": 12, "output_tokens": 9}})
		for {
			read()
		}
	}
	mode := "default"
	for index, arg := range os.Args {
		if arg == "--permission-mode" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
		}
	}
	out(map[string]any{"type": "system", "subtype": "init", "permissionMode": mode})
	switch os.Getenv("CAMP_LOCAL_TEST_FLOW") {
	case "failure":
		out(map[string]any{"type": "result", "is_error": true, "result": "Not logged in"})
		return
	case "stall":
		for {
			read()
		}
	case "approval":
		out(map[string]any{"type": "control_request", "request_id": "approval", "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": "echo synthetic"}}})
		response := read()
		if object(object(response["response"])["response"])["behavior"] != "allow" {
			os.Exit(4)
		}
	}
	out(map[string]any{"type": "stream_event", "event": map[string]any{"delta": map[string]any{"type": "text_delta", "text": "Streaming reply"}}})
	out(map[string]any{"type": "result", "is_error": false, "result": "Streaming reply"})
	for {
		read()
	}
}

type conversationOutput struct {
	bytes.Buffer
	onEvent func(map[string]any)
}

func (o *conversationOutput) Write(data []byte) (int, error) {
	if o.onEvent != nil {
		var event map[string]any
		if err := json.Unmarshal(data, &event); err != nil {
			return 0, err
		}
		o.onEvent(event)
	}
	return o.Buffer.Write(data)
}

func TestConversationRealProviderProcessesStreamAndApprove(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		for _, mode := range []string{"chat", "planning", "implementation"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				a := providerFixture(t, "conversation-"+strings.ToLower(provider))
				flow := "complete"
				if mode == "implementation" {
					flow = "approval"
				}
				a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_FLOW="+flow)
				pidPath := filepath.Join(a.Config.Home, "child.pid")
				a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_PID="+pidPath)
				session := Session{Provider: provider, Root: a.Config.Home, CWD: a.Config.Home, SessionID: "00000000-0000-0000-0000-000000000001"}
				request := conversationRequest{Session: session, Mode: mode, Prompt: "Synthetic test"}
				if mode == "implementation" {
					request.NewSession, request.ExecutionApproved = true, true
					request.Permissions = PermissionSettings{"approval": "on-request", "sandbox": "workspace-write"}
					if provider == "Claude" {
						request.Permissions = PermissionSettings{"claudeMode": "manual"}
					}
					value, err := a.executionContext(context.Background(), session, request.Permissions)
					requireApprovalOK(t, err)
					request.ExecutionContext = value.Fingerprint
				}
				reader, writer := io.Pipe()
				defer writer.Close()
				approved, created, streamed := 0, 0, false
				out := &conversationOutput{onEvent: func(event map[string]any) {
					switch event["type"] {
					case "approval":
						approved++
						requireApprovalOK(t, json.NewEncoder(writer).Encode(map[string]any{"type": "approval", "id": event["id"], "allow": true}))
					case "session":
						created++
						root, _ := filepath.EvalSymlinks(session.Root)
						digest := sha256.Sum256([]byte(provider + ":" + root + ":" + str(event["sessionID"])))
						path := filepath.Join(a.Config.Support, "run-"+hex.EncodeToString(digest[:])+".lock")
						lock, err := os.OpenFile(path, os.O_RDWR, 0600)
						requireApprovalOK(t, err)
						err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
						lock.Close()
						if !errors.Is(err, syscall.EWOULDBLOCK) {
							t.Fatal("New native identity published before lock acquisition", err)
						}
					case "text":
						streamed = event["text"] == "Streaming reply"
					}
				}}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				go func() { _ = json.NewEncoder(writer).Encode(request) }()
				requireApprovalOK(t, a.Conversation(ctx, reader, out))
				if !streamed || !strings.Contains(out.String(), `"type":"done"`) || mode == "implementation" && (approved != 1 || created != 1) || mode != "implementation" && (approved != 0 || created != 0) {
					t.Fatal("Incomplete conversation", out.String())
				}
				pid, err := os.ReadFile(pidPath)
				requireApprovalOK(t, err)
				id, _ := strconv.Atoi(string(pid))
				if err := syscall.Kill(id, 0); !errors.Is(err, syscall.ESRCH) {
					t.Fatal("Provider process was not reaped", err)
				}
			})
		}
	}
}

func TestConversationCancellationReapsProviderAndInputReader(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for index := 0; index < 4; index++ {
		a := providerFixture(t, "conversation-codex")
		a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_FLOW=stall")
		reader, writer := io.Pipe()
		var once sync.Once
		out := &conversationOutput{onEvent: func(event map[string]any) {
			if event["type"] == "started" {
				once.Do(func() { go func() { _ = json.NewEncoder(writer).Encode(map[string]any{"type": "stop"}) }() })
			}
		}}
		request := conversationRequest{Session: Session{Provider: "Codex", Root: a.Config.Home, CWD: a.Config.Home, SessionID: "one"}, Prompt: "Synthetic test"}
		go func() { _ = json.NewEncoder(writer).Encode(request) }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := a.Conversation(ctx, reader, out)
		cancel()
		writer.Close()
		requireApprovalOK(t, err)
		if !strings.Contains(out.String(), `"type":"cancelled"`) || strings.Contains(out.String(), `"type":"done"`) {
			t.Fatal(out.String())
		}
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline+3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runtime.NumGoroutine() > baseline+3 {
		t.Fatal("Conversation input or process goroutines leaked")
	}
}
