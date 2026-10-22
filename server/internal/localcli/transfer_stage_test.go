package localcli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func stageFixture(t *testing.T) (*App, TransferStageInput, string) {
	t.Helper()
	a, source := holdFixture(t)
	m, err := transfer.Decode(source.Payload)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	cloneStageFixture(t, source.Session.CWD, destination)
	review, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(source.Payload)
	a.commandPath = func(string, []string) (string, error) {
		t.Error("Staging launched a provider")
		return "", errors.New("unexpected provider")
	}
	return a, TransferStageInput{Scope: strings.Repeat("a", 64), TransferID: uuid.NewString(), Destination: destination,
		Branch: m.Branch, Payload: source.Payload, PayloadSHA256: hex.EncodeToString(sum[:]), ReviewSHA256: review}, source.Session.CWD
}

func cloneStageFixture(t *testing.T, source, destination string) {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", "-c", "core.hooksPath=/dev/null", "clone", "--no-local", "--no-hardlinks", "--", source, destination)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(data))
	}
	if err := os.Chmod(destination, 0700); err != nil {
		t.Fatal(err)
	}
}

func stageCall(t *testing.T, a *App, in TransferStageInput) (any, error) {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return a.Run(t.Context(), "transfer-stage", bytes.NewReader(body))
}

func TestLocalTransferStageRestartAndConflict(t *testing.T) {
	a, in, source := stageFixture(t)
	result, err := stageCall(t, a, in)
	if err != nil {
		t.Fatal(err)
	}
	receipt := result.(TransferStageResult)
	if receipt.Scope != in.Scope || receipt.TransferID != in.TransferID || receipt.PayloadSHA256 != in.PayloadSHA256 || receipt.Receipt.ReviewSHA256 != in.ReviewSHA256 || receipt.Receipt.Files != 1 {
		t.Fatal("wrong receipt")
	}
	for _, directory := range []string{source, in.Destination} {
		data, err := os.ReadFile(filepath.Join(directory, "file.txt"))
		if err != nil || string(data) != "after\n" {
			t.Fatal("Wrong staged content or source changed", err)
		}
	}
	restarted := New(a.Config)
	replayed, err := stageCall(t, restarted, in)
	if err != nil || replayed.(TransferStageResult) != receipt {
		t.Fatal("Restart did not reverify the same receipt", err)
	}
	// A lost reply must not allow the same transfer to point at a new checkout.
	other := in
	other.Destination = filepath.Join(t.TempDir(), "other")
	cloneStageFixture(t, source, other.Destination)
	if _, err = stageCall(t, restarted, other); err == nil {
		t.Fatal("Rebound a saved destination")
	}
	write(t, filepath.Join(in.Destination, "file.txt"), "newer local work\n")
	if _, err = stageCall(t, restarted, in); err == nil {
		t.Fatal("Overwrote newer local work")
	}
	data, _ := os.ReadFile(filepath.Join(in.Destination, "file.txt"))
	if string(data) != "newer local work\n" {
		t.Fatal("Conflict discarded local work")
	}
}

func TestLocalTransferStageRejectsUnreviewedAndUnsafeInputs(t *testing.T) {
	for _, kind := range []string{"payload-hash", "review-hash", "scope", "transfer", "relative", "branch", "dirty", "public", "symlink", "state-symlink"} {
		t.Run(kind, func(t *testing.T) {
			a, in, _ := stageFixture(t)
			original := in.Destination
			switch kind {
			case "payload-hash":
				in.PayloadSHA256 = strings.Repeat("0", 64)
			case "review-hash":
				in.ReviewSHA256 = strings.Repeat("0", 64)
			case "scope":
				in.Scope = "../escape"
			case "transfer":
				in.TransferID = "../escape"
			case "relative":
				in.Destination = "destination"
			case "branch":
				in.Branch = "different-branch"
			case "dirty":
				write(t, filepath.Join(in.Destination, "unrelated.txt"), "keep me")
			case "public":
				if err := os.Chmod(in.Destination, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				in.Destination = filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(original, in.Destination); err != nil {
					t.Fatal(err)
				}
			case "state-symlink":
				if err := os.MkdirAll(a.Config.Support, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(a.Config.Support, "restore-"+in.Scope+"-"+in.TransferID)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := stageCall(t, a, in); err == nil {
				t.Fatal("Accepted unsafe staging")
			}
			data, err := os.ReadFile(filepath.Join(original, "file.txt"))
			if err != nil || string(data) != "before\n" {
				t.Fatal("Rejected request changed files", err)
			}
		})
	}
}

func TestLocalTransferStageStrictDecode(t *testing.T) {
	a, in, _ := stageFixture(t)
	body, _ := json.Marshal(in)
	for _, raw := range [][]byte{append(append([]byte{}, body...), []byte(" {}")...), append([]byte(`{"unexpected":true,`), body[1:]...)} {
		if _, err := a.Run(t.Context(), "transfer-stage", bytes.NewReader(raw)); err == nil {
			t.Fatal("Accepted malformed envelope")
		}
	}
	data, _ := os.ReadFile(filepath.Join(in.Destination, "file.txt"))
	if string(data) != "before\n" {
		t.Fatal("Malformed input changed files")
	}
}
