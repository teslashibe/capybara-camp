package localcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportAmendmentsTombstonesAndReappearance(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(a.Config.Home, "session.jsonl")
	s := Session{Provider: "Claude", Root: a.Config.Home, SessionID: "fixture", Path: path}
	write(t, path, record("a", "First")+record("b", "Second")+record("a", "First amended"))
	first, err := a.ExportPage(context.Background(), s, -1, 1)
	if err != nil || len(first.Messages) != 1 || first.Messages[0].Text != "First amended" || first.Messages[0].Model != "fixture-model" || !first.HasMore {
		t.Fatal(first, err)
	}
	second, err := a.ExportPage(context.Background(), s, first.After, 1)
	if err != nil || len(second.Messages) != 1 || second.Messages[0].ID != "b" || second.HasMore {
		t.Fatal(second, err)
	}
	write(t, path, record("b", "Second amended"))
	changed, err := a.ExportPage(context.Background(), s, -1, 200)
	if err != nil || len(changed.Messages) != 2 || changed.Messages[0].Ordinal != 0 || !changed.Messages[0].Removed || changed.Messages[0].Text != "" || changed.Messages[1].Removed {
		t.Fatal(changed, err)
	}
	write(t, path, record("b", "Second amended")+record("a", "First returns")+record("c", "Third"))
	changed, err = a.ExportPage(context.Background(), s, -1, 200)
	if err != nil || len(changed.Messages) != 3 {
		t.Fatal(changed, err)
	}
	for i, m := range changed.Messages {
		if m.Ordinal != int64(i) || m.Removed {
			t.Fatal(changed)
		}
	}
	files, _ := filepath.Glob(filepath.Join(a.Config.Support, "transcript-index", "*.sqlite"))
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}

func TestExportPartialWriteRollsBackAndMatchesExistingIndex(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(a.Config.Home, "session.jsonl")
	s := Session{Provider: "Claude", Root: a.Config.Home, SessionID: "fixture", Path: path}
	write(t, path, record("a", "Complete answer"))
	key := hash([]byte(s.Provider + "\n" + s.Root + "\n" + s.SessionID + "\n" + s.Path))
	db := makeDB(t, filepath.Join(a.Config.Support, "transcript-index", key+".sqlite"), `CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE messages(id TEXT PRIMARY KEY,ordinal INTEGER NOT NULL UNIQUE,body TEXT NOT NULL,seen INTEGER NOT NULL,removed INTEGER NOT NULL);`)
	body, _ := json.Marshal(ExportMessage{ID: "a", Ordinal: 9, Role: "assistant", Text: "Previously indexed"})
	if _, err := db.Exec("INSERT INTO messages VALUES('a',9,?,1,0)", string(body)); err != nil {
		t.Fatal(err)
	}
	page, err := a.ExportPage(context.Background(), s, -1, 200)
	if err != nil || page.Messages[0].Ordinal != 9 || page.Messages[0].Text != "Complete answer" {
		t.Fatal(page, err)
	}
	write(t, path, `{"type":"assistant"`)
	if _, err = a.ExportPage(context.Background(), s, -1, 200); err == nil {
		t.Fatal("Partial write was committed")
	}
	var removed int
	if err = db.QueryRow("SELECT removed FROM messages WHERE id='a'").Scan(&removed); err != nil || removed != 0 {
		t.Fatal("Rollback lost prior message", err)
	}
	write(t, path, record("a", "Complete answer"))
	page, err = a.ExportPage(context.Background(), s, -1, 200)
	if err != nil || page.Messages[0].Removed || page.Messages[0].Ordinal != 9 {
		t.Fatal(page, err)
	}
}

func TestExportBoundsIsolationAndProtocolValidation(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(a.Config.Home, "session.jsonl")
	s := Session{Provider: "Claude", Root: a.Config.Home, SessionID: "fixture", Path: path}
	write(t, path, record("a", strings.Repeat("x", 600000))+record("b", strings.Repeat("y", 600000)))
	page, err := a.ExportPage(context.Background(), s, -1, 200)
	if err != nil || len(page.Messages) != 1 || !page.HasMore {
		t.Fatal(len(page.Messages), err)
	}
	other := s
	other.Root += "/another"
	if _, err = a.ExportPage(context.Background(), other, -1, 200); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(a.Config.Support, "transcript-index", "*.sqlite"))
	if len(files) != 2 {
		t.Fatal("Profile indexes merged")
	}
	write(t, path, record("large", strings.Repeat("x", 1100000)))
	if _, err = a.ExportPage(context.Background(), s, -1, 200); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatal(err)
	}
	for _, input := range []string{`{}`, `{} {}`, `{"session":{"provider":"Claude"},"limit":0}`, `{"session":{"provider":"Codex"},"limit":true}`} {
		if _, err := a.Run(context.Background(), "transcript", strings.NewReader(input)); err == nil {
			t.Fatal("Invalid request accepted", input)
		}
	}
}

func TestCodexFallbackExportDoesNotCommitPartialRollout(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(a.Config.Home, "rollout.jsonl")
	s := Session{Provider: "Codex", Root: a.Config.Home, SessionID: "fixture", Path: path}
	text := `{"type":"response_item","payload":{"id":"answer","type":"message","role":"assistant","content":[{"type":"output_text","text":"Complete"}]}}` + "\n"
	write(t, path, text)
	if page, err := a.ExportPage(context.Background(), s, -1, 200); err != nil || len(page.Messages) != 1 {
		t.Fatal(page, err)
	}
	write(t, path, text+`{"type":"response_item"`)
	if _, err := a.ExportPage(context.Background(), s, -1, 200); err == nil {
		t.Fatal("Partial Codex rollout was exported")
	}
	if page, err := a.Transcript(context.Background(), s, 120); err != nil || len(page.Messages) != 1 {
		t.Fatal("Live transcript did not retain completed text", page, err)
	}
}
