// camp-test-provider supplies synthetic stdio responses to the native test
// suites. It is built by test.sh and is never bundled in the app or server.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type message = map[string]any

type input struct{ values <-chan any }

func newInput() input {
	values := make(chan any, 1)
	go func() {
		defer close(values)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 65536), 2<<20)
		for scanner.Scan() {
			var value any
			if json.Unmarshal(scanner.Bytes(), &value) != nil {
				return
			}
			values <- value
		}
	}()
	return input{values}
}

func (i input) next() (any, error) {
	v, ok := <-i.values
	if !ok {
		return nil, io.EOF
	}
	return v, nil
}

func (i input) request() (message, error) {
	v, err := i.next()
	if err != nil {
		return nil, err
	}
	r, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("Expected fixture request")
	}
	return r, nil
}

func emit(v any) error     { return json.NewEncoder(os.Stdout).Encode(v) }
func object(v any) message { result, _ := v.(map[string]any); return result }
func text(v any) string    { result, _ := v.(string); return result }
func array(v any) []any    { result, _ := v.([]any); return result }
func require(ok bool) error {
	if !ok {
		return errors.New("Native fixture protocol assertion failed")
	}
	return nil
}

func appendRecord(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	return err
}

// wait returns true after Stop or EOF. Keeping one bounded input reader lets a
// native cancellation interrupt a timed response without sleeping blindly.
func (i input) wait(duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case value, ok := <-i.values:
			if !ok || text(object(value)["type"]) == "stop" {
				_ = emit(message{"type": "cancelled"})
				return true
			}
		case <-timer.C:
			return false
		}
	}
}

func approval(id, kind string) message {
	return message{"type": "approval", "id": id, "kind": kind, "title": "Run command", "detail": "echo test", "questions": []any{}, "canApprove": true, "acceptLabel": "Allow once"}
}

func run(scenario string, args []string) error {
	if scenario == "stalled" {
		_ = emit(message{"type": "started"})
		time.Sleep(30 * time.Second) // Deliberately never read the initial request.
		return nil
	}
	if scenario == "close-input" {
		return os.Stdin.Close()
	}
	i := newInput()
	if scenario == "observer-live" || scenario == "observer-once" {
		value, err := i.next()
		if err != nil {
			return err
		}
		result := message{"activity": []any{}, "sessions": []any{}, "errors": message{}}
		if scenario == "observer-live" {
			rows := array(value)
			if len(rows) == 0 {
				return errors.New("Missing observer source")
			}
			id := text(object(rows[0])["id"])
			result["activity"] = []any{message{"id": id + ":one", "sourceID": id, "state": "running", "model": "fixture", "children": []any{}, "owner": "Codex", "pid": 123}}
		}
		if err := emit(result); err != nil {
			return err
		}
		if scenario == "observer-live" {
			// The watcher closes its one-shot request pipe after sending source
			// metadata. Its observer keeps running until the parent terminates it.
			time.Sleep(30 * time.Second)
		}
		return nil
	}
	r, err := i.request()
	if err != nil {
		return err
	}
	switch scenario {
	case "planning-stream":
		if err := require(text(r["mode"]) == "planning"); err != nil {
			return err
		}
		if err := emit(message{"type": "started"}); err != nil {
			return err
		}
		if err := emit(message{"type": "text", "text": "Live planning stream before completion"}); err != nil {
			return err
		}
		if i.wait(30 * time.Second) {
			return nil
		}
		return errors.New("Planning stream fixture was not stopped")
	case "continuation-worker":
		implementation := text(r["mode"]) == "implementation" && r["executionApproved"] == true && len(text(r["executionContext"])) == 64
		chat := text(r["mode"]) == "chat" && r["executionApproved"] == false && r["permissions"] == nil
		if err := require(len(args) == 3 && len(object(r["restoredHistory"])) == 3 && (implementation || chat)); err != nil {
			return err
		}
		if err := appendRecord(args[2], r); err != nil {
			return err
		}
		if r["newSession"] == true {
			_ = emit(message{"type": "session", "sessionID": args[0]})
		} else if err := require(text(object(r["session"])["sessionID"]) == args[0]); err != nil {
			return err
		}
		_ = emit(message{"type": "started"})
		_ = emit(message{"type": "settings", "model": "fixture-actual-model", "effort": "high", "speed": "fast"})
		_ = emit(message{"type": "text", "text": "Synthetic continuation answer " + args[1]})
		if args[1] == "0" {
			id := "e8a1e25a-2b71-4fb9-8122-9ad03a842993"
			_ = emit(approval(id, "command"))
			control, err := i.request()
			if err != nil {
				return err
			}
			if err := require(text(control["type"]) == "approval" && text(control["id"]) == id && control["allow"] == true); err != nil {
				return err
			}
			_ = emit(message{"type": "approvalResolved", "requestID": id})
		}
	case "ui-audit":
		// Interactive synthetic transport for exercising the real native UI.
		// This executable never invokes either provider or executes a command.
		provider := text(object(r["session"])["provider"])
		model := text(r["model"])
		if model == "" {
			model = "synthetic-model"
		}
		speed := "ultrafast"
		if provider == "Claude" {
			speed = "fast"
		}
		_ = emit(message{"type": "settings", "model": model, "effort": "high", "speed": speed})
		if text(r["mode"]) == "implementation" && r["newSession"] == true {
			_ = emit(message{"type": "session", "sessionID": object(r["session"])["sessionID"]})
		}
		_ = emit(message{"type": "started"})
		if text(r["mode"]) == "planning" {
			return auditPlan(i, provider, text(r["prompt"]))
		}
		prompt := strings.ToLower(text(r["prompt"]))
		if strings.Contains(prompt, "complete") {
			_ = emit(message{"type": "text", "text": "Synthetic queued response completed"})
			break
		}
		_ = emit(message{"type": "text", "text": "Synthetic streaming response\n\nThis deliberately long paragraph checks wrapping, readable response spacing, and whether the chat follows its latest content when the window changes width. It uses the native conversation transport without calling Codex or Claude.\n\nThe next input can steer this response, queue a separate prompt, or stop it. The final line must remain visible.\n\nLatest synthetic streaming marker"})
		approvalID := "e8a1e25a-2b71-4fb9-8122-9ad03a842993"
		if strings.Contains(prompt, "question") {
			_ = emit(message{"type": "approval", "id": approvalID, "kind": "question", "title": "Choose the next step", "detail": "", "canApprove": true, "acceptLabel": "Submit answers", "questions": []any{
				message{"id": "path", "header": "Approach", "prompt": "Which approach should the synthetic planner use for this layout review?", "options": []string{"Review the current screen", "Compare both layouts", "Inspect keyboard navigation"}, "multiple": false, "secret": false, "allowText": false},
				message{"id": "note", "header": "Context", "prompt": "What should this review focus on?", "options": []string{}, "multiple": false, "secret": false, "allowText": true},
			}})
		} else if strings.Contains(prompt, "permission") {
			request := approval(approvalID, "permissions")
			request["title"] = "Review synthetic permissions"
			request["detail"] = "Read the synthetic project files for this layout review"
			request["decisions"] = []any{
				message{"id": "decline", "label": "Decline", "allows": false},
				message{"id": "once", "label": "Allow once", "allows": true},
				message{"id": "session", "label": "Allow for this session", "allows": true},
			}
			_ = emit(request)
		} else if strings.Contains(prompt, "approval") {
			_ = emit(approval(approvalID, "command"))
		}
		for {
			control, err := i.request()
			if err != nil {
				return err
			}
			switch text(control["type"]) {
			case "stop":
				return emit(message{"type": "cancelled"})
			case "steer":
				_ = emit(message{"type": "boundary"})
				_ = emit(message{"type": "steerAccepted", "requestID": control["id"]})
				_ = emit(message{"type": "text", "text": "\n\nSteering received: " + text(control["text"])})
				if strings.Contains(strings.ToLower(text(control["text"])), "finish") {
					return emit(message{"type": "done"})
				}
			case "approval":
				_ = emit(message{"type": "approvalResolved", "requestID": approvalID})
				_ = emit(message{"type": "boundary"})
				_ = emit(message{"type": "text", "text": "\n\nSynthetic approval recorded; no command was executed"})
				return emit(message{"type": "done"})
			}
		}
	case "unexpected-session-id":
		_ = emit(message{"type": "session", "sessionID": "22222222-2222-4222-8222-222222222222"})
		_ = emit(message{"type": "started"})
	case "missing-session-id":
		_ = emit(message{"type": "session"})
	case "conflicting-session-id":
		_ = emit(message{"type": "session", "sessionID": "22222222-2222-4222-8222-222222222222"})
		_ = emit(message{"type": "session", "sessionID": "33333333-3333-4333-8333-333333333333"})
	case "duplicate-session-id":
		_ = emit(message{"type": "session", "sessionID": "22222222-2222-4222-8222-222222222222"})
		_ = emit(message{"type": "session", "sessionID": "22222222-2222-4222-8222-222222222222"})
		_ = emit(message{"type": "started"})
	case "restored-history":
		ref := object(r["restoredHistory"])
		if err := require(len(ref) == 3 && text(ref["scope"]) == strings.Repeat("a", 64) && text(ref["receiptSHA256"]) == strings.Repeat("b", 64) && text(ref["transferID"]) == "11111111-1111-4111-8111-111111111111" && r["newSession"] == true && r["executionApproved"] == false && text(r["mode"]) == "chat"); err != nil {
			return err
		}
		_ = emit(message{"type": "session", "sessionID": "22222222-2222-4222-8222-222222222222"})
		_ = emit(message{"type": "started"})
		_ = emit(message{"type": "text", "text": "Continued with attached history"})
	case "message-settings":
		_ = emit(message{"type": "started"})
		_ = emit(message{"type": "settings", "model": "actual-first", "effort": "ultra", "speed": "ultrafast"})
		_ = emit(message{"type": "text", "text": "First response"})
		_ = emit(message{"type": "boundary"})
		_ = emit(message{"type": "settings", "model": "actual-second", "effort": "high", "speed": "fast"})
		_ = emit(message{"type": "text", "text": "Second response"})
		_ = emit(message{"type": "boundary"})
		_ = emit(message{"type": "settings"})
		_ = emit(message{"type": "text", "text": "Unreported settings"})
	case "reported-settings":
		_ = emit(message{"type": "settings", "model": "actual-model", "effort": "ultra", "speed": "ultrafast"})
		_ = emit(message{"type": "started"})
		for {
			control, err := i.request()
			if err != nil {
				return err
			}
			if text(control["type"]) == "stop" {
				return emit(message{"type": "cancelled"})
			}
		}
	case "gated-reply":
		if len(args) != 1 {
			return errors.New("Missing gate folder")
		}
		if err := emit(message{"type": "started"}); err != nil {
			return err
		}
		path := filepath.Join(args[0], text(object(r["session"])["id"]))
		for {
			if _, err := os.Stat(path); err == nil {
				break
			}
			if i.wait(10 * time.Millisecond) {
				return nil
			}
		}
		_ = emit(message{"type": "text", "text": "reply " + text(r["prompt"])})
	case "cancel":
		_ = emit(message{"type": "started"})
		for {
			control, err := i.request()
			if err != nil {
				return err
			}
			if text(control["type"]) == "stop" {
				return emit(message{"type": "cancelled", "text": "Stopped"})
			}
		}
	case "oversized":
		_, err := fmt.Fprintln(os.Stdout, strings.Repeat("x", 5<<20))
		return err
	case "stderr-done":
		_, _ = io.WriteString(os.Stderr, strings.Repeat("x", 1<<20))
	case "late-stop":
		_ = emit(message{"type": "started"})
		_ = emit(message{"type": "text", "text": "finished"})
		_ = emit(message{"type": "done"})
		_, _ = i.next()
		return nil
	case "isolated-plan":
		if err := require(text(r["mode"]) == "planning"); err != nil {
			return err
		}
		_ = emit(message{"type": "started"})
		if i.wait(150 * time.Millisecond) {
			return nil
		}
		for _, event := range []message{{"type": "text", "text": "first"}, {"type": "boundary"}, {"type": "text", "text": "second"}, {"type": "usage", "inputTokens": 123, "outputTokens": true}} {
			_ = emit(event)
		}
	case "overflow-plan":
		for _, event := range []message{{"type": "started"}, {"type": "text", "text": strings.Repeat("a", 8000)}, {"type": "boundary"}, {"type": "text", "text": strings.Repeat("b", 300)}} {
			_ = emit(event)
		}
	case "started-only":
		return emit(message{"type": "started"})
	case "rejected":
		return emit(message{"type": "error", "message": "Session ownership could not be verified"})
	case "complete":
		_ = emit(message{"type": "started"})
		_ = emit(message{"type": "text", "text": "Complete"})
	case "planning":
		if len(args) != 1 {
			return errors.New("Missing planning trace")
		}
		if err := require(text(r["mode"]) == "planning"); err != nil {
			return err
		}
		contexts := array(r["context"])
		if len(contexts) == 0 {
			return errors.New("Missing pinned context")
		}
		messages := array(object(contexts[0])["messages"])
		if len(messages) == 0 || text(object(messages[0])["text"]) != "Pinned context" {
			return errors.New("Context changed")
		}
		phase := "draft"
		if strings.Contains(text(r["prompt"]), "Review the other proposal once") {
			phase = "review"
		} else if strings.Contains(text(r["prompt"]), "Produce one combined plan") {
			phase = "combined"
		}
		provider := text(object(r["session"])["provider"])
		if err := appendRecord(args[0], message{"provider": provider, "phase": phase, "model": r["model"], "effort": r["effort"], "prompt": r["prompt"]}); err != nil {
			return err
		}
		_ = emit(message{"type": "started"})
		if i.wait(250 * time.Millisecond) {
			return nil
		}
		_ = emit(message{"type": "text", "text": provider + " " + phase + " " + text(r["model"])})
		_ = emit(message{"type": "usage", "inputTokens": 123, "outputTokens": 45})
	case "implementation":
		if len(args) != 1 {
			return errors.New("Missing implementation trace")
		}
		if err := require(text(r["mode"]) == "implementation" && r["executionApproved"] == true && text(r["executionContext"]) == strings.Repeat("a", 64)); err != nil {
			return err
		}
		if err := appendRecord(args[0], r); err != nil {
			return err
		}
		if r["newSession"] == true {
			_ = emit(message{"type": "session", "sessionID": object(r["session"])["sessionID"]})
		}
		_ = emit(message{"type": "started"})
		if i.wait(200 * time.Millisecond) {
			return nil
		}
		_ = emit(message{"type": "text", "text": "Implemented with " + text(r["model"])})
	case "question-approval":
		if len(args) != 2 {
			return errors.New("Missing approval identities")
		}
		if err := require(text(r["mode"]) == "implementation" && r["executionApproved"] == true && r["newSession"] == true); err != nil {
			return err
		}
		_ = emit(message{"type": "session", "sessionID": args[0]})
		_ = emit(message{"type": "started"})
		_ = emit(message{"type": "approval", "id": args[1], "kind": "question", "title": "Choose", "detail": "", "canApprove": true, "acceptLabel": "Submit answers", "questions": []any{message{"id": "answer", "header": "Format", "prompt": "Which format?", "options": []any{}, "multiple": false, "secret": false, "allowText": true}}})
		answer, err := i.request()
		if err != nil {
			return err
		}
		if err := require(text(answer["id"]) == args[1] && text(answer["type"]) == "approval" && len(object(answer["answers"])) == 0); err != nil {
			return err
		}
		_ = emit(message{"type": "approvalRejected", "requestID": args[1], "message": "Answer the question"})
		answer, err = i.request()
		if err != nil {
			return err
		}
		selected := array(object(answer["answers"])["answer"])
		if err := require(len(selected) == 1 && text(selected[0]) == "line 1\nline 2"); err != nil {
			return err
		}
		_ = emit(message{"type": "approvalResolved", "requestID": args[1]})
		_ = emit(message{"type": "text", "text": "Implemented"})
	case "command-approval", "unexpected-approval":
		if len(args) != 1 {
			return errors.New("Missing approval identity")
		}
		_ = emit(approval(args[0], "command"))
		if scenario == "unexpected-approval" {
			return nil
		}
		control, err := i.request()
		if err != nil {
			return err
		}
		if err := require(text(control["type"]) == "stop"); err != nil {
			return err
		}
		return emit(message{"type": "cancelled"})
	case "permission-approval":
		if len(args) != 2 {
			return errors.New("Missing permission trace and identity")
		}
		if err := require(text(r["mode"]) == "implementation" && text(object(r["permissions"])["sandbox"]) == "read-only"); err != nil {
			return err
		}
		if err := appendRecord(args[0], r); err != nil {
			return err
		}
		_ = emit(message{"type": "permissions", "text": "read-only · on-request · user"})
		_ = emit(message{"type": "started"})
		request := approval(args[1], "command")
		request["acceptLabel"] = "Allow"
		request["decisions"] = []any{message{"id": "acceptForSession", "label": "Allow for this session", "allows": true}, message{"id": "cancel", "label": "Decline and stop", "allows": false}}
		_ = emit(request)
		answer, err := i.request()
		if err != nil {
			return err
		}
		if err := require(text(answer["decision"]) == "acceptForSession" && answer["allow"] == true); err != nil {
			return err
		}
		_ = emit(message{"type": "approvalResolved", "requestID": args[1]})
		_ = emit(message{"type": "text", "text": "Done"})
	default:
		return errors.New("Unknown native test scenario")
	}
	return emit(message{"type": "done"})
}

// Keep planning independent of the chat fixture's prompt-triggered approvals.
// Planning instructions themselves contain words such as "permission".
func auditPlan(i input, provider, prompt string) error {
	phase := "Independent plan"
	paragraphs := []string{
		"Keep Main chat and its subagents together, above the ten most recent sessions.",
		"Reuse the existing model controls and preserve the wording on every card.",
		"Check keyboard navigation, narrow windows and the latest streamed message before implementation.",
	}
	if strings.Contains(prompt, "Review the other proposal once") {
		phase = "Review of the other plan"
		paragraphs = []string{
			"Both plans preserve the existing interface and keep child conversations separate.",
			"Use the shared transcript viewport and test switching sessions during streaming.",
			"Open decision: keep older context behind @ search or add a separate history browser. The first option keeps this release smaller.",
		}
	} else if strings.Contains(prompt, "Produce one combined plan") {
		phase = "Combined plan"
		paragraphs = []string{
			"1. Keep Main chat, Subagents and Recent sessions in that order, with the same provider icons.",
			"2. Reuse the model, effort and speed controls and the width-constrained transcript viewport.",
			"3. Verify streaming, keyboard navigation, session switching and cancellation at the minimum window size.",
			"Decision to review: older sessions remain available through @ search. A separate history browser can be considered later.",
		}
	}
	content := provider + " · " + phase + "\n\n"
	if err := emit(message{"type": "text", "text": content}); err != nil {
		return err
	}
	for _, paragraph := range paragraphs {
		if i.wait(650 * time.Millisecond) {
			return nil
		}
		content += paragraph + "\n\n"
		if err := emit(message{"type": "text", "text": content}); err != nil {
			return err
		}
	}
	if err := emit(message{"type": "usage", "inputTokens": 123, "outputTokens": 45}); err != nil {
		return err
	}
	return emit(message{"type": "done"})
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Choose a native test scenario")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
