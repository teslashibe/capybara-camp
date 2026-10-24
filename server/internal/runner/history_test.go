package runner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
func readFixtureHistory(server map[string]any, record string, child ...bool) error {
	childHistory := len(child) > 0 && child[0]
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
	id := ""
	if childHistory {
		listed, e := call("tools/call", map[string]any{"name": "list_subagents", "arguments": map[string]any{"limit": 1}})
		if e != nil {
			return e
		}
		text := listed["content"].([]any)[0].(map[string]any)["text"].(string)
		var catalog struct {
			Children []struct {
				ID string `json:"session_id"`
			}
		}
		if e = json.Unmarshal([]byte(text), &catalog); e != nil || len(catalog.Children) != 1 {
			return fmt.Errorf("child list unavailable")
		}
		id = catalog.Children[0].ID
	} else {
		id = properties["session_id"].(map[string]any)["enum"].([]any)[0].(string)
	}
	if id == "" {
		return fmt.Errorf("history source missing")
	}
	if len(child) > 1 && child[1] {
		result, err := call("tools/call", map[string]any{"name": "read_session", "arguments": map[string]any{"session_id": id, "limit": 1}})
		if err != nil {
			return err
		}
		text := result["content"].([]any)[0].(map[string]any)["text"].(string)
		var page localcli.HistoryPage
		if err = json.Unmarshal([]byte(text), &page); err != nil || len(page.Messages) != 1 || len(page.Messages[0].Attachments) != 1 {
			return fmt.Errorf("attachment descriptor missing")
		}
		message := page.Messages[0]
		ref := message.Attachments[0]
		fixtureRecord(record, "attachment:descriptor")
		result, err = call("tools/call", map[string]any{"name": "read_attachment", "arguments": map[string]any{"session_id": id, "message_id": message.ID, "attachment_id": ref.ID, "source_digest": ref.SourceDigest}})
		if err != nil {
			return err
		}
		content := result["content"].([]any)[0].(map[string]any)
		fixtureRecord(record, "attachment:response")
		encoded, _ := content["data"].(string)
		body, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || content["type"] != "image" || len(body) != 16<<20 || fixtureAttachmentHash(body) != ref.SHA256 {
			return fmt.Errorf("attachment body changed")
		}
		fixtureRecord(record, "attachment:16MiB:verified")
		return nil
	}
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
			if childHistory {
				fixtureRecord(record, "child-history:150:complete")
			} else {
				fixtureRecord(record, "history:150:complete")
			}
			return nil
		}
		offset, textOffset = *page.NextOffset, page.NextTextOffset
	}
	return fmt.Errorf("history never ended")
}

func TestProviderReadsChildHistoryThroughCampTool(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			f, _, workspace, record := turnFixture(t, provider, "child-history")
			ctx := t.Context()
			id := uuid.NewString()
			tx, err := f.api.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `INSERT INTO sessions(id,camp_id,project_id,profile_id,owner_user_id,owner_device_id,local_session_id,title,state,stage,shared)
 SELECT $1::uuid,camp_id,project_id,profile_id,owner_user_id,owner_device_id,$1::text,'Child fixture','idle','Done',false FROM sessions WHERE id=$2`, id, workspace.SessionID); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO session_children(session_id,camp_id,parent_session_id,source_key) VALUES($1,$2,$3,$4)`, id, workspace.CampID, workspace.SessionID, strings.Repeat("a", 64)); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO session_child_revisions(parent_session_id,child_session_id,revision,transcript_revision,source_revision,name,phase,workflow,state,model,complete)
 VALUES($1,$2,1,1,$3,'Child fixture','','','done','fixture',true)`, workspace.SessionID, id, strings.Repeat("b", 64)); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 150; i++ {
				text := "history"
				switch i {
				case 0:
					text = "first history"
				case 149:
					text = "last history"
				case 75:
					text = strings.Repeat("😀", 75000)
				}
				messageID := fmt.Sprintf("history-%d", i)
				if _, err = tx.Exec(ctx, `INSERT INTO messages(session_id,local_id,ordinal) VALUES($1,$2,$3)`, id, messageID, i); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(ctx, `INSERT INTO message_revisions(session_id,local_id,revision,role,text) VALUES($1,$2,1,'assistant',$3)`, id, messageID, text); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = tx.Exec(ctx, `UPDATE sessions SET transcript_revision=1 WHERE id IN ($1,$2)`, id, workspace.SessionID); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			request, err := f.store.CreateRequest(ctx, f.owner, workspace.CampID, workspace.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Read the child conversation"})
			if err != nil {
				t.Fatal(err)
			}
			cancel, done := startWorker(t, f.worker)
			eventually(t, func() bool {
				r, e := f.store.Request(ctx, f.owner, workspace.CampID, request.ID)
				return e == nil && r.State == "completed"
			})
			stopWorker(t, cancel, done)
			calls, err := os.ReadFile(record)
			if err != nil || strings.Count(string(calls), "turn\n") != 1 || !strings.Contains(string(calls), "child-history:150:complete") {
				t.Fatal("Provider did not read child history", string(calls), err)
			}
		})
	}
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

func fixtureAttachmentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestProviderReadsAttachmentThroughCampTool(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			f, _, workspace, record := turnFixture(t, provider, "attachment-history")
			request, err := f.store.CreateRequest(t.Context(), f.owner, workspace.CampID, workspace.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Inspect the attached image"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if t.Failed() {
					raw, _ := os.ReadFile(record)
					t.Logf("synthetic provider: %s", raw)
					if state, e := f.worker.checkout.loadTurn(); e == nil && state != nil {
						t.Logf("runner phase: %s", state.Phase)
					}
					r, e := f.store.Request(context.Background(), f.owner, workspace.CampID, request.ID)
					if e == nil {
						t.Logf("request state: %s", r.State)
					}
				}
			}()
			workerContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			done := make(chan error, 1)
			go func() { done <- f.worker.Run(workerContext); close(done) }()
			defer stopWorker(t, cancel, done)
			completed := false
			// The maximum-sized body traverses two authenticated readers and a
			// subprocess under the race detector; allow their bounded timeouts.
			for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
				r, e := f.store.Request(t.Context(), f.owner, workspace.CampID, request.ID)
				if e == nil && r.State == "completed" {
					completed = true
					break
				}
				if e == nil && (r.State == "failed" || r.State == "cancelled") {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if !completed {
				t.Fatal("Attachment turn did not complete")
			}
			calls, err := os.ReadFile(record)
			if err != nil || strings.Count(string(calls), "turn\n") != 1 || !strings.Contains(string(calls), "attachment:16MiB:verified") {
				t.Fatal("Provider did not receive complete attachment", string(calls), err)
			}
		})
	}
}
