package localcli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func installFixture(t *testing.T, provider string) (*App, TransferInstallInput) {
	t.Helper()
	a, stage, _ := stageFixture(t)
	manifest, err := transfer.Decode(stage.Payload)
	if err != nil {
		t.Fatal(err)
	}
	manifest.History = nil
	stage.Payload, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	stage.PayloadSHA256 = installHash(stage.Payload)
	stage.ReviewSHA256, err = manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(a.Config.Home, "."+strings.ToLower(provider))
	marker := "sessions"
	if provider == "Claude" {
		marker = "projects"
	}
	if err := os.MkdirAll(filepath.Join(root, marker), 0700); err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	parent, child, owner, camp := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	message := func(id string, ordinal int) map[string]any {
		return map[string]any{"id": id, "ordinal": ordinal, "role": "assistant", "text": "Preserved 🦫 history", "model": "fixture-model", "effort": "high", "speed": "fast", "revision": 1, "removed": false}
	}
	archive := map[string]any{"version": 1, "origin": "https://camp.example.com", "session": map[string]any{
		"id": parent, "camp_id": camp, "owner_user_id": owner, "owner_device_id": uuid.NewString(), "profile_id": uuid.NewString(), "provider": provider, "execution_location": "cloud", "execution_workspace_id": uuid.NewString(), "execution_epoch": 2, "revision": 3, "transcript_revision": 4, "permission_revision": 2,
	}, "conversations": []any{
		map[string]any{"id": parent, "messages": []any{message("main-1", 0), message("main-2", 9)}},
		map[string]any{"id": child, "child": map[string]any{"id": child, "parent_id": parent, "name": "Research", "source_key": strings.Repeat("a", 64), "source_revision": strings.Repeat("b", 64), "complete": true, "removed": false, "revision": 3, "transcript_revision": 2}, "messages": []any{message("child-1", 0)}},
	}}
	raw, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	stage.Scope = installHash([]byte("https://camp.example.com\n" + owner + "\n" + camp))
	var projected installedHistory
	if err = json.Unmarshal(raw, &projected); err != nil {
		t.Fatal(err)
	}
	ss := projected.Session
	export := TransferInstallExport{ID: uuid.NewString(), CampID: ss.CampID, SessionID: ss.ID, OwnerID: ss.OwnerID, DeviceID: uuid.NewString(), WorkspaceID: ss.WorkspaceID, WorkspaceRevision: 1, Epoch: ss.Epoch, Revision: ss.Revision, TranscriptRevision: ss.TranscriptRevision, PermissionRevision: ss.PermissionRevision, Kind: "capture", Paths: []string{"file.txt"}, State: "ready", SHA256: stage.PayloadSHA256, Bytes: len(stage.Payload), ExpiresAt: time.Now().Add(time.Hour)}
	return a, TransferInstallInput{TransferStageInput: stage, Export: export, History: raw, HistorySHA256: installHash(raw), Target: TransferInstallTarget{ProfileID: uuid.NewString(), SourceID: provider + ":" + root, Provider: provider, Root: root, SessionID: uuid.NewString(), Purpose: "implementation"}}
}
func installCall(ctx context.Context, a *App, in TransferInstallInput) (TransferInstallResult, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return TransferInstallResult{}, err
	}
	result, err := a.Run(ctx, "transfer-install", bytes.NewReader(raw))
	if err != nil {
		return TransferInstallResult{}, err
	}
	return result.(TransferInstallResult), nil
}
func installDirectory(a *App, in TransferInstallInput) string {
	return filepath.Join(a.Config.Support, "restore-"+in.Scope+"-"+in.TransferID)
}

func TestLocalTransferInstallRestart(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			a, in := installFixture(t, provider)
			result, err := installCall(t.Context(), a, in)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(result.Receipt)
			if result.ReceiptSHA256 != installHash(encoded) || result.Receipt.Target != in.Target || result.Receipt.TranscriptRevision != 4 {
				t.Fatal("Wrong installation receipt")
			}
			again, err := installCall(t.Context(), New(a.Config), in)
			if err != nil || again != result {
				t.Fatal("Lost reply did not recover", err)
			}
			for _, name := range []string{"intent.json", "history.json", "installation.json"} {
				path := filepath.Join(installDirectory(a, in), name)
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("Installation is not private", err)
				}
				data, _ := os.ReadFile(path)
				want := encoded
				if name == "history.json" {
					want = in.History
				}
				if !bytes.Equal(data, want) {
					t.Fatal("Archive changed or metadata was lost")
				}
			}
			entries, err := os.ReadDir(in.Target.Root)
			if err != nil || len(entries) != 1 {
				t.Fatal("Provider profile modified", err)
			}
			saved, _ := os.ReadFile(filepath.Join(in.Destination, "file.txt"))
			if string(saved) != "after\n" {
				t.Fatal("Files not installed")
			}
		})
	}
}

func TestLocalTransferInstallRefusesRebindingAndCorruption(t *testing.T) {
	for _, kind := range []string{"profile", "session", "purpose", "history", "destination", "new-edits", "missing-history", "missing-intent", "corrupt-history", "public-history", "history-symlink", "history-hardlink", "receipt-symlink"} {
		t.Run(kind, func(t *testing.T) {
			a, in := installFixture(t, "Codex")
			if _, err := installCall(t.Context(), a, in); err != nil {
				t.Fatal(err)
			}
			state := installDirectory(a, in)
			history := filepath.Join(state, "history.json")
			switch kind {
			case "profile":
				in.Target.ProfileID = uuid.NewString()
			case "session":
				in.Target.SessionID = uuid.NewString()
			case "purpose":
				in.Target.Purpose = "planning"
			case "history":
				in.History = bytes.ReplaceAll(in.History, []byte("Preserved"), []byte("Different"))
				in.HistorySHA256 = installHash(in.History)
			case "destination":
				original := in.Destination
				in.Destination = filepath.Join(t.TempDir(), "other")
				cloneStageFixture(t, original, in.Destination)
			case "new-edits":
				write(t, filepath.Join(in.Destination, "file.txt"), "keep this")
			case "missing-history":
				if err := os.Remove(history); err != nil {
					t.Fatal(err)
				}
			case "missing-intent":
				if err := os.Remove(filepath.Join(state, "intent.json")); err != nil {
					t.Fatal(err)
				}
			case "corrupt-history":
				write(t, history, "corrupted")
			case "public-history":
				if err := os.Chmod(history, 0644); err != nil {
					t.Fatal(err)
				}
			case "history-symlink", "history-hardlink", "receipt-symlink":
				target := filepath.Join(t.TempDir(), "outside")
				write(t, target, string(in.History))
				name := history
				if kind == "receipt-symlink" {
					name = filepath.Join(state, "installation.json")
				}
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "history-hardlink" {
					err = os.Link(target, name)
				} else {
					err = os.Symlink(target, name)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := installCall(t.Context(), New(a.Config), in); err == nil {
				t.Fatal("Accepted changed installation")
			}
			if kind == "new-edits" {
				raw, _ := os.ReadFile(filepath.Join(in.Destination, "file.txt"))
				if string(raw) != "keep this" {
					t.Fatal("Overwrote local work")
				}
			}
		})
	}
}

func TestLocalTransferInstallInputAndPartialRecovery(t *testing.T) {
	for _, kind := range []string{"hash", "scope", "provider", "unknown-profile", "target-id", "purpose", "bad-message", "duplicate", "child-parent", "trailing", "cancelled", "export-session", "export-revision", "export-hash", "export-expired"} {
		t.Run(kind, func(t *testing.T) {
			a, in := installFixture(t, "Claude")
			ctx := t.Context()
			switch kind {
			case "export-session":
				in.Export.SessionID = uuid.NewString()
			case "export-revision":
				in.Export.TranscriptRevision++
			case "export-hash":
				in.Export.SHA256 = strings.Repeat("0", 64)
			case "export-expired":
				in.Export.ExpiresAt = time.Now().Add(-time.Second)
			case "hash":
				in.HistorySHA256 = strings.Repeat("0", 64)
			case "scope":
				in.Scope = strings.Repeat("0", 64)
			case "provider":
				in.Target.Provider = "Codex"
			case "unknown-profile":
				in.Target.SourceID = "other"
			case "target-id":
				in.Target.SessionID = "not-a-uuid"
			case "purpose":
				in.Target.Purpose = "run"
			case "bad-message", "duplicate", "child-parent":
				var h map[string]any
				_ = json.Unmarshal(in.History, &h)
				conversations := h["conversations"].([]any)
				first := conversations[0].(map[string]any)
				messages := first["messages"].([]any)
				if kind == "bad-message" {
					messages[0].(map[string]any)["revision"] = 100
				}
				if kind == "duplicate" {
					messages[1].(map[string]any)["id"] = "main-1"
				}
				if kind == "child-parent" {
					conversations[1].(map[string]any)["child"].(map[string]any)["parent_id"] = uuid.NewString()
				}
				in.History, _ = json.Marshal(h)
				in.HistorySHA256 = installHash(in.History)
			case "trailing":
				in.History = append(in.History, []byte(" {}")...)
				in.HistorySHA256 = installHash(in.History)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := installCall(ctx, a, in); err == nil {
				t.Fatal("Accepted invalid installation")
			}
			raw, _ := os.ReadFile(filepath.Join(in.Destination, "file.txt"))
			if string(raw) != "before\n" {
				t.Fatal("Invalid input changed files")
			}
		})
	}
	for _, phase := range []string{"intent", "history", "files"} {
		t.Run("restart-"+phase, func(t *testing.T) {
			a, in := installFixture(t, "Codex")
			result, err := installCall(t.Context(), a, in)
			if err != nil {
				t.Fatal(err)
			}
			state := installDirectory(a, in)
			if err = os.Remove(filepath.Join(state, "installation.json")); err != nil {
				t.Fatal(err)
			}
			if phase != "files" {
				if err = os.Remove(filepath.Join(state, "stage.json")); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(in.Destination, "file.txt"), "before\n")
			}
			if phase == "intent" {
				if err = os.Remove(filepath.Join(state, "history.json")); err != nil {
					t.Fatal(err)
				}
			}
			recovered, err := installCall(t.Context(), New(a.Config), in)
			if err != nil || recovered != result {
				t.Fatal("Partial install failed recovery", err)
			}
		})
	}
}

func TestLocalTransferInstallExcludesConcurrentStager(t *testing.T) {
	a, in := installFixture(t, "Codex")
	stager, _, err := a.openTransferStager(t.Context(), in.TransferStageInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = installCall(t.Context(), a, in); err == nil {
		t.Fatal("Concurrent checkout writer admitted")
	}
	if err = stager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = installCall(t.Context(), a, in); err != nil {
		t.Fatal(err)
	}
	// The generic staging command still revalidates the same workspace journal.
	if _, err = stageCall(t, a, in.TransferStageInput); err != nil {
		t.Fatal(err)
	}
}

func TestLocalTransferInstallRecoversOrphanedTemporary(t *testing.T) {
	a, in := installFixture(t, "Codex")
	if _, err := installCall(t.Context(), a, in); err != nil {
		t.Fatal(err)
	}
	dir := installDirectory(a, in)
	orphan := filepath.Join(dir, ".install-"+uuid.NewString())
	unrelated := filepath.Join(dir, "keep.txt")
	write(t, orphan, "interrupted private write")
	write(t, unrelated, "retain")
	if _, err := installCall(t.Context(), New(a.Config), in); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("Orphaned temporary retained", err)
	}
	if raw, err := os.ReadFile(unrelated); err != nil || string(raw) != "retain" {
		t.Fatal("Removed unrelated state", err)
	}
	target := filepath.Join(t.TempDir(), "outside")
	write(t, target, "do not remove")
	if err := os.Symlink(target, orphan); err != nil {
		t.Fatal(err)
	}
	if _, err := installCall(t.Context(), New(a.Config), in); err == nil {
		t.Fatal("Followed unsafe temporary")
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "do not remove" {
		t.Fatal("Changed symlink target", err)
	}
}
