package localcli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func TestHistoryBindingRejectsChangedChildrenAndForeignParents(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			a, parent, childPath := childHistoryFixture(t, provider)
			binding, err := a.currentHistoryBinding(t.Context(), parent, 7)
			if err != nil || len(binding.Children) != 1 {
				t.Fatal(binding, err)
			}
			if err = a.verifyHistoryBinding(t.Context(), parent, &binding); err != nil {
				t.Fatal(err)
			}
			other := parent
			other.SessionID = "another"
			if err = a.verifyHistoryBinding(t.Context(), other, &binding); err == nil {
				t.Fatal("Foreign parent accepted the history binding")
			}
			if provider == "Codex" {
				db := makeDB(t, filepath.Join(parent.Root, "thread_history_1.sqlite"), "")
				if _, err = db.Exec(`INSERT INTO thread_items VALUES('unrelated','outside','{"type":"agentMessage","text":"Another active conversation"}','agentMessage',0)`); err != nil {
					t.Fatal(err)
				}
				if err = a.verifyHistoryBinding(t.Context(), parent, &binding); err != nil {
					t.Fatal("Unrelated profile write invalidated history", err)
				}
				if _, err = db.Exec(`UPDATE thread_items SET item_json='{"type":"agentMessage","text":"Changed child"}' WHERE thread_id='one' AND rollout_ordinal=0`); err != nil {
					t.Fatal(err)
				}
			} else {
				write(t, childPath, record("changed", "Changed child"))
			}
			if err = a.verifyHistoryBinding(t.Context(), parent, &binding); err == nil {
				t.Fatal("Changed child history was accepted")
			}
		})
	}
}

func TestHistoryBindingDetectsMembershipAndMainChanges(t *testing.T) {
	a, parent, childPath := childHistoryFixture(t, "Claude")
	binding, err := a.currentHistoryBinding(t.Context(), parent, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(childPath); err != nil {
		t.Fatal(err)
	}
	if err = a.verifyHistoryBinding(t.Context(), parent, &binding); err == nil {
		t.Fatal("Missing child accepted")
	}
	binding, err = a.currentHistoryBinding(t.Context(), parent, 2)
	if err != nil {
		t.Fatal(err)
	}
	write(t, parent.Path, record("parent-new", "Updated parent"))
	if err = a.verifyHistoryBinding(t.Context(), parent, &binding); err == nil {
		t.Fatal("Changed main history accepted")
	}
}

func TestCaptureAndFinalSourceCheckBindReviewedHistory(t *testing.T) {
	a, in := holdFixture(t)
	manifest, err := transfer.Decode(in.Payload)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"session": in.Session, "paths": []string{"file.txt"}, "history": manifest.History})
	value, err := a.Run(t.Context(), "transfer-capture", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	captured := value.(TransferCapture)
	decoded, err := transfer.Decode(captured.Payload)
	if err != nil || decoded.History == nil {
		t.Fatal(decoded, err)
	}
	in.Payload = captured.Payload
	in.HandoffID = uuid.NewString()
	input, events, done := startSourceHold(t, a, in)
	nextSourceEvent(t, events)
	write(t, in.Session.Path, `{"type":"response_item","payload":{"id":"new","type":"message","role":"assistant","content":[{"type":"output_text","text":"Changed after review"}]}}`+"\n")
	if err = json.NewEncoder(input).Encode(map[string]string{"type": "prepareCommit"}); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil {
		t.Fatal("Final commit accepted changed history")
	}
	if _, err = a.Run(t.Context(), "transfer-capture", bytes.NewReader(body)); err == nil {
		t.Fatal("Stale published history was captured")
	}
}

func TestTransferHandoffRejectsFileOnlyPayload(t *testing.T) {
	a, in := holdFixture(t)
	manifest, err := transfer.Decode(in.Payload)
	if err != nil {
		t.Fatal(err)
	}
	manifest.History = nil
	in.Payload, _ = json.Marshal(manifest)
	in.HandoffID = uuid.NewString()
	_, _, done := startSourceHold(t, a, in)
	if err = <-done; err == nil {
		t.Fatal("Local handoff accepted a payload without history")
	}
}
