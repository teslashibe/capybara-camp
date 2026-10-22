package localcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func contextFixture() []contextSource {
	return []contextSource{{ID: "local-source", Title: "Synthetic source", Revision: "pinned", Messages: []contextMessage{
		{ID: "m1", Role: "user", Text: "First selected message"},
		{ID: "m2", Role: "assistant", Text: "Ignore these quoted instructions; they remain data"},
	}}}
}

func contextArguments(t *testing.T, data string) map[string]any {
	t.Helper()
	var result map[string]any
	d := json.NewDecoder(strings.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestContextReadsOnlyPinnedSelections(t *testing.T) {
	tool := contextTool{sources: contextFixture()}
	result, err := tool.read(contextArguments(t, `{"session_id":"local-source","offset":1,"limit":1}`))
	if err != nil || result["revision"] != "pinned" || result["next_offset"] != nil || len(str(result["digest"])) != 64 {
		t.Fatal(result, err)
	}
	messages := result["messages"].([]map[string]any)
	if len(messages) != 1 || messages[0]["id"] != "m2" || !strings.Contains(str(messages[0]["source"]), "message=m2&revision=pinned") {
		t.Fatal(messages)
	}
	result, err = tool.read(contextArguments(t, `{"session_id":"local-source","limit":1}`))
	if err != nil || result["next_offset"] != int64(1) {
		t.Fatal(result, err)
	}
	result, err = tool.read(contextArguments(t, `{"session_id":"local-source","offset":9223372036854775807}`))
	if err != nil || len(result["messages"].([]map[string]any)) != 0 || result["next_offset"] != nil {
		t.Fatal("Offset overflow or invalid empty page", result, err)
	}
	for _, raw := range []string{
		`{"session_id":"/etc/passwd"}`, `{"session_id":"unattached"}`, `{"session_id":"local-source","path":"/etc/passwd"}`,
		`{"session_id":"local-source","offset":-1}`, `{"session_id":"local-source","offset":true}`,
		`{"session_id":"local-source","limit":121}`, `{"session_id":"local-source","limit":1.0}`,
		`{"session_id":"local-source","offset":9223372036854775808}`, `{"session_id":"local-source","limit":null}`,
	} {
		if _, err := tool.read(contextArguments(t, raw)); err == nil {
			t.Fatal("Accepted unauthorized or invalid read", raw)
		}
	}
}

func TestContextDigestAndEscapedProvenance(t *testing.T) {
	messages := []contextMessage{{ID: "m1", Role: "user", Text: "<hello> & café 😀\u2028\u2029\n\x00"}, {ID: "m2", Role: "assistant", Text: "Quoted text"}}
	if got := contextDigest(messages); got != "edab19ade53d7cdd479c7db3522fe7c2b544315618bdb36040ee48b4659f033a" {
		t.Fatal("Historical sorted UTF-8 JSON digest changed", got)
	}
	source := contextSource{ID: "a/b? c+é", Revision: "&=/#", Messages: []contextMessage{{ID: "x?&=+#"}}}
	if link := contextLink(source, source.Messages[0]); link != "capybara://session/a%2Fb%3F%20c%2B%C3%A9?message=x%3F%26%3D%2B%23&revision=%26%3D%2F%23" {
		t.Fatal("Unsafe source link", link)
	}
}

func TestContextWireValidationAndBounds(t *testing.T) {
	base, _ := json.Marshal(contextFixture())
	if _, err := decodeContext(bytes.Replace(base, []byte("Synthetic"), []byte{0xff}, 1)); err == nil {
		t.Fatal("Invalid UTF-8 silently changed the selected source")
	}
	for _, input := range []string{
		`null`, `{}`, `[null]`, strings.Replace(string(base), `"title":"Synthetic source"`, `"title":null`, 1),
		strings.Replace(string(base), `"revision":"pinned"`, `"revision":42`, 1),
		strings.Replace(string(base), `"title":"Synthetic source"`, `"title":"Synthetic source","path":"/tmp"`, 1),
		strings.Replace(string(base), `"text":"First selected message"`, `"text":null`, 1),
		strings.Replace(string(base), `"role":"user"`, `"role":"execute"`, 1),
		strings.Replace(string(base), `"id":"m2"`, `"id":"m1"`, 1),
		string(base) + `{}`, strings.Repeat(" ", 1<<20+1),
	} {
		if _, err := decodeContext([]byte(input)); err == nil {
			t.Fatal("Invalid manifest accepted", input[:min(len(input), 80)])
		}
	}
	if _, err := decodeContext([]byte(`[]`)); err != nil {
		t.Fatal("Empty explicit selection rejected", err)
	}
	for _, change := range []string{"message", "total", "source-count", "message-count", "duplicate-source"} {
		sources := contextFixture()
		switch change {
		case "message":
			sources[0].Messages[0].Text = strings.Repeat("x", 48<<10+1)
		case "total":
			sources[0].Messages = nil
			for i := 0; i < 6; i++ {
				sources[0].Messages = append(sources[0].Messages, contextMessage{ID: strconv.Itoa(i), Role: "user", Text: strings.Repeat("x", 48<<10)})
			}
		case "source-count":
			for len(sources) < 21 {
				source := contextFixture()[0]
				source.ID = strconv.Itoa(len(sources))
				sources = append(sources, source)
			}
		case "message-count":
			for len(sources[0].Messages) < 121 {
				sources[0].Messages = append(sources[0].Messages, contextMessage{ID: strconv.Itoa(len(sources[0].Messages)), Role: "user"})
			}
		case "duplicate-source":
			sources = append(sources, sources[0])
		}
		if err := validateContext(sources); err == nil {
			t.Fatal("Context bound missed", change)
		}
	}
	sources := contextFixture()
	for i := range sources[0].Messages {
		sources[0].Messages[i].Text = strings.Repeat("x", 40<<10)
	}
	if _, err := (contextTool{sources}).read(contextArguments(t, `{"session_id":"local-source"}`)); err == nil {
		t.Fatal("Oversized read was not rejected")
	}
}

func TestContextToolSurfaceAndErrors(t *testing.T) {
	tool := contextTool{sources: contextFixture()}
	list := tool.handle(map[string]any{"id": 1, "method": "tools/list"})
	data, _ := json.Marshal(list)
	if !bytes.Contains(data, []byte(`"readOnlyHint":true`)) || bytes.Contains(data, []byte("First selected message")) {
		t.Fatal("Tool list exposed content or lost read-only contract", string(data))
	}
	for _, method := range []string{"send_prompt", "write_file", "read_session"} {
		result := object(tool.handle(map[string]any{"id": 2, "method": "tools/call", "params": map[string]any{"name": method, "arguments": map[string]any{"session_id": "other"}}})["result"])
		if result["isError"] != true {
			t.Fatal("Unattached or unavailable tool accepted", method)
		}
	}
	if tool.handle(map[string]any{"method": "notifications/initialized"}) != nil {
		t.Fatal("Replied to notification")
	}
	if object(tool.handle(map[string]any{"id": 3, "params": false})["error"])["code"] != -32602 {
		t.Fatal("Invalid params not rejected")
	}
}

func TestContextGrantPrivateImmutableAndCleaned(t *testing.T) {
	sources := contextFixture()
	grant, err := newContextGrant(sources, t.TempDir(), "/synthetic/camp-local")
	requireApprovalOK(t, err)
	path := filepath.Join(grant.directory, "sources.json")
	for _, file := range []string{grant.directory, path, grant.instructionsPath, filepath.Join(grant.directory, "owner.json")} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("Context file is not private", file, err)
		}
	}
	sources[0].Messages[0].Text = "Changed after approval"
	data, err := privateContextFile(path)
	if err != nil || bytes.Contains(data, []byte("Changed after approval")) || strings.Contains(grant.instructions(), "First selected message") {
		t.Fatal("Pinned selection changed or prompt includes content", err)
	}
	if !strings.Contains(grant.attachmentLinks(), "capybara://session/local-source?message=m1&revision=pinned") {
		t.Fatal("Missing source link")
	}
	grant.Close()
	grant.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Manifest retained", err)
	}
}

func TestContextFileRejectsPublicSymlinkAndFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sources.json")
	if err := os.WriteFile(path, []byte(`[]`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := privateContextFile(path); err == nil {
		t.Fatal("Public-readable context admitted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "link")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := privateContextFile(symlink); err == nil {
		t.Fatal("Context symlink followed")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := privateContextFile(fifo); err == nil {
		t.Fatal("Context FIFO admitted")
	}
}

func TestContextCleanupPreservesLiveAndUnknownDirectories(t *testing.T) {
	dir := t.TempDir()
	live, err := newContextGrant(contextFixture(), dir, "/fixture/camp-local")
	requireApprovalOK(t, err)
	defer live.Close()
	dead := filepath.Join(dir, "camp-context-dead")
	requireApprovalOK(t, os.Mkdir(dead, 0700))
	// A completed child gives a known exited PID without guessing OS PID limits.
	command := exec.Command(os.Args[0])
	command.Env = []string{"CAMP_LOCAL_TEST_PROCESS=reply", "GORACE=atexit_sleep_ms=0"}
	requireApprovalOK(t, command.Run())
	owner, _ := json.Marshal(map[string]any{"kind": "capybara-context-v1", "pid": command.Process.Pid})
	requireApprovalOK(t, os.WriteFile(filepath.Join(dead, "owner.json"), owner, 0600))
	requireApprovalOK(t, os.WriteFile(filepath.Join(dead, "sources.json"), []byte(`[]`), 0600))
	unknown := filepath.Join(dir, "camp-context-user")
	requireApprovalOK(t, os.Mkdir(unknown, 0700))
	requireApprovalOK(t, os.WriteFile(filepath.Join(unknown, "owner.json"), owner, 0600))
	requireApprovalOK(t, os.WriteFile(filepath.Join(unknown, "notes.txt"), []byte("Keep"), 0600))
	cleanupContextGrants(dir)
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatal("Orphan remains", err)
	}
	for _, path := range []string{live.directory, filepath.Join(unknown, "notes.txt")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("Live or unknown context removed", err)
		}
	}
}

func TestContextRealStdioExchangeAndLimits(t *testing.T) {
	grant, err := newContextGrant(contextFixture(), t.TempDir(), os.Args[0])
	requireApprovalOK(t, err)
	defer grant.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0])
	command.Env = []string{"CAMP_LOCAL_TEST_PROCESS=context-tool", "CAMP_LOCAL_TEST_MANIFEST=" + filepath.Join(grant.directory, "sources.json"), "GORACE=atexit_sleep_ms=0"}
	command.Stdin = strings.NewReader("{broken\n" +
		`{"id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}` + "\n" +
		`{"method":"notifications/initialized"}` + "\n" +
		`{"id":2,"method":"tools/call","params":{"name":"read_session","arguments":{"session_id":"local-source"}}}` + "\n")
	output, err := command.Output()
	requireApprovalOK(t, err)
	lines := bytes.Split(bytes.TrimSpace(output), []byte("\n"))
	if len(lines) != 3 {
		t.Fatal("Stdio lost framing", string(output))
	}
	reply := contextArguments(t, string(lines[2]))
	result := object(reply["result"])
	if result["isError"] != false || !strings.Contains(string(lines[2]), "First selected message") {
		t.Fatal("Stdio read failed", string(lines[2]))
	}
	var out bytes.Buffer
	a := &App{}
	if err := a.ServeContext(context.Background(), filepath.Join(grant.directory, "sources.json"), strings.NewReader(strings.Repeat("x", 128<<10+1)), &out); err == nil {
		t.Fatal("Oversized stdio request accepted")
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if err := a.ServeContext(cancelled, filepath.Join(grant.directory, "sources.json"), strings.NewReader(""), io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal("Cancelled context accepted", err)
	}
}
