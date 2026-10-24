package localcli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teslashibe/capybara-camp/server/internal/attachments"
)

func TestRestoredAttachmentContextAndRevocation(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			a, in, h, body := attachmentInstallFixture(t, provider)
			installed, err := installCall(t.Context(), a, in)
			if err != nil {
				t.Fatal(err)
			}
			session := Session{Provider: provider, Root: in.Target.Root, CWD: in.Destination, SourceID: in.Target.SourceID, SessionID: in.Target.SessionID}
			ref := RestoredHistoryReference{Scope: in.Scope, TransferID: in.TransferID, ReceiptSHA256: installed.ReceiptSHA256}
			sources, reader, err := a.restoredHistory(t.Context(), session, ref)
			if err != nil {
				t.Fatal(err)
			}
			proxy, server, err := startContextProxy(sources, reader)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			for i, source := range sources {
				page, err := proxy.reader(t.Context(), HistoryRead{SessionID: source.ID, Limit: 1})
				if err != nil || len(page.Messages) != 1 || len(page.Messages[0].Attachments) != 2 {
					t.Fatal("Missing attachment descriptors", err)
				}
				item := h.Attachments[i]
				read := HistoryRead{SessionID: source.ID, Limit: 1, Attachment: &HistoryAttachmentSelection{MessageID: item.MessageID, ID: item.Attachment.ID, SourceDigest: item.Attachment.SourceDigest}}
				page, err = proxy.reader(t.Context(), read)
				if err != nil || validateHistoryPage(source, read, page) != nil || !bytes.Equal(page.Attachment.Data, body) {
					t.Fatal("Restored body not available through scoped proxy", err)
				}
				tool := contextTool{history: sources, reader: proxy.reader, ctx: t.Context()}
				result := tool.handle(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "read_attachment", "arguments": map[string]any{"session_id": source.ID, "message_id": item.MessageID, "attachment_id": item.Attachment.ID, "source_digest": item.Attachment.SourceDigest}}})
				raw, _ := json.Marshal(result)
				if !bytes.Contains(raw, []byte(`"type":"resource"`)) || !bytes.Contains(raw, []byte("A preserved attachment")) || bytes.Contains(raw, []byte(`"isError":true`)) {
					t.Fatal("Document missing from tool response", string(raw))
				}
				for _, bad := range []HistoryAttachmentSelection{{MessageID: "other", ID: item.Attachment.ID, SourceDigest: item.Attachment.SourceDigest}, {MessageID: item.MessageID, ID: "a:1", SourceDigest: strings.Repeat("d", 64)}, {MessageID: item.MessageID, ID: item.Attachment.ID, SourceDigest: strings.Repeat("f", 64)}} {
					read.Attachment = &bad
					if _, err := proxy.reader(t.Context(), read); err == nil {
						t.Fatal("Unavailable or unselected body readable")
					}
				}
			}
			item := h.Attachments[0]
			read := HistoryRead{SessionID: sources[0].ID, Limit: 1, Attachment: &HistoryAttachmentSelection{MessageID: item.MessageID, ID: item.Attachment.ID, SourceDigest: item.Attachment.SourceDigest}}
			if err := os.WriteFile(filepath.Join(installDirectory(a, in), item.filename()), bytes.Repeat([]byte{'X'}, len(body)), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := proxy.reader(t.Context(), read); err == nil {
				t.Fatal("Tampered installed body readable")
			}
			if err := os.Remove(filepath.Join(installDirectory(a, in), "installation.json")); err != nil {
				t.Fatal(err)
			}
			if _, err := proxy.reader(t.Context(), HistoryRead{SessionID: sources[0].ID, Limit: 1}); err == nil {
				t.Fatal("Erased receipt retained history access")
			}
		})
	}
}

func TestHistoryAttachmentMaximumBodyAndWrongSelection(t *testing.T) {
	data := bytes.Repeat([]byte{255}, attachments.MaxBytes)
	ref := attachments.Reference{ID: "a:0", Kind: "image", Source: "inline", SourceDigest: strings.Repeat("a", 64), State: "embedded", MediaType: "image/png", SHA256: installHash(data), Size: len(data)}
	source := HistorySource{ID: "camp:fixture", Title: "Image", Revision: "1"}
	reader := func(ctx context.Context, in HistoryRead) (HistoryPage, error) {
		return HistoryPage{SessionID: source.ID, Revision: source.Revision, Attachment: &HistoryAttachmentBody{MessageID: "m", Reference: ref, Data: data}}, nil
	}
	proxy, server, err := startContextProxy([]HistorySource{source}, reader)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	read := HistoryRead{SessionID: source.ID, Limit: 1, Attachment: &HistoryAttachmentSelection{MessageID: "m", ID: ref.ID, SourceDigest: ref.SourceDigest}}
	page, err := proxy.reader(t.Context(), read)
	if err != nil || !bytes.Equal(page.Attachment.Data, data) {
		t.Fatal("Maximum body truncated", err)
	}
	read.Attachment.MessageID = "wrong"
	if _, err := proxy.reader(t.Context(), read); err == nil {
		t.Fatal("Response was not tied to selected message")
	}
}
