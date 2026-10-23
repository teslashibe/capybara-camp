package localcli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/teslashibe/capybara-camp/server/internal/testgit"
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

func TestTransferCaptureReviewsUnpublishedCommits(t *testing.T) {
	a, in := holdFixture(t)
	root := in.Session.CWD
	base := testgit.Command(t, root, "rev-parse", "HEAD")
	testgit.Command(t, root, "add", "file.txt")
	testgit.Command(t, root, "commit", "-qm", "Unpublished fixture")
	head := testgit.Command(t, root, "rev-parse", "HEAD")
	write(t, filepath.Join(root, "file.txt"), "Selected uncommitted edit\n")
	body, _ := json.Marshal(map[string]any{"session": in.Session, "paths": []string{"file.txt"}, "remoteBaseCommit": base})
	value, err := a.Run(t.Context(), "transfer-capture", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	capture := value.(TransferCapture)
	if capture.Commits == nil || capture.Commits.BaseCommit != base || capture.Commits.HeadCommit != head || capture.Commits.Commits != 1 || capture.Commits.Objects < 3 || capture.Commits.Bytes == 0 || capture.Commits.ReviewSHA256 == "" {
		t.Fatal("Missing commit review summary")
	}
	value, err = a.Run(t.Context(), "transfer-review", bytes.NewReader(capture.Payload))
	if err != nil {
		t.Fatal(err)
	}
	review := value.(TransferCapture)
	if review.Commits == nil || *review.Commits != *capture.Commits || review.ReviewSHA256 != capture.ReviewSHA256 || review.Payload != nil {
		t.Fatal("Downloaded review lost commit approval")
	}
	manifest, err := transfer.Decode(capture.Payload)
	if err != nil || len(manifest.Files) != 1 || string(manifest.Files[0].Data) != "Selected uncommitted edit\n" {
		t.Fatal("Working change missing from combined capture", err)
	}
}

func TestDownloadedTransferReviewUsesCaptureValidation(t *testing.T) {
	a, in := holdFixture(t)
	body, _ := json.Marshal(map[string]any{"session": in.Session, "paths": []string{"file.txt"}})
	value, err := a.Run(t.Context(), "transfer-capture", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	capture := value.(TransferCapture)
	value, err = a.Run(t.Context(), "transfer-review", bytes.NewReader(capture.Payload))
	if err != nil {
		t.Fatal(err)
	}
	review := value.(TransferCapture)
	if review.Payload != nil || review.PayloadSHA256 != capture.PayloadSHA256 || review.ReviewSHA256 != capture.ReviewSHA256 || review.BaseCommit != capture.BaseCommit || len(review.Files) != 1 || review.Files[0] != capture.Files[0] {
		t.Fatal("Downloaded review differed from original capture")
	}
	for _, name := range []string{"path", "bytes", "secret", "extra", "trailing"} {
		t.Run(name, func(t *testing.T) {
			manifest, err := transfer.Decode(capture.Payload)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "path":
				manifest.Files[0].Path = "../outside"
			case "bytes":
				manifest.Files[0].Data = []byte("changed")
			case "secret":
				manifest.Files[0].Path = ".env"
			}
			payload, _ := json.Marshal(manifest)
			if name == "extra" {
				payload = append([]byte(`{"unknown":true,`), payload[1:]...)
			}
			if name == "trailing" {
				payload = append(payload, []byte("{}")...)
			}
			if _, err = a.Run(t.Context(), "transfer-review", bytes.NewReader(payload)); err == nil {
				t.Fatal("Accepted invalid review")
			}
		})
	}
}
