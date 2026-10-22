package localcli

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeActivityUsesVerifiedLiveRowsAndKeepsChildHistory(t *testing.T) {
	a := providerFixture(t, "reply")
	root := filepath.Join(a.Config.Home, ".claude")
	path := filepath.Join(root, "projects/p/parent.jsonl")
	write(t, path, record("one", "Main response"))
	write(t, filepath.Join(root, "sessions/stale.json"), `{"sessionId":"stale","status":"busy","pid":1}`)
	write(t, filepath.Join(root, "projects/p/parent/subagents/agent-review.jsonl"), record("review", "Review response")+`{"type":"system","subtype":"stop_hook_summary"}`+"\n")
	source := sourceFor(root, "Claude")
	reply, _ := json.Marshal([]map[string]any{{"sessionId": "parent", "status": "busy", "name": "Live parent", "cwd": a.Config.Home, "pid": 123}})
	a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_REPLY="+string(reply))
	result, err := a.Activity(context.Background(), ActivityRequest{Sources: []Source{source}})
	if err != nil || len(result.Activity) != 1 || len(result.Sessions) != 1 || len(result.Errors) != 0 {
		t.Fatal(result, err)
	}
	live := result.Activity[0]
	if live.State != "running" || live.Owner != "Claude Code" || live.PID != 123 || live.Model != "fixture-model" || len(live.Children) != 1 || live.Children[0].Status != "done" {
		t.Fatal(live)
	}
	if len(result.Models[source.ID]) != 15 || result.ObservedAt == 0 {
		t.Fatal(result)
	}
	if err := a.assertClaudeAvailable(context.Background(), result.Sessions[0]); err == nil || !strings.Contains(err.Error(), "open in Claude Code") {
		t.Fatal("External session was available for takeover", err)
	}
}

func TestActivityStatesAndIdleFocus(t *testing.T) {
	for _, state := range []struct{ native, expected string }{{"idle", "idle"}, {"waiting", "waiting"}, {"waiting_for_input", "waiting"}, {"future-state", "unknown"}} {
		t.Run(state.native, func(t *testing.T) {
			a := providerFixture(t, "reply")
			root := filepath.Join(a.Config.Home, ".claude")
			write(t, filepath.Join(root, "projects/p/parent.jsonl"), record("one", "Response"))
			write(t, filepath.Join(root, "projects/p/parent/subagents/agent-child.jsonl"), record("child", "Child"))
			write(t, filepath.Join(root, "sessions/registry.json"), `{}`)
			a.Config.Environment = append(a.Config.Environment, `CAMP_LOCAL_TEST_REPLY=[{"sessionId":"parent","status":"`+state.native+`"}]`)
			source := sourceFor(root, "Claude")
			result, err := a.Activity(context.Background(), ActivityRequest{Sources: []Source{source}, SelectedID: source.ID + ":parent"})
			if err != nil || len(result.Activity) != 1 || result.Activity[0].State != state.expected || len(result.Activity[0].Children) != 1 || result.Activity[0].Children[0].Status != "unknown" {
				t.Fatal(result, err)
			}
			if err := a.assertClaudeAvailable(context.Background(), result.Sessions[0]); err == nil {
				t.Fatal("Idle external ownership did not block resume")
			}
		})
	}
}

func TestActivityStaleRegistryAndErrorsDoNotInventRunningSessions(t *testing.T) {
	for _, reply := range []string{`[]`, `{}`, `not JSON`, `null`, `[1,null,{"sessionId":"../outside"}]`} {
		t.Run(reply, func(t *testing.T) {
			a := providerFixture(t, "reply")
			root := filepath.Join(a.Config.Home, ".claude")
			write(t, filepath.Join(root, "sessions/stale.json"), `{"sessionId":"parent","status":"busy","pid":1}`)
			write(t, filepath.Join(root, "projects/p/parent.jsonl"), record("one", "Response"))
			a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_REPLY="+reply)
			result, err := a.Activity(context.Background(), ActivityRequest{Sources: []Source{sourceFor(root, "Claude")}})
			if err != nil || len(result.Activity) != 0 || len(result.Sessions) != 0 {
				t.Fatal(result, err)
			}
			if reply != `[]` && !strings.HasPrefix(reply, "[") && len(result.Errors) != 1 {
				t.Fatal("Invalid live status was silently accepted", result)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture(t).Activity(ctx, ActivityRequest{Sources: []Source{{Provider: "Claude"}}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestActivityFailureIsProfileScopedAndSafe(t *testing.T) {
	a := providerFixture(t, "failure")
	root := filepath.Join(a.Config.Home, ".claude")
	write(t, filepath.Join(root, "sessions/registry.json"), `{}`)
	source := sourceFor(root, "Claude")
	result, err := a.Activity(context.Background(), ActivityRequest{Sources: []Source{source, sourceFor(filepath.Join(a.Config.Home, ".codex"), "Codex")}})
	if err != nil || len(result.Errors) != 1 || result.Errors[source.ID] == "" || strings.Contains(result.Errors[source.ID], "private-provider") || len(result.Models) != 2 {
		t.Fatal(result, err)
	}
	for _, input := range []string{`[] {}`, `null`, `{}`, `{"sources":null}`} {
		if _, err := a.Run(context.Background(), "activity", strings.NewReader(input)); err == nil {
			t.Fatal("Invalid activity request accepted", input)
		}
	}
	for _, input := range []string{`[]`, `{"sources":[],"selectedID":"none"}`} {
		if _, err := a.Run(context.Background(), "activity", strings.NewReader(input)); err != nil {
			t.Fatal(input, err)
		}
	}
}
