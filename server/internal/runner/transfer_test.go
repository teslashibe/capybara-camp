package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func TestWorkerStagesTransferThroughAPIAndRecoversLostReplies(t *testing.T) {
	testWorkerStagesTransfer(t, false)
}

func TestWorkerStagesUnpublishedCommitsThroughAPIAndRecoversLostReplies(t *testing.T) {
	testWorkerStagesTransfer(t, true)
}

func testWorkerStagesTransfer(t *testing.T, unpublished bool) {
	var remoteDir string
	remote, sha, fetches := gitFixtureServer(t, func(dir string) { remoteDir = dir }, nil)
	var enabled atomic.Bool
	var chunks, reports atomic.Int32
	f, g, provider, source := workerWorkspaceFixture(t, remote, sha, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if enabled.Load() {
				var count *atomic.Int32
				if strings.HasSuffix(r.URL.Path, "/transfer/chunks/0") {
					count = &chunks
				}
				if strings.HasSuffix(r.URL.Path, "/report") {
					count = &reports
				}
				if count != nil && count.Add(1) == 1 {
					next.ServeHTTP(discardResponse{}, r)
					w.WriteHeader(503)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	cancel, done := startWorker(t, f.worker)
	eventually(t, func() bool {
		w, err := g.Workspace(t.Context(), f.owner, source.CampID, source.ID)
		return err == nil && w.State == "ready"
	})
	eventually(t, func() bool { r, err := f.worker.journal.loadWorkspace(); return err == nil && r == nil })
	stopWorker(t, cancel, done)
	session, err := f.store.Session(t.Context(), f.owner, source.CampID, source.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("transferred version\n")
	manifest := transfer.Manifest{Version: 1, BaseCommit: sha, Branch: "source-branch", Files: []transfer.File{{Path: "README.md", BeforeSHA256: payloadHash([]byte("pinned version\n")), SHA256: payloadHash(content), Data: content}}}
	if unpublished {
		local := privateTemp(t)
		fixtureCommand(t, local, "clone", "--no-hardlinks", remoteDir, ".")
		if err = os.WriteFile(filepath.Join(local, "committed.txt"), []byte("Unpublished contents\n"), 0600); err != nil {
			t.Fatal(err)
		}
		fixtureCommand(t, local, "add", ".")
		fixtureCommand(t, local, "commit", "-m", "Unpublished commit")
		if err = os.WriteFile(filepath.Join(local, "README.md"), content, 0600); err != nil {
			t.Fatal(err)
		}
		manifest, err = transfer.SnapshotWithCommits(t.Context(), local, []string{"README.md"}, sha)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(fixtureCommand(t, remoteDir, "rev-list", "--all"), manifest.BaseCommit) {
			t.Fatal("Fixture published the source commit")
		}
	}
	manifest.History = &transfer.HistoryBinding{Version: 1, ParentID: strings.Repeat("c", 64), MainRevision: strings.Repeat("d", 64), Children: []transfer.HistoryChild{}, TranscriptRevision: session.TranscriptRevision}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	review, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	upload, err := f.store.CreateTransfer(t.Context(), f.owner, source.CampID, source.SessionID, uuid.NewString(), camp.TransferInput{TargetLocation: "cloud", TargetAccountID: source.ProviderAccountID, ExpectedEpoch: session.ExecutionEpoch, ExpectedRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision, ExpectedPermissionRevision: session.PermissionRevision, PayloadSHA256: payloadHash(raw), ReviewSHA256: review, PayloadBytes: len(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.PutTransferChunk(t.Context(), f.owner, source.CampID, source.SessionID, upload.ID, 0, camp.TransferChunk{Data: raw, SHA256: payloadHash(raw)}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.CompleteTransfer(t.Context(), f.owner, source.CampID, source.SessionID, upload.ID); err != nil {
		t.Fatal(err)
	}
	target, err := g.CreateTransferWorkspace(t.Context(), f.owner, source.CampID, source.SessionID, upload.ID, uuid.NewString(), camp.WorkspaceInput{ProviderAccountID: source.ProviderAccountID, GitConnectionID: source.GitConnectionID, GitHubInstallationID: source.GitHubInstallationID, RepositoryID: source.RepositoryID, InstallationPage: 1, RepositoryPage: 1, Purpose: source.Purpose, ExpectedSessionRevision: session.Revision, ExpectedProjectRevision: source.ProjectRevision})
	if err != nil {
		t.Fatal(err)
	}
	if unpublished && (target.RemoteBaseCommit != sha || target.BaseCommit != manifest.BaseCommit || target.RequestedRef != sha) {
		t.Fatal("Destination lost authorized ancestor or reviewed head")
	}
	enabled.Store(true)
	cancel, done = startWorker(t, f.worker)
	eventually(t, func() bool {
		w, err := g.Workspace(t.Context(), f.owner, target.CampID, target.ID)
		return err == nil && w.State == "ready"
	})
	eventually(t, func() bool { r, err := f.worker.journal.loadWorkspace(); return err == nil && r == nil })
	stopWorker(t, cancel, done)
	if chunks.Load() != 2 || reports.Load() != 2 || provider.scopes.Load() != 2 || fetches.Load() != 2 {
		t.Fatal("lost replies repeated preparation", chunks.Load(), reports.Load(), provider.scopes.Load(), fetches.Load())
	}
	if err = f.worker.checkout.verify(t.Context(), target); err != nil {
		t.Fatal("staged checkout did not verify", err)
	}
	if unpublished {
		if fixtureCommand(t, f.worker.checkout.workdir(target), "rev-parse", "HEAD") != manifest.BaseCommit {
			t.Fatal("Destination substituted the remote ancestor")
		}
		committed, err := os.ReadFile(filepath.Join(f.worker.checkout.workdir(target), "committed.txt"))
		if err != nil || string(committed) != "Unpublished contents\n" {
			t.Fatal("Unpublished contents missing", err)
		}
	}
	data, err := os.ReadFile(filepath.Join(f.worker.checkout.workdir(target), "README.md"))
	if err != nil || !bytes.Equal(data, content) {
		t.Fatal("destination contents", err)
	}
	data, err = os.ReadFile(filepath.Join(f.worker.checkout.workdir(source), "README.md"))
	if err != nil || string(data) != "pinned version\n" {
		t.Fatal("source contents changed", err)
	}
	current, err := f.store.Session(t.Context(), f.owner, source.CampID, source.SessionID)
	if err != nil || !reflect.DeepEqual(current, session) {
		t.Fatal("staging moved source session", err)
	}
	if _, err = f.store.CancelTransfer(t.Context(), f.owner, source.CampID, source.SessionID, upload.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.worker.cleanupRetiredWorkspaces(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(f.worker.checkout.workdir(target)); err != nil {
		t.Fatal("cancellation discarded staged changes", err)
	}
	data, err = os.ReadFile(filepath.Join(f.worker.checkout.workdir(source), "README.md"))
	if err != nil || string(data) != "pinned version\n" {
		t.Fatal("cancellation discarded source", err)
	}
}

func TestTransferCheckoutBindingAndMissingJournalFailClosed(t *testing.T) {
	remote, sha, _ := gitFixtureServer(t, nil, nil)
	c, w, token := checkoutFixture(t, remote, sha)
	w.TransferID = uuid.NewString()
	if err := c.prepare(t.Context(), w, token); err != nil {
		t.Fatal(err)
	}
	if err := c.saveJSON(transferMarker(w), w); err != nil {
		t.Fatal(err)
	}
	if err := c.verify(t.Context(), w); err == nil {
		t.Fatal("held checkout passed without a staging receipt")
	}
	changed := w
	changed.TransferID = uuid.NewString()
	if _, err := c.transferHeld(changed); !errors.Is(err, ErrState) {
		t.Fatal("another transfer inherited the checkpoint", err)
	}
	if err := c.removeUnused(w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.workdir(w)); err != nil {
		t.Fatal("held checkout was erased", err)
	}
}

func TestUnpublishedCheckoutRejectsWrongBindingAndRecoversBeforeStaging(t *testing.T) {
	var remoteDir string
	remote, base, fetches := gitFixtureServer(t, func(dir string) { remoteDir = dir }, nil)
	local := privateTemp(t)
	fixtureCommand(t, local, "clone", "--no-hardlinks", remoteDir, ".")
	if err := os.WriteFile(filepath.Join(local, "new.txt"), []byte("Keep unpublished work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, local, "add", ".")
	fixtureCommand(t, local, "commit", "-m", "Unpublished fixture")
	head := fixtureCommand(t, local, "rev-parse", "HEAD")
	archive, err := transfer.CaptureCommits(t.Context(), local, base, head)
	if err != nil {
		t.Fatal(err)
	}
	c, w, token := checkoutFixture(t, remote, head)
	w.TransferID, w.RemoteBaseCommit = uuid.NewString(), base
	if err := c.prepare(t.Context(), w, token); !errors.Is(err, ErrState) {
		t.Fatal("Missing archive accepted", err)
	}
	wrong := archive
	wrong.HeadCommit = base
	if err := c.prepareCommits(t.Context(), w, token, &wrong); !errors.Is(err, ErrState) {
		t.Fatal("Wrong archive accepted", err)
	}
	if fetches.Load() != 0 {
		t.Fatal("Invalid request fetched repository")
	}
	for i := 0; i < 2; i++ {
		// No staging marker yet: interruption may safely repeat read-only
		// preparation in this unused destination, retaining the source.
		if err := c.prepareCommits(t.Context(), w, token, &archive); err != nil {
			t.Fatal(err)
		}
		if fixtureCommand(t, c.workdir(w), "rev-parse", "HEAD") != head {
			t.Fatal("Recovery lost reviewed head")
		}
	}
	if err := c.saveJSON(transferMarker(w), w); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.workdir(w), "retained.txt"), []byte("Retain staged work"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.prepareCommits(t.Context(), w, token, &archive); err == nil {
		t.Fatal("Staged checkout recreated")
	}
	data, err := os.ReadFile(filepath.Join(c.workdir(w), "retained.txt"))
	if err != nil || string(data) != "Retain staged work" {
		t.Fatal("Staged work was erased", err)
	}
	changed := w
	changed.RemoteBaseCommit = strings.Repeat("e", 40)
	if _, err := c.transferHeld(changed); !errors.Is(err, ErrState) {
		t.Fatal("Changed ancestor inherited staging marker", err)
	}
}
