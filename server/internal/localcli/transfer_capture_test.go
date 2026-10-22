package localcli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func TestTransferReviewUsesRepositoryRelativeSelectedBytes(t *testing.T) {
	a, in := holdFixture(t)
	body, _ := json.Marshal(map[string]any{"session": in.Session})
	listed, err := a.Run(t.Context(), "transfer-changes", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	changes := listed.(transfer.ChangeSet)
	if len(changes.Files) != 1 || changes.Files[0].Path != "file.txt" {
		t.Fatal(changes)
	}
	body, _ = json.Marshal(map[string]any{"session": in.Session, "paths": []string{"file.txt"}})
	value, err := a.Run(t.Context(), "transfer-capture", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	capture := value.(TransferCapture)
	manifest, err := transfer.Decode(capture.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || string(manifest.Files[0].Data) != "after\n" || capture.PayloadSHA256 == "" || capture.ReviewSHA256 == "" {
		t.Fatal("Wrong capture")
	}
	if bytes.Contains(capture.Payload, []byte(in.Session.CWD)) {
		t.Fatal("Local path entered transfer payload")
	}
	body, _ = json.Marshal(map[string]any{"session": in.Session, "paths": []string{}})
	if _, err = a.Run(t.Context(), "transfer-capture", bytes.NewReader(body)); err != nil {
		t.Fatal("History-only transfer failed", err)
	}
}
