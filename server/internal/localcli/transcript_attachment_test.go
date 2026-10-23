package localcli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teslashibe/capybara-camp/server/internal/attachments"
)

func attachmentRecord(provider, id, body string) string {
	content := []any{map[string]any{"type": "image", "url": "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(body))}}
	var record any = map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "id": id, "role": "user", "content": content}}
	if provider == "Claude" {
		content[0] = map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString([]byte(body))}}
		record = map[string]any{"uuid": id, "type": "user", "message": map[string]any{"content": content}}
	}
	data, _ := json.Marshal(record)
	return string(data) + "\n"
}

func TestAttachmentOnlyTranscriptAndVerifiedBytes(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			a := fixture(t)
			path := filepath.Join(a.Config.Home, "session.jsonl")
			s := Session{Provider: provider, Root: a.Config.Home, SessionID: "fixture", Path: path}
			write(t, path, attachmentRecord(provider, "image", "first"))
			transcript, err := a.Transcript(t.Context(), s, 120)
			if err != nil || transcript.Total != 1 || len(transcript.Messages) != 1 || transcript.Messages[0].Text != "" || len(transcript.Messages[0].Attachments) != 1 {
				t.Fatal(transcript, err)
			}
			ref := transcript.Messages[0].Attachments[0]
			request := AttachmentRead{Session: s, MessageID: "image", AttachmentID: ref.ID, SourceDigest: ref.SourceDigest}
			input, _ := json.Marshal(request)
			result, err := a.Run(t.Context(), "transcript-attachment", bytes.NewReader(input))
			if err != nil || string(result.(AttachmentBody).Data) != "first" {
				t.Fatal(result, err)
			}
			if _, err = a.ExportPage(t.Context(), s, -1, 200); !errors.Is(err, ErrAttachmentPublication) {
				t.Fatal("Text-only export should be refused", err)
			}
			write(t, path, attachmentRecord(provider, "image", "changed"))
			if _, err = a.TranscriptAttachment(t.Context(), request); !errors.Is(err, attachments.ErrChanged) {
				t.Fatal(err)
			}
			write(t, path, attachmentRecord(provider, "different", "first"))
			if _, err = a.TranscriptAttachment(t.Context(), request); !errors.Is(err, attachments.ErrChanged) {
				t.Fatal(err)
			}
			if _, err = a.Run(t.Context(), "transcript-attachment", strings.NewReader(string(input)+"{}")); err == nil {
				t.Fatal("Accepted trailing request data")
			}
		})
	}
}

func TestClaudeAttachmentAmendmentCannotReturnOldBytes(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(a.Config.Home, "session.jsonl")
	s := Session{Provider: "Claude", Root: a.Config.Home, SessionID: "fixture", Path: path}
	first := attachmentRecord("Claude", "one", "first")
	write(t, path, first)
	transcript, _ := a.Transcript(t.Context(), s, 120)
	ref := transcript.Messages[0].Attachments[0]
	request := AttachmentRead{Session: s, MessageID: "one", AttachmentID: ref.ID, SourceDigest: ref.SourceDigest}
	write(t, path, first+attachmentRecord("Claude", "one", "last"))
	if _, err := a.TranscriptAttachment(t.Context(), request); !errors.Is(err, attachments.ErrChanged) {
		t.Fatal(err)
	}
	transcript, err := a.Transcript(t.Context(), s, 120)
	if err != nil || transcript.Total != 1 {
		t.Fatal(transcript, err)
	}
	request.SourceDigest = transcript.Messages[0].Attachments[0].SourceDigest
	data, err := a.TranscriptAttachment(t.Context(), request)
	if err != nil || string(data.Data) != "last" {
		t.Fatal(data, err)
	}
	write(t, path, first+record("one", "Now text only"))
	if _, err = a.TranscriptAttachment(t.Context(), request); !errors.Is(err, attachments.ErrChanged) {
		t.Fatal(err)
	}
	write(t, path, first+`{"incomplete":`)
	request.SourceDigest = ref.SourceDigest
	if data, err = a.TranscriptAttachment(t.Context(), request); err == nil || len(data.Data) != 0 {
		t.Fatal("Returned bytes across an incomplete record", err)
	}
}

func TestCodexDatabaseAttachmentAndExportRollback(t *testing.T) {
	a := fixture(t)
	db := makeDB(t, filepath.Join(a.Config.Home, "thread_history_1.sqlite"), `CREATE TABLE thread_items(thread_id,item_id,item_json,item_type,rollout_ordinal)`)
	_, err := db.Exec(`INSERT INTO thread_items VALUES('fixture','one','{"type":"userMessage","content":[{"type":"text","text":"Before"}]}','userMessage',0)`)
	if err != nil {
		t.Fatal(err)
	}
	s := Session{Provider: "Codex", Root: a.Config.Home, SessionID: "fixture"}
	before, err := a.ExportPage(t.Context(), s, -1, 200)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE thread_items SET item_json='{"type":"userMessage","content":[{"type":"image","url":"data:image/png;base64,Ynl0ZXM="}]}'`)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := a.Transcript(t.Context(), s, 120)
	if err != nil || len(transcript.Messages) != 1 || len(transcript.Messages[0].Attachments) != 1 {
		t.Fatal(transcript, err)
	}
	ref := transcript.Messages[0].Attachments[0]
	data, err := a.TranscriptAttachment(t.Context(), AttachmentRead{Session: s, MessageID: "one", AttachmentID: ref.ID, SourceDigest: ref.SourceDigest})
	if err != nil || string(data.Data) != "bytes" {
		t.Fatal(data, err)
	}
	if _, err = a.ExportPage(t.Context(), s, -1, 200); !errors.Is(err, ErrAttachmentPublication) {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE thread_items SET item_json='{"type":"userMessage","content":[{"type":"text","text":"Before"}]}'`)
	if err != nil {
		t.Fatal(err)
	}
	after, err := a.ExportPage(t.Context(), s, -1, 200)
	if err != nil || after.ContentRevision != before.ContentRevision {
		t.Fatal("Attachment refusal changed the committed index", err)
	}
	// A newer empty SQLite item must not resurrect an attachment from rollout.
	s.Path = filepath.Join(a.Config.Home, "old.jsonl")
	write(t, s.Path, attachmentRecord("Codex", "one", "bytes"))
	_, err = db.Exec(`UPDATE thread_items SET item_json='{"type":"userMessage","content":[]}'`)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err = a.Transcript(t.Context(), s, 120)
	if err != nil || transcript.Total != 0 {
		t.Fatal("Resurrected a removed database attachment", transcript, err)
	}
	if _, err = a.TranscriptAttachment(t.Context(), AttachmentRead{Session: s, MessageID: "one", AttachmentID: ref.ID, SourceDigest: ref.SourceDigest}); !errors.Is(err, attachments.ErrChanged) {
		t.Fatal(err)
	}
}

func TestSubagentAttachmentRequiresChildTranscriptSelection(t *testing.T) {
	a := fixture(t)
	path := filepath.Join(a.Config.Home, "child.jsonl")
	s := Session{Provider: "Claude", Root: a.Config.Home, SessionID: "child", Path: path}
	raw := strings.Replace(attachmentRecord("Claude", "one", "child bytes"), `"type":"user"`, `"isSidechain":true,"type":"user"`, 1)
	write(t, path, raw)
	parent, err := a.Transcript(t.Context(), s, 120)
	if err != nil || parent.Total != 0 {
		t.Fatal(parent, err)
	}
	s.IsSubagent = true
	child, err := a.Transcript(t.Context(), s, 120)
	if err != nil || len(child.Messages) != 1 {
		t.Fatal(child, err)
	}
	ref := child.Messages[0].Attachments[0]
	request := AttachmentRead{Session: s, MessageID: "one", AttachmentID: ref.ID, SourceDigest: ref.SourceDigest}
	read, err := a.TranscriptAttachment(t.Context(), request)
	if err != nil || string(read.Data) != "child bytes" {
		t.Fatal(read, err)
	}
	request.Session.IsSubagent = false
	if _, err = a.TranscriptAttachment(t.Context(), request); !errors.Is(err, attachments.ErrChanged) {
		t.Fatal(err)
	}
}
