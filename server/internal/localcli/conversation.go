package localcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxConversationText = 2 << 20

type conversationRequest struct {
	Session             Session                   `json:"session"`
	Prompt              string                    `json:"prompt"`
	Model               string                    `json:"model"`
	Effort              string                    `json:"effort"`
	Speed               string                    `json:"speed"`
	Mode                string                    `json:"mode"`
	ExecutionApproved   bool                      `json:"executionApproved"`
	NewSession          bool                      `json:"newSession"`
	ExecutionContext    string                    `json:"executionContext"`
	Permissions         PermissionSettings        `json:"permissions"`
	Context             json.RawMessage           `json:"context"`
	History             []HistorySource           `json:"-"`
	ReadHistory         HistoryReader             `json:"-"`
	RestoredHistory     *RestoredHistoryReference `json:"restoredHistory,omitempty"`
	grant               *contextGrant
	inferenceSystem     string
	inferenceSystemFile string
}

// ConversationRequest is shared by the desktop stream and installation runner.
type ConversationRequest = conversationRequest

func (a *App) RunConversation(ctx context.Context, request ConversationRequest, controls <-chan map[string]any, emit func(map[string]any) error) error {
	return a.runConversation(ctx, request, controls, emit)
}

func (a *App) ExecutionContext(ctx context.Context, session Session, settings PermissionSettings) (ExecutionContext, error) {
	return a.executionContext(ctx, session, settings)
}

type turnConnection interface {
	approvalConnection
	RPC(string, map[string]any, time.Duration) (map[string]any, error)
	Next() (map[string]any, error)
}

func jsonValue(value any) string { data, _ := json.Marshal(value); return string(data) }

// Conversation owns one provider process and a bounded input queue. The caller
// supplies a closeable, pollable stream so cancellation joins the input reader.
func (a *App) Conversation(ctx context.Context, input io.ReadCloser, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	closeInput := context.AfterFunc(ctx, func() { _ = input.Close() })
	defer closeInput()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), maxNativeEvent+2)
	if !scanner.Scan() || len(scanner.Bytes()) > maxNativeEvent {
		return errors.New("Invalid conversation request")
	}
	var request conversationRequest
	decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("Invalid conversation request")
	}
	// Keep null distinct from a deliberate false for this execution choice.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(scanner.Bytes(), &fields)
	if value, ok := fields["newSession"]; ok && string(value) != "true" && string(value) != "false" {
		return errors.New("Invalid session creation choice")
	}
	controls := make(chan map[string]any, 16)
	inputError := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			if len(scanner.Bytes()) > maxNativeEvent {
				break
			}
			var event map[string]any
			decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
			decoder.UseNumber()
			if decoder.Decode(&event) != nil || event == nil || decoder.Decode(new(any)) != io.EOF {
				continue
			}
			if str(event["type"]) == "stop" {
				cancel()
				return
			}
			select {
			case controls <- event:
			case <-ctx.Done():
				return
			}
		}
		if scanner.Err() != nil && ctx.Err() == nil {
			inputError <- errors.New("Conversation control input exceeded its limit or closed unexpectedly")
		}
		cancel() // EOF is a stop, never permission to keep running unattended.
	}()
	defer func() { cancel(); _ = input.Close(); <-done }()
	emit := func(value map[string]any) error {
		data, err := json.Marshal(value)
		if err != nil || len(data) > 4<<20 {
			return errors.New("The conversation exceeded the event size limit")
		}
		if writer, ok := output.(interface{ SetWriteDeadline(time.Time) error }); ok {
			if err := writer.SetWriteDeadline(time.Now().Add(20 * time.Second)); err != nil {
				return errors.New("Conversation output cannot enforce its write deadline")
			}
		}
		data = append(data, '\n')
		n, err := output.Write(data)
		if err != nil || n != len(data) {
			return errors.New("Conversation output closed")
		}
		return nil
	}
	err := a.runConversation(ctx, request, controls, emit)
	select {
	case failure := <-inputError:
		return failure
	default:
	}
	if errors.Is(err, context.Canceled) {
		_ = emit(map[string]any{"type": "cancelled", "text": "Stopped. Queued prompts are paused."})
		return nil
	}
	return err
}

func validSessionID(id string) bool {
	return id != "" && len(id) <= 200 && !strings.ContainsAny(id, "/\\\x00\r\n")
}

func (a *App) runConversation(ctx context.Context, request conversationRequest, controls <-chan map[string]any, emit func(map[string]any) error) error {
	if request.Mode == "" {
		request.Mode = "chat"
	}
	if request.Mode != "chat" && request.Mode != "planning" && request.Mode != "implementation" && request.Mode != "inference" {
		return errors.New("Unknown conversation mode")
	}
	implementation, planning := request.Mode == "implementation", request.Mode == "planning"
	inference := request.Mode == "inference"
	if inference && (request.inferenceSystem == "" || request.inferenceSystemFile == "" || request.NewSession || len(request.Context) > 0 || len(request.History) > 0) {
		return errors.New("Inference requires its dedicated entry point")
	}
	if implementation && !request.ExecutionApproved {
		return errors.New("Approve the implementation before enabling project tools")
	}
	if !implementation && len(request.Permissions) != 0 {
		return errors.New("Project permissions require explicit execution approval")
	}
	session := request.Session
	promptLimit := 65536
	if inference {
		promptLimit = 1 << 20
	}
	if session.Provider != "Codex" && session.Provider != "Claude" || strings.TrimSpace(request.Prompt) == "" || len(request.Prompt) > promptLimit || !validSessionID(session.SessionID) {
		return errors.New("Choose a session and write a prompt under 64 KB")
	}
	if !filepath.IsAbs(session.Root) || !filepath.IsAbs(session.CWD) {
		return errors.New("Restore the original project and profile before resuming this session")
	}
	var err error
	session.Root, err = canonical(session.Root, a.Config.Home)
	if err != nil || !isDir(session.Root) {
		return errors.New("Restore the selected profile before resuming this session")
	}
	session.CWD, err = canonical(session.CWD, a.Config.Home)
	if err != nil || !isDir(session.CWD) {
		return errors.New("Restore the original project folder before resuming this session")
	}
	request.Session = session
	if err = a.attachRestoredHistory(ctx, &request); err != nil {
		return err
	}
	if session.IsSubagent && !planning {
		return errors.New("Subagents are read-only in Camp. Continue their parent session")
	}
	request.Permissions, err = normalizePermissions(session.Provider, request.Permissions)
	if err != nil {
		return err
	}
	if implementation {
		approved, err := a.executionContext(ctx, session, request.Permissions)
		if err != nil {
			return err
		}
		if request.ExecutionContext != approved.Fingerprint {
			return errors.New("The project or CLI settings changed. Review the implementation approval again")
		}
	}
	if (planning || inference) && session.Provider == "Claude" {
		session.SessionID = uuid.NewString()
	}
	command, err := runtimeCommand(session, request.Model, request.Effort, request.Speed)
	if err != nil {
		return err
	}
	locks, err := a.sessionLocks()
	if err != nil {
		return err
	}
	defer locks.close()
	acquire := locks.acquire
	key := session.Provider + ":" + session.Root + ":" + session.SessionID
	if planning {
		key = "planning:" + session.Provider
	}
	if inference {
		key = "inference:" + session.Provider + ":" + session.Root
	}
	if err := acquire(key); err != nil {
		return err
	}
	if !planning && !inference && !request.NewSession && !a.isolated {
		if err := a.assertAvailable(ctx, session); err != nil {
			return err
		}
	}
	if len(request.Context) != 0 && !bytes.Equal(bytes.TrimSpace(request.Context), []byte("null")) || len(request.History) > 0 {
		data := request.Context
		if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			data = []byte("[]")
		}
		sources, err := decodeContext(data)
		if err != nil {
			return err
		}
		if len(sources) != 0 || len(request.History) > 0 {
			self, err := os.Executable()
			if err != nil {
				return errors.New("Could not locate the bundled context helper")
			}
			request.grant, err = newContextGrantWithHistory(sources, request.History, request.ReadHistory, a.Config.Support, self)
			if err != nil {
				return err
			}
			defer request.grant.Close()
			request.Prompt += request.grant.attachmentLinks()
		}
	}
	command, err = a.conversationCommand(ctx, session, request, command)
	if err != nil {
		return err
	}
	env := a.runtimeEnvironment(session)
	if request.grant != nil && session.Provider == "Claude" && !implementation {
		filtered := env[:0]
		for _, entry := range env {
			if !strings.HasPrefix(entry, "CLAUDE_CODE_SAFE_MODE=") {
				filtered = append(filtered, entry)
			}
		}
		env = filtered
	}
	limit := maxNativeEvent
	if inference {
		limit = maxInferenceWire
	}
	connection, err := a.connectWithLimit(ctx, session, command, implementation, env, limit)
	if err != nil {
		return err
	}
	defer connection.Close()
	request.Session = session
	publish := func(event map[string]any) error {
		if str(event["type"]) == "session" {
			id := str(event["sessionID"])
			if !request.NewSession || !validSessionID(id) {
				return errors.New("The provider returned an invalid session identity")
			}
			if err := acquire(session.Provider + ":" + session.Root + ":" + id); err != nil {
				return err
			}
		}
		return emit(event)
	}
	if session.Provider == "Codex" {
		return codexTurn(connection, request, controls, publish)
	}
	return claudeTurn(connection, request, controls, publish)
}

func (a *App) conversationCommand(ctx context.Context, session Session, request conversationRequest, validated []string) ([]string, error) {
	if request.Mode == "implementation" {
		command, err := executionCommand(session, validated, request.NewSession, request.Permissions)
		if err != nil {
			return nil, err
		}
		if session.Provider == "Claude" && request.grant != nil {
			command = append(command, "--mcp-config", jsonValue(map[string]any{"mcpServers": map[string]any{"camp_context": request.grant.server}}), "--append-system-prompt-file", request.grant.instructionsPath, "--system-prompt-snapshot", "off")
		}
		return command, nil
	}
	if session.Provider == "Codex" {
		command, err := codexChatCommand(ctx, session, request.grant)
		if err == nil && request.Mode == "inference" {
			command = append(command, "-c", "project_doc_max_bytes=0")
		}
		return command, err
	}
	command := append(validated, "--input-format", "stream-json", "--replay-user-messages")
	if request.Mode == "planning" || request.Mode == "inference" || request.NewSession {
		var err error
		command, err = removeCommandFlag(command, "--resume", true)
		if err != nil {
			return nil, err
		}
		command = append(command, "--session-id", session.SessionID)
		if request.Mode == "planning" || request.Mode == "inference" {
			command = append(command, "--no-session-persistence")
		}
	}
	if request.Mode == "inference" {
		command = append(command, "--system-prompt-file", request.inferenceSystemFile, "--system-prompt-snapshot", "off", "--setting-sources", "", "--disable-slash-commands")
	}
	if request.grant != nil {
		var err error
		command, err = removeCommandFlag(command, "--safe-mode", false)
		if err != nil {
			return nil, err
		}
		settings := map[string]any{"disableAllHooks": true, "autoMemoryEnabled": false}
		for index, value := range command {
			if value == "--settings" && index+1 < len(command) {
				var selected map[string]any
				_ = json.Unmarshal([]byte(command[index+1]), &selected)
				for key, value := range selected {
					settings[key] = value
				}
				command, err = removeCommandFlag(command, "--settings", true)
				if err != nil {
					return nil, err
				}
				break
			}
		}
		command = append(command, "--restricted", "--setting-sources", "", "--disable-slash-commands", "--settings", jsonValue(settings), "--mcp-config", jsonValue(map[string]any{"mcpServers": map[string]any{"camp_context": request.grant.server}}), "--allowedTools", "mcp__camp_context__read_session", "--append-system-prompt-file", request.grant.instructionsPath, "--system-prompt-snapshot", "off")
	}
	return command, nil
}

func emitConversationText(request conversationRequest, emit func(map[string]any) error, text string) error {
	if request.Mode == "planning" && len(text) > 8192 {
		return errors.New("The planning response exceeded 8 KB. This call used its allowance; restart with a shorter brief to try again")
	}
	if len(text) > conversationTextLimit(request) {
		if request.Mode == "inference" {
			return errors.New("The inference response exceeded 4 MiB")
		}
		return errors.New("The conversation response exceeded 2 MB")
	}
	return emit(map[string]any{"type": "text", "text": text})
}

func conversationTextLimit(request conversationRequest) int {
	if request.Mode == "inference" {
		return maxInferenceText
	}
	return maxConversationText
}

func emitUsage(emit func(map[string]any) error, usage map[string]any, codex bool) error {
	result := map[string]any{"type": "usage"}
	input, output := "input_tokens", "output_tokens"
	if codex {
		input, output = "inputTokens", "outputTokens"
	}
	count := func(value any) (int64, bool) {
		n, ok := value.(json.Number)
		if !ok {
			return 0, false
		}
		parsed, err := n.Int64()
		return parsed, err == nil && parsed >= 0 && parsed <= 9_007_199_254_740_991
	}
	for source, destination := range map[string]string{input: "inputTokens", output: "outputTokens"} {
		if n, ok := count(usage[source]); ok {
			result[destination] = n
		}
	}
	if n, ok := result["inputTokens"].(int64); !codex && ok {
		for _, key := range []string{"cache_read_input_tokens", "cache_creation_input_tokens"} {
			if value, ok := count(usage[key]); ok && n <= 9_007_199_254_740_991-value {
				n += value
			}
		}
		result["inputTokens"] = n
	}
	if len(result) > 1 {
		return emit(result)
	}
	return nil
}
