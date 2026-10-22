package localcli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func childHistoryFixture(t *testing.T, provider string) (*App, Session, string) {
	t.Helper()
	a := fixture(t)
	root := filepath.Join(a.Config.Home, strings.ToLower(provider))
	parent := Session{Provider: provider, Root: root, SessionID: "parent", Path: filepath.Join(root, "projects/p/parent.jsonl")}
	write(t, parent.Path, record("parent-message", "Only the parent"))
	childPath := filepath.Join(strings.TrimSuffix(parent.Path, ".jsonl"), "subagents/agent-one.jsonl")
	if provider == "Claude" {
		var text strings.Builder
		for i := 0; i < 230; i++ {
			raw, _ := json.Marshal(map[string]any{"type": "assistant", "uuid": "message-" + strings.Repeat("x", i), "isSidechain": true,
				"message": map[string]any{"content": "Child history", "model": "child-model"}})
			text.Write(raw)
			text.WriteByte('\n')
		}
		write(t, childPath, text.String())
	} else {
		childPath = filepath.Join(root, "sessions/child.jsonl")
		write(t, childPath, "")
		db := makeDB(t, filepath.Join(root, "state_5.sqlite"), `CREATE TABLE threads(id,title,rollout_path,updated_at,model,agent_nickname,agent_role); CREATE TABLE thread_spawn_edges(parent_thread_id,child_thread_id);`)
		if _, err := db.Exec(`INSERT INTO threads VALUES('one','Child',?,2,'child-model','Maple','reviewer'); INSERT INTO thread_spawn_edges VALUES('parent','one')`, childPath); err != nil {
			t.Fatal(err)
		}
		history := makeDB(t, filepath.Join(root, "thread_history_1.sqlite"), `CREATE TABLE thread_items(thread_id,item_id,item_json,item_type,rollout_ordinal)`)
		for i := 0; i < 230; i++ {
			raw, _ := json.Marshal(map[string]any{"type": "agentMessage", "text": "Child history", "model": "child-model", "effort": "high"})
			if _, err := history.Exec(`INSERT INTO thread_items VALUES('one',?,?,'agentMessage',?)`, "message-"+strings.Repeat("x", i), string(raw), i); err != nil {
				t.Fatal(err)
			}
		}
	}
	return a, parent, childPath
}

func TestChildHistoryExportsPinnedPagesWithProvenance(t *testing.T) {
	for _, provider := range []string{"Claude", "Codex"} {
		t.Run(provider, func(t *testing.T) {
			a, parent, path := childHistoryFixture(t, provider)
			catalog, err := a.ChildHistory(t.Context(), parent)
			if err != nil || len(catalog.Sources) != 1 || len(catalog.Revision) != 64 {
				t.Fatal(catalog, err)
			}
			source := catalog.Sources[0]
			if source.Status == "running" || source.Model != "child-model" || len(source.ID) != 64 {
				t.Fatal("Incorrect child provenance", source)
			}
			raw, _ := json.Marshal(catalog)
			if bytes.Contains(raw, []byte(parent.Root)) || bytes.Contains(raw, []byte(path)) {
				t.Fatal("Child catalog exposed a filesystem path")
			}
			after, count := int64(-1), 0
			seen := map[string]bool{}
			for pages := 0; pages < 10; pages++ {
				page, err := a.ExportChildHistory(t.Context(), parent, source.ID, source.Revision, after, 70)
				if err != nil || page.ParentID != catalog.ParentID || page.Source != source || page.Page.Fingerprint != source.Revision {
					t.Fatal("Snapshot changed", page, err)
				}
				for _, m := range page.Page.Messages {
					if seen[m.ID] || m.Text != "Child history" || m.Model != "child-model" || m.Removed {
						t.Fatal("Lost or duplicated child message", m)
					}
					seen[m.ID] = true
					count++
				}
				if !page.Page.HasMore {
					break
				}
				if page.Page.After <= after {
					t.Fatal("Cursor did not advance")
				}
				after = page.Page.After
			}
			if count != 230 {
				t.Fatal("Incomplete child history", count)
			}
			other := parent
			other.SessionID = "unrelated"
			if _, err := a.ExportChildHistory(t.Context(), other, source.ID, source.Revision, -1, 200); err == nil {
				t.Fatal("An unrelated parent read this child")
			}
			if provider == "Codex" {
				db := makeDB(t, filepath.Join(parent.Root, "thread_history_1.sqlite"), "")
				if _, err := db.Exec(`DELETE FROM thread_items WHERE thread_id='one' AND rollout_ordinal=0`); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(record("new", "Changed after review")), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := a.ExportChildHistory(t.Context(), parent, source.ID, source.Revision, -1, 200); err == nil {
				t.Fatal("Changed child accepted the old snapshot")
			}
		})
	}
}

func TestChildHistoryRejectsEscapingFilesAndMalformedRequests(t *testing.T) {
	a, parent, path := childHistoryFixture(t, "Claude")
	outside := filepath.Join(a.Config.Home, "outside.jsonl")
	write(t, outside, record("private", "Other history"))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ChildHistory(t.Context(), parent); err == nil {
		t.Fatal("A linked transcript escaped the child folder")
	}
	for _, body := range []string{`{}`, `{"path":"/tmp/other"}`, `{} {}`, `{"session":{},"limit":1.5}`} {
		if _, err := a.Run(t.Context(), "child-export-page", strings.NewReader(body)); err == nil {
			t.Fatal("Accepted malformed child export")
		}
	}
}

func TestChildHistoryProfileIsolationAndLimits(t *testing.T) {
	a, parent, _ := childHistoryFixture(t, "Claude")
	catalog, err := a.ChildHistory(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	source := catalog.Sources[0]
	other := parent
	other.Root = filepath.Join(a.Config.Home, "other")
	other.Path = filepath.Join(other.Root, "projects/p/parent.jsonl")
	write(t, other.Path, record("parent-message", "Other parent"))
	write(t, strings.TrimSuffix(other.Path, ".jsonl")+"/subagents/agent-one.jsonl", record("other", "Other profile"))
	second, err := a.ChildHistory(t.Context(), other)
	if err != nil || second.ParentID == catalog.ParentID || second.Sources[0].ID == source.ID {
		t.Fatal("Profile identity was lost", second, err)
	}
	if _, err = a.ExportChildHistory(t.Context(), other, source.ID, source.Revision, -1, 200); err == nil {
		t.Fatal("Another profile accepted this child")
	}
	for _, args := range [][2]int64{{-2, 1}, {-1, 0}, {-1, 201}} {
		if _, err = a.ExportChildHistory(t.Context(), parent, source.ID, source.Revision, args[0], int(args[1])); err == nil {
			t.Fatal("Invalid child cursor accepted")
		}
	}
	if _, err = childrenForLimit(t.Context(), parent, false, 1); err != nil {
		t.Fatal(err)
	}
	write(t, strings.TrimSuffix(parent.Path, ".jsonl")+"/subagents/agent-two.jsonl", record("second", "Another child"))
	if _, err = childrenForLimit(t.Context(), parent, false, 1); err == nil {
		t.Fatal("An overflowing catalog silently omitted a child")
	}
	latest, err := a.ChildHistory(t.Context(), parent)
	if err != nil || latest.Revision == catalog.Revision {
		t.Fatal("Child membership did not change the review revision", err)
	}
}

func TestChildHistoryUsesCodexDatabaseAfterRolloutRotation(t *testing.T) {
	a, parent, path := childHistoryFixture(t, "Codex")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	catalog, err := a.ChildHistory(t.Context(), parent)
	if err != nil || len(catalog.Sources) != 1 {
		t.Fatal("Database history was lost with its rollout", err)
	}
	source := catalog.Sources[0]
	page, err := a.ExportChildHistory(t.Context(), parent, source.ID, source.Revision, -1, 200)
	if err != nil || len(page.Page.Messages) != 200 || !page.Page.HasMore {
		t.Fatal("Database-only child did not export", err)
	}
}

func TestChildHistoryCodexFallbackChecksRecordedIdentity(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, "codex")
	path := filepath.Join(root, "sessions/child.jsonl")
	parent := Session{Provider: "Codex", Root: root, SessionID: "parent"}
	db := makeDB(t, filepath.Join(root, "state_5.sqlite"), `CREATE TABLE threads(id,title,rollout_path,updated_at); CREATE TABLE thread_spawn_edges(parent_thread_id,child_thread_id);`)
	if _, err := db.Exec(`INSERT INTO threads VALUES('one','Child',?,1); INSERT INTO thread_spawn_edges VALUES('parent','one')`, path); err != nil {
		t.Fatal(err)
	}
	write(t, path, `{"type":"session_meta","payload":{"id":"one"}}`+"\n"+`{"type":"response_item","payload":{"type":"message","id":"first","role":"assistant","content":[{"type":"output_text","text":"Fallback history"}]}}`+"\n")
	catalog, err := a.ChildHistory(t.Context(), parent)
	if err != nil || len(catalog.Sources) != 1 {
		t.Fatal(err)
	}
	source := catalog.Sources[0]
	page, err := a.ExportChildHistory(t.Context(), parent, source.ID, source.Revision, -1, 200)
	if err != nil || len(page.Page.Messages) != 1 || page.Page.Messages[0].Text != "Fallback history" {
		t.Fatal(page, err)
	}
	write(t, path, `{"type":"session_meta","payload":{"id":"someone-else"}}`+"\n")
	if _, err := a.ChildHistory(t.Context(), parent); err == nil {
		t.Fatal("A foreign fallback rollout was accepted")
	}
}
