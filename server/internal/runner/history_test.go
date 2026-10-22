package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

// The synthetic provider calls the actual context-tool executable. The tool
// reaches its parent runner's narrow loopback reader; only the parent holds
// the runner credential used for the real authenticated Camp history API.
func readFixtureHistory(server map[string]any, record string) error {
	path, _ := server["command"].(string)
	var args []string
	for _, arg := range server["args"].([]any) {
		value, _ := arg.(string)
		args = append(args, value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, args...)
	input, err := command.StdinPipe()
	if err != nil {
		return err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err = command.Start(); err != nil {
		return err
	}
	defer func() { input.Close(); _ = command.Wait() }()
	write, read := json.NewEncoder(input), json.NewDecoder(output)
	call := func(method string, params any) (map[string]any, error) {
		if err := write.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}); err != nil {
			return nil, err
		}
		var response map[string]any
		if err := read.Decode(&response); err != nil {
			return nil, err
		}
		result, _ := response["result"].(map[string]any)
		if result == nil || result["isError"] == true {
			return nil, fmt.Errorf("context read failed")
		}
		return result, nil
	}
	list, err := call("tools/list", map[string]any{})
	if err != nil {
		return err
	}
	tools := list["tools"].([]any)
	tool := tools[0].(map[string]any)
	properties := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
	id := properties["session_id"].(map[string]any)["enum"].([]any)[0].(string)
	offset, textOffset := int64(0), 0
	contents := map[string]string{}
	for calls := 0; calls < 100; calls++ {
		result, err := call("tools/call", map[string]any{"name": "read_session", "arguments": map[string]any{"session_id": id, "offset": offset, "text_offset": textOffset, "limit": 120}})
		if err != nil {
			return err
		}
		text := result["content"].([]any)[0].(map[string]any)["text"].(string)
		var page localcli.HistoryPage
		if err = json.Unmarshal([]byte(text), &page); err != nil {
			return err
		}
		for _, message := range page.Messages {
			if !strings.Contains(message.Source, "capybara://session/") {
				return fmt.Errorf("missing history provenance")
			}
			contents[message.ID] += message.Text
		}
		if page.NextOffset == nil {
			if len(contents) != 150 || contents["history-0"] != "first history" || contents["history-149"] != "last history" || contents["history-75"] != strings.Repeat("😀", 75000) {
				return fmt.Errorf("incomplete history")
			}
			fixtureRecord(record, "history:150:complete")
			return nil
		}
		offset, textOffset = *page.NextOffset, page.NextTextOffset
	}
	return fmt.Errorf("history never ended")
}

func TestProviderReadsCompleteHistoryThroughCampTool(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			f, _, workspace, record := turnFixture(t, provider, "history")
			defer func() {
				if t.Failed() {
					raw, _ := os.ReadFile(record)
					t.Logf("provider fixture: %s", raw)
					if r, e := f.worker.checkout.loadTurn(); e == nil && r != nil {
						t.Logf("worker phase: %s", r.Phase)
					}
				}
			}()
			tx, err := f.api.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			for i := 0; i < 150; i++ {
				id, text := fmt.Sprintf("history-%d", i), "history"
				switch i {
				case 0:
					text = "first history"
				case 149:
					text = "last history"
				case 75:
					text = strings.Repeat("😀", 75000)
				}
				if _, err = tx.Exec(t.Context(), `INSERT INTO messages(session_id,local_id,ordinal) VALUES($1,$2,$3)`, workspace.SessionID, id, i); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(t.Context(), `INSERT INTO message_revisions(session_id,local_id,revision,role,text) VALUES($1,$2,1,'assistant',$3)`, workspace.SessionID, id, text); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = tx.Exec(t.Context(), `UPDATE sessions SET transcript_revision=1 WHERE id=$1`, workspace.SessionID); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			request, err := f.store.CreateRequest(t.Context(), f.owner, workspace.CampID, workspace.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Continue with full history"})
			if err != nil {
				t.Fatal(err)
			}
			cancel, done := startWorker(t, f.worker)
			eventually(t, func() bool {
				r, e := f.store.Request(t.Context(), f.owner, workspace.CampID, request.ID)
				return e == nil && r.State == "completed"
			})
			stopWorker(t, cancel, done)
			calls, err := os.ReadFile(record)
			if err != nil || strings.Count(string(calls), "turn\n") != 1 || !strings.Contains(string(calls), "history:150:complete") {
				t.Fatal("Provider did not read complete history", string(calls), err)
			}
		})
	}
}
