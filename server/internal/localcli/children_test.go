package localcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCodexChildGraphIncludesDescendantsAndStopsAtCycles(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, ".codex")
	makeDB(t, filepath.Join(root, "state_5.sqlite"), `CREATE TABLE threads(id,title,rollout_path,updated_at,model,agent_nickname,agent_role);
CREATE TABLE thread_spawn_edges(parent_thread_id,child_thread_id);
INSERT INTO threads VALUES('parent','Parent','',1,'','', ''),('child','Child','c.jsonl',2,'model-small','Maple','explorer'),('grandchild','Grandchild','g.jsonl',3,'model-medium',NULL,NULL),('unrelated','Other','',4,'','', '');
INSERT INTO thread_spawn_edges VALUES('parent','child'),('child','grandchild'),('grandchild','child'),('grandchild','parent');`)
	rows, err := childrenFor(context.Background(), Session{Provider: "Codex", Root: root, SessionID: "parent"}, true)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	byID := map[string]Child{}
	for _, row := range rows {
		byID[row.ID] = row
		if row.Status != "unknown" {
			t.Fatal("Stored child was reported as live", row)
		}
	}
	if byID["child"].Name != "Maple" || byID["child"].Phase != "explorer" || byID["child"].Model != "model-small" || byID["grandchild"].Name != "Grandchild" {
		t.Fatal(rows)
	}
}

func TestCodexChildrenTolerateOlderStores(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, ".codex")
	makeDB(t, filepath.Join(root, "state_5.sqlite"), `CREATE TABLE threads(id,title,rollout_path,updated_at);`)
	rows, err := childrenFor(context.Background(), Session{Provider: "Codex", Root: root, SessionID: "parent"}, true)
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}

func TestClaudeChildWorkflowActivityAndTerminalState(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(a.Config.Home, ".claude/projects/p/parent.jsonl")
	folder := strings.TrimSuffix(path, ".jsonl")
	create := func(workflow, id, journal string, updated time.Time) string {
		t.Helper()
		base := filepath.Join(folder, "subagents/workflows", workflow)
		write(t, filepath.Join(base, "agent-"+id+".jsonl"), record("one", "Review")+`{"message":{"model":"<synthetic>"}}`+"\n")
		write(t, filepath.Join(base, "journal.jsonl"), journal)
		if err := os.Chtimes(filepath.Join(base, "journal.jsonl"), updated, updated); err != nil {
			t.Fatal(err)
		}
		return base
	}
	create("old", "first", `{"type":"started","agentId":"first"}`+"\n", time.Unix(1, 0))
	create("current", "second", `{"type":"started","agentId":"second","label":"Review changes","phase":"review"}`+"\n", time.Unix(2, 0))
	create("finished", "third", `{"type":"started","agentId":"third"}`+"\n"+`{"type":"result","agentId":"third"}`+"\n", time.Unix(0, 1))
	write(t, filepath.Join(folder, "workflows/finished.json"), `{"status":"completed"}`)
	write(t, filepath.Join(folder, "workflows/scripts/review-current.js"), "throw new Error('Never execute source files');")
	write(t, filepath.Join(folder, "subagents/agent-alone.jsonl"), `{"type":"system","subtype":"stop_hook_summary"}`+"\n")
	write(t, filepath.Join(folder, "subagents/agent-alone.meta.json"), `{"description":"Independent helper","workflowPhase":"research"}`)
	parent := Session{Provider: "Claude", Root: a.Config.Home, SessionID: "parent", Path: path}
	get := func(busy bool) map[string]Child {
		t.Helper()
		rows, err := childrenFor(context.Background(), parent, busy)
		if err != nil || len(rows) != 4 {
			t.Fatal(rows, err)
		}
		byID := map[string]Child{}
		for _, row := range rows {
			byID[row.ID] = row
		}
		if busy && byID["current:second"].Status == "running" && rows[0].ID != "current:second" {
			t.Fatal("Live child not first", rows)
		}
		return byID
	}
	rows := get(true)
	if rows["current:second"].Status != "running" || rows["old:first"].Status != "unknown" || rows["finished:third"].Status != "done" || rows[":alone"].Status != "done" {
		t.Fatal(rows)
	}
	if rows["current:second"].Workflow != "review" || rows["current:second"].Name != "Review changes" || rows["current:second"].Model != "fixture-model" || rows[":alone"].Name != "Independent helper" {
		t.Fatal(rows)
	}
	if get(false)["current:second"].Status != "unknown" {
		t.Fatal("Idle parent produced a running child")
	}
	write(t, filepath.Join(folder, "workflows/current.json"), `{"status":"cancelled"}`)
	rows = get(true)
	if rows["current:second"].Status != "cancelled" || rows["old:first"].Status != "unknown" {
		t.Fatal(rows)
	}
}

func TestMetadataReadLimitDoesNotReadFollowingOversizedRecord(t *testing.T) {
	var got []string
	err := scanLines(context.Background(), strings.NewReader(`{"name":"first"}`+"\n"+strings.Repeat("x", maxRecord+1)), false, 1, func(_ int, d map[string]any) error {
		got = append(got, str(d["name"]))
		return nil
	})
	if err != nil || !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatal(got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scanLines(ctx, strings.NewReader(""), false, 0, func(int, map[string]any) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLegacyReadFailureRetainsSourceAndCanceledScanDoesNotSave(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, ".codex")
	path := filepath.Join(root, "sessions/broken.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "gone"), path); err != nil {
		t.Fatal(err)
	}
	result, err := a.Scan(context.Background())
	canonicalRoot, _ := canonical(root, a.Config.Home)
	if err != nil || !reflect.DeepEqual(result.FailedRoots, []string{canonicalRoot}) {
		t.Fatal(result, err)
	}
	cachePath := filepath.Join(a.Config.Support, "import-cache.json")
	write(t, cachePath, `{"marker":{}}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Scan(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(cachePath); err != nil || string(data) != `{"marker":{}}` {
		t.Fatal("Cancelled scan wrote cache", err)
	}
}
