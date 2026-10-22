package localcli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/config"
)

func fixture(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	return New(config.Local{Home: filepath.Join(root, "home"), UsersDir: filepath.Join(root, "users"), Support: filepath.Join(root, "support")})
}
func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func makeDB(t *testing.T, path, ddl string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	return db
}
func sourceFor(root, provider string) Source {
	return Source{ID: provider + ":" + root, Provider: provider, Root: root, Label: filepath.Base(root)}
}
func record(id, text string) string {
	data, _ := json.Marshal(map[string]any{"type": "assistant", "uuid": id, "message": map[string]any{"content": text, "model": "fixture-model"}})
	return string(data) + "\n"
}

func TestTitleBoundAndInstructionFiltering(t *testing.T) {
	for _, raw := range []string{strings.Repeat("word ", 500000), strings.Repeat("x", 2000000), strings.Repeat(" First\tsecond\nthird ", 100000), strings.Repeat(" ", 100000) + "First\u2003second", "first" + strings.Repeat(" ", 100000) + "last"} {
		expected := []rune(strings.Join(strings.Fields(raw), " "))
		if len(expected) > 110 {
			expected = expected[:110]
		}
		if actual := titleFor(raw); actual != string(expected) {
			t.Fatalf("Title differs: %q", actual)
		}
	}
	for _, raw := range []string{" # AGENTS.md instructions", "You\nare an assistant", "<xml>instructions</xml>"} {
		if titleFor(raw) != "Imported session" {
			t.Fatal(raw)
		}
	}
	if titleFor(" \t\n") != "Untitled session" {
		t.Fatal("Empty title")
	}
}

func TestAutomaticProfileDiscoveryNeedsNoSavedPaths(t *testing.T) {
	a := fixture(t)
	other := filepath.Join(a.Config.UsersDir, "teammate")
	for _, root := range []string{
		filepath.Join(a.Config.Home, ".codex"), filepath.Join(a.Config.Home, ".codex-work"),
		filepath.Join(other, ".codex"), filepath.Join(other, ".codex-client"),
	} {
		write(t, filepath.Join(root, "sessions", "2026", "large.jsonl"), "{unfinished")
	}
	for _, root := range []string{filepath.Join(a.Config.Home, ".claude"), filepath.Join(other, ".claude-team")} {
		write(t, filepath.Join(root, "projects", "demo", "large.jsonl"), "{unfinished")
	}
	alias := filepath.Join(a.Config.Home, ".codex-alias")
	if err := os.Symlink(filepath.Join(a.Config.Home, ".codex"), alias); err != nil {
		t.Fatal(err)
	}
	value, err := a.Run(context.Background(), "discover", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	sources := value.(map[string]any)["sources"].([]Source)
	counts := map[string]int{}
	for _, source := range sources {
		counts[source.Provider]++
		if source.Count != 0 {
			t.Fatal("Discovery read transcript counts")
		}
	}
	if counts["Codex"] != 4 || counts["Claude"] != 2 {
		t.Fatal(counts)
	}
	if _, err := os.Stat(filepath.Join(a.Config.Support, "sources.json")); !os.IsNotExist(err) {
		t.Fatal("Automatic discovery must not require or create a manual source list")
	}
	if _, err := os.Stat(filepath.Join(a.Config.Support, "import-cache.json")); !os.IsNotExist(err) {
		t.Fatal("Fast discovery must not import transcripts")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Run(ctx, "discover", strings.NewReader("")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCodexStoresAreReadOnlyAndProfilesStaySeparate(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, ".codex")
	db := makeDB(t, filepath.Join(root, "state_5.sqlite"), `CREATE TABLE threads(id,title,cwd,updated_at,rollout_path,source,creator_account_id,archived,model);
INSERT INTO threads VALUES('one','SQLite chat','/tmp',1,'missing.jsonl','cli','account',0,'model');
INSERT INTO threads VALUES('child','Child','/tmp',1,'missing.jsonl','{"subagent":{}}','account',0,'model');`)
	_ = db.Close()
	history := makeDB(t, filepath.Join(root, "thread_history_1.sqlite"), `CREATE TABLE thread_items(thread_id,item_id,item_json,item_type,rollout_ordinal)`)
	for i, item := range []map[string]any{{"type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "remember orange"}}}, {"type": "reasoning", "text": "private"}, {"type": "agentMessage", "text": "Orange"}} {
		data, _ := json.Marshal(item)
		if _, err := history.Exec("INSERT INTO thread_items VALUES(?,?,?,?,?)", "one", i, string(data), item["type"], i); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := codexSessions(context.Background(), sourceFor(root, "Codex"))
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	if entries[0].AccountID != "account" || entries[0].Model != "model" {
		t.Fatal(entries)
	}
	page, err := a.Transcript(context.Background(), entries[0], 1)
	if err != nil || page.Total != 2 || page.Messages[0].Text != "Orange" {
		t.Fatal(page, err)
	}
	readonly, err := openReadDB(filepath.Join(root, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if _, err = readonly.Exec("INSERT INTO threads(id) VALUES('forbidden')"); err == nil {
		t.Fatal("Provider database was writable")
	}
	other := makeSession(sourceFor(filepath.Join(a.Config.Home, ".codex2"), "Codex"), "one", "Title", "/tmp", "", 1)
	if other.ID == entries[0].ID {
		t.Fatal("Accounts merged")
	}
}

func TestLegacyCodexAndHistoryFallback(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, ".codex")
	path := filepath.Join(root, "archived_sessions", "rollout.jsonl")
	write(t, path, `{"type":"session_meta","payload":{"id":"old","cwd":"/tmp"}}`+"\n"+`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"legacy context"}]}}`+"\n{partial")
	if detectProvider(root) != "Codex" {
		t.Fatal("Archived profile not detected")
	}
	entries, err := codexSessions(context.Background(), sourceFor(root, "Codex"))
	if err != nil || len(entries) != 1 || !entries[0].Archived {
		t.Fatal(entries, err)
	}
	makeDB(t, filepath.Join(root, "thread_history_1.sqlite"), `CREATE TABLE thread_items(thread_id,item_id,item_json,item_type,rollout_ordinal)`)
	page, err := a.Transcript(context.Background(), entries[0], 120)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "legacy context" {
		t.Fatal(page, err)
	}
	if entries[0].Model != "" || entries[0].AccountID != "" {
		t.Fatal("Missing defaults")
	}
}

func TestClaudeTextOnlyHistoryAndDuplicateMessages(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, ".claude")
	path := filepath.Join(root, "projects", "p", "one.jsonl")
	text := `{"type":"user","sessionId":"one","cwd":"/tmp","uuid":"a","message":{"content":[{"type":"text","text":"Hello"}]}}` + "\n" +
		`{"type":"assistant","uuid":"b","message":{"content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"Hi"},{"type":"tool_use","name":"Bash"}]}}` + "\n" +
		`{"type":"user","uuid":"c","message":{"content":[{"type":"tool_result","content":"not a user message"}]}}` + "\n"
	write(t, path, text+record("b", "Replaced")+"{partial")
	write(t, filepath.Join(root, "projects", "p", "agent-child.jsonl"), "{}")
	write(t, filepath.Join(root, "projects", "p", "side.jsonl"), `{"type":"user","isSidechain":true,"cwd":"/tmp","message":{"content":"child"}}`)
	cache := map[string]cacheEntry{}
	entries, err := claudeSessions(context.Background(), sourceFor(root, "Claude"), cache)
	if err != nil || len(entries) != 1 || entries[0].Title != "Hello" {
		t.Fatal(entries, err)
	}
	page, err := a.Transcript(context.Background(), entries[0], 1)
	if err != nil || page.Total != 2 || page.Messages[0].Text != "Replaced" {
		t.Fatal(page, err)
	}
	before := cache[path].Signature
	write(t, path, text+"{}\n")
	_, err = claudeSessions(context.Background(), sourceFor(root, "Claude"), cache)
	if err != nil || reflect.DeepEqual(before, cache[path].Signature) {
		t.Fatal("Append did not invalidate cache", err)
	}
}

func TestSourcesConcurrentAddsAndFailureRetention(t *testing.T) {
	a := fixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			root := filepath.Join(a.Config.Home, ".codex")
			if err := os.MkdirAll(filepath.Join(root, "sessions"), 0700); err != nil {
				t.Error(err)
				return
			}
			if err := a.AddSource(context.Background(), root); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var paths []string
	if err := readJSON(filepath.Join(a.Config.Support, "sources.json"), &paths); err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	info, err := os.Stat(filepath.Join(a.Config.Support, "sources.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if err := a.AddSource(context.Background(), filepath.Join(a.Config.Home, "missing")); err == nil {
		t.Fatal("Invalid source accepted")
	}
	missing := filepath.Join(a.Config.Home, "disappeared")
	if err := saveJSON(filepath.Join(a.Config.Support, "sources.json"), []string{missing}); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(a.Config.Home, ".codexBroken")
	makeDB(t, filepath.Join(broken, "state_5.sqlite"), "CREATE TABLE threads(id)")
	result, err := a.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.FailedRoots) != 2 || len(result.Errors) != 2 {
		t.Fatal(result)
	}
}

func TestClaudeDesktopMetadataStaysWithinProfileHome(t *testing.T) {
	a := fixture(t)
	other := filepath.Join(a.Config.UsersDir, "teammate")
	for _, home := range []string{a.Config.Home, other} {
		write(t, filepath.Join(home, ".claude", "projects", "p", "same.jsonl"), `{"type":"user","sessionId":"same","cwd":"/tmp","message":{"content":"Base"}}`)
		meta, _ := json.Marshal(map[string]any{"cliSessionId": "same", "lastActivityAt": 1, "title": filepath.Base(home), "model": "model-" + filepath.Base(home)})
		write(t, filepath.Join(home, "Library/Application Support/Claude/claude-code-sessions/a/b/local_one.json"), string(meta))
	}
	result, err := a.Scan(context.Background())
	if err != nil || len(result.Sessions) != 2 {
		t.Fatal(result, err)
	}
	for _, s := range result.Sessions {
		home := filepath.Base(filepath.Dir(s.Root))
		if s.Title != home || s.Model != "model-"+home {
			t.Fatal("Metadata crossed account homes", s)
		}
	}
}

func TestSourceAddCancelsWhileWaitingForLock(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, ".codex")
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(a.Config.Support, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(a.Config.Support, "sources.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := a.AddSource(ctx, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.Config.Support, "sources.json")); !os.IsNotExist(err) {
		t.Fatal("Cancelled writer saved source", err)
	}
	if err := a.AddSource(ctx, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("An already-cancelled request was accepted", err)
	}
}
