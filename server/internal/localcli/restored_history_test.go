package localcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func restoredFixture(t *testing.T, provider string) (*App, TransferInstallInput, RestoredHistoryReference, Session) {
	t.Helper()
	a, in := installFixture(t, provider)
	result, err := installCall(t.Context(), a, in)
	if err != nil {
		t.Fatal(err)
	}
	return a, in, RestoredHistoryReference{Scope: in.Scope, TransferID: in.TransferID, ReceiptSHA256: result.ReceiptSHA256},
		Session{Provider: provider, Root: in.Target.Root, CWD: in.Destination, SourceID: in.Target.SourceID, SessionID: in.Target.SessionID}
}

func TestRestoredHistoryCompletePagesAndContextProvenance(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			a, in := installFixture(t, provider)
			var raw map[string]any
			if err := json.Unmarshal(in.History, &raw); err != nil {
				t.Fatal(err)
			}
			raw["session"].(map[string]any)["title"] = "Original planning session"
			conversations := raw["conversations"].([]any)
			want := []map[string]string{{}, {}}
			for index, count := range []int{260, 250} {
				messages := []any{}
				for i := range count {
					id, text := fmt.Sprintf("original-%d-%d", index, i), fmt.Sprintf("Message %d 🦫", i)
					if i == 0 {
						text = strings.Repeat("a🦫é", 24000) // Split inside multibyte boundaries.
					}
					want[index][id] = text
					messages = append(messages, map[string]any{"id": id, "ordinal": i * 3, "role": "assistant", "text": text, "revision": 1})
				}
				conversations[index].(map[string]any)["messages"] = messages
			}
			in.History, _ = json.Marshal(raw)
			in.HistorySHA256 = installHash(in.History)
			installed, err := installCall(t.Context(), a, in)
			if err != nil {
				t.Fatal(err)
			}
			session := Session{Provider: provider, Root: in.Target.Root, CWD: in.Destination, SourceID: in.Target.SourceID, SessionID: in.Target.SessionID}
			ref := RestoredHistoryReference{Scope: in.Scope, TransferID: in.TransferID, ReceiptSHA256: installed.ReceiptSHA256}
			sources, reader, err := a.restoredHistory(t.Context(), session, ref)
			if err != nil || len(sources) != 2 || sources[0].Title != "Original planning session" || sources[1].Title != "Research" {
				t.Fatal("catalog", err)
			}
			for index, source := range sources {
				got := map[string]string{}
				read := HistoryRead{SessionID: source.ID, Limit: 120}
				complete := false
				for range 100 {
					page, err := reader(t.Context(), read)
					if err != nil || validateHistoryPage(source, read, page) != nil {
						t.Fatal("page", err)
					}
					for _, message := range page.Messages {
						if message.Ordinal%3 != 0 || len(got[message.ID]) != message.TextOffset {
							t.Fatal("Changed original ordinal or text offset")
						}
						got[message.ID] += message.Text
					}
					if page.NextOffset == nil {
						complete = true
						break
					}
					read.Offset, read.TextOffset = *page.NextOffset, page.NextTextOffset
				}
				if !complete || !reflect.DeepEqual(got, want[index]) {
					t.Fatal("Pinned history was truncated or rewritten")
				}
			}
			grant, err := newContextGrantWithHistory([]contextSource{}, sources, reader, t.TempDir(), "/fixture/camp-local")
			if err != nil {
				t.Fatal(err)
			}
			defer grant.Close()
			input := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_session","arguments":{"session_id":%q,"offset":3,"limit":1}}}`+"\n", sources[1].ID)
			var out strings.Builder
			if err = a.ServeContext(t.Context(), filepath.Join(grant.directory, "sources.json"), strings.NewReader(input), &out); err != nil || !strings.Contains(out.String(), "message=original-1-1") || !strings.Contains(out.String(), "Message 1") {
				t.Fatal("Lost child-message provenance", err, out.String())
			}
			for _, invalid := range []HistoryRead{
				{SessionID: "camp:foreign", Limit: 1}, {SessionID: sources[0].ID, Offset: -1, Limit: 1},
				{SessionID: sources[0].ID, Limit: 121}, {SessionID: sources[0].ID, TextOffset: 2, Limit: 1},
				{SessionID: sources[0].ID, Offset: 1, TextOffset: 1, Limit: 1},
				{SessionID: sources[0].ID, Offset: 9999, TextOffset: 1, Limit: 1},
			} {
				if _, err := reader(t.Context(), invalid); err == nil {
					t.Fatal("Invalid or foreign cursor accepted", invalid)
				}
			}
			// Mutating the returned catalog must not change the pinned reader.
			original := sources[0]
			sources[0].Revision = "forged"
			page, err := reader(t.Context(), HistoryRead{SessionID: original.ID, Limit: 1})
			if err != nil || page.Revision != original.Revision {
				t.Fatal("Caller replaced the pinned revision", err)
			}
		})
	}
}

func TestRestoredHistoryRefusesRebindingCorruptionAndErasure(t *testing.T) {
	for _, change := range []string{"scope", "transfer", "receipt", "profile", "provider", "source", "checkout", "intent", "history", "removed", "symlink", "hardlink", "replacement", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			a, in, ref, session := restoredFixture(t, "Codex")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sources, reader, err := a.restoredHistory(ctx, session, ref)
			if err != nil {
				t.Fatal(err)
			}
			historyPath := filepath.Join(installDirectory(a, in), "history.json")
			live := false
			switch change {
			case "scope":
				ref.Scope = strings.Repeat("e", 64)
			case "transfer":
				ref.TransferID = uuid.NewString()
			case "receipt":
				ref.ReceiptSHA256 = strings.Repeat("f", 64)
			case "profile":
				session.Root = a.Config.Home
			case "provider":
				session.Provider = "Claude"
			case "source":
				session.SourceID = "other"
			case "checkout":
				session.CWD = a.Config.Home
			case "intent":
				err = os.WriteFile(filepath.Join(installDirectory(a, in), "intent.json"), []byte("{}"), 0600)
				live = true
			case "history":
				err = os.WriteFile(historyPath, []byte("{}"), 0600)
				live = true
			case "removed":
				err = os.Remove(historyPath)
				live = true
			case "replacement", "symlink":
				moved := historyPath + ".original"
				err = os.Rename(historyPath, moved)
				if err == nil && change == "symlink" {
					err = os.Symlink(moved, historyPath)
				} else if err == nil {
					err = os.WriteFile(historyPath, in.History, 0600)
				}
				live = true
			case "hardlink":
				err = os.Link(historyPath, historyPath+".link")
				live = true
			case "cancelled":
				cancel()
				live = true
			}
			if err != nil {
				t.Fatal(err)
			}
			if live {
				if _, err = reader(t.Context(), HistoryRead{SessionID: sources[0].ID, Limit: 1}); err == nil {
					t.Fatal("Invalidated running archive remained readable")
				}
			} else if _, _, err = a.restoredHistory(ctx, session, ref); err == nil {
				t.Fatal("Changed destination binding accepted")
			}
		})
	}
}

func TestRestoredHistoryAttachmentDoesNotGrantExecutionOrCreateConversation(t *testing.T) {
	a, _, ref, session := restoredFixture(t, "Claude")
	request := conversationRequest{Session: session, Mode: "chat", RestoredHistory: &ref}
	if err := a.attachRestoredHistory(t.Context(), &request); err != nil {
		t.Fatal(err)
	}
	if request.NewSession || request.ExecutionApproved || len(request.Permissions) != 0 || request.Session != session || len(request.History) != 2 || request.ReadHistory == nil {
		t.Fatal("History attachment changed execution or session identity")
	}
	if err := a.attachRestoredHistory(t.Context(), &request); err == nil {
		t.Fatal("Overwrote an existing reader")
	}
	request = conversationRequest{Session: session, Mode: "inference", RestoredHistory: &ref}
	if err := a.attachRestoredHistory(t.Context(), &request); err == nil {
		t.Fatal("Attached an archive to dedicated inference")
	}
	a.isolated = true
	request.Mode = "chat"
	if err := a.attachRestoredHistory(t.Context(), &request); err == nil {
		t.Fatal("Cloud runner accepted a local archive")
	}
}
