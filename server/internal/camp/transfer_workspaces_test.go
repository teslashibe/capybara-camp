package camp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/testgit"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

type transferWorkspaceFixture struct {
	git      *GitStore
	provider *workspaceGit
	owner    auth.Actor
	camp     Camp
	session  Session
	source   Workspace
	target   Workspace
	upload   Transfer
	grant    RunnerGrant
	input    WorkspaceInput
	raw      []byte
}

func TestTransferWorkspaceAuthorizesRemoteAncestorAndRetainsReviewedHead(t *testing.T) {
	g, provider, actors, c, session, input := workspaceFixture(t)
	owner := actors[0]
	dir := t.TempDir()
	testgit.Command(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("Base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testgit.Command(t, dir, "add", ".")
	testgit.Command(t, dir, "commit", "-qm", "Remote ancestor")
	base := testgit.Command(t, dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("Unpublished\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testgit.Command(t, dir, "add", ".")
	testgit.Command(t, dir, "commit", "-qm", "Unpublished")
	manifest, err := transfer.SnapshotWithCommits(t.Context(), dir, []string{}, base)
	if err != nil {
		t.Fatal(err)
	}
	create, _ := transferPayload(t, session, []byte("unused"))
	manifest.History = &transfer.HistoryBinding{Version: 1, ParentID: strings.Repeat("c", 64), MainRevision: strings.Repeat("d", 64), Children: []transfer.HistoryChild{}, TranscriptRevision: session.TranscriptRevision}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	create.TargetLocation, create.TargetAccountID = "cloud", input.ProviderAccountID
	create.PayloadBytes, create.PayloadSHA256 = len(raw), transferDigest(raw)
	create.ReviewSHA256, err = manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	upload, err := g.store.CreateTransfer(t.Context(), owner, c.ID, session.ID, uuid.NewString(), create)
	if err != nil {
		t.Fatal(err)
	}
	upload = uploadTransfer(t, g.store, owner, c, session, upload, raw)
	upload, err = g.store.CompleteTransfer(t.Context(), owner, c.ID, session.ID, upload.ID)
	if err != nil || upload.RemoteBaseCommit != base || upload.BaseCommit != manifest.BaseCommit {
		t.Fatal("Transfer metadata lost ancestry", err)
	}
	input.Ref = manifest.BaseCommit
	request := uuid.NewString()
	// A valid-looking SHA from the remote is insufficient unless it is the
	// reviewed ancestor. No workspace is created on this failed authorization.
	if _, err := g.CreateTransferWorkspace(t.Context(), owner, c.ID, session.ID, upload.ID, request, input); !errors.Is(err, ErrRevision) {
		t.Fatal("Unrelated remote commit accepted", err)
	}
	provider.mu.Lock()
	provider.commit = base
	provider.mu.Unlock()
	target, err := g.CreateTransferWorkspace(t.Context(), owner, c.ID, session.ID, upload.ID, request, input)
	if err != nil {
		t.Fatal(err)
	}
	if target.BaseCommit != manifest.BaseCommit || target.RemoteBaseCommit != base || target.RequestedRef != base {
		t.Fatal("Workspace replaced reviewed head with remote ancestor")
	}
	provider.mu.Lock()
	refs := append([]string{}, provider.resolvedRefs...)
	provider.mu.Unlock()
	if len(refs) != 2 || refs[0] != base || refs[1] != base {
		t.Fatal("Remote resolution did not use pinned ancestor", refs)
	}
	again, err := g.CreateTransferWorkspace(t.Context(), owner, c.ID, session.ID, upload.ID, request, input)
	if err != nil || again != target {
		t.Fatal("Workspace replay changed ancestry", err)
	}
	if _, err := g.Workspace(t.Context(), actors[1], c.ID, target.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("Teammate read private workspace", err)
	}
	grant, err := g.store.CreateRunnerGrant(t.Context(), owner, input.ProviderAccountID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.store.pool.Exec(t.Context(), `UPDATE ticket_workspaces SET remote_base_commit=$2 WHERE id=$1`, target.ID, strings.Repeat("e", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ClaimRunnerWorkspace(t.Context(), grant.Token, target.ID, uuid.NewString()); !errors.Is(err, ErrRevision) {
		t.Fatal("Changed ancestor remained claimable", err)
	}
}

func newTransferWorkspaceFixture(t *testing.T, purpose ...string) transferWorkspaceFixture {
	t.Helper()
	g, provider, actors, c, session, input := workspaceFixture(t)
	owner := actors[0]
	old, err := g.CreateWorkspace(t.Context(), owner, c.ID, session.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := g.store.CreateRunnerGrant(t.Context(), owner, input.ProviderAccountID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerWorkspace(t.Context(), grant.Token, old.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = g.RunnerWorkspaceToken(t.Context(), grant.Token, old.ID, claim); err != nil {
		t.Fatal(err)
	}
	old, err = g.ReportRunnerWorkspace(t.Context(), grant.Token, old.ID, claim, WorkspaceReport{State: "ready", BaseCommit: old.BaseCommit, Branch: old.Branch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.AssignExecution(t.Context(), owner, c.ID, session.ID, uuid.NewString(), ExecutionInput{Location: "cloud", WorkspaceID: old.ID, ExpectedEpoch: session.ExecutionEpoch, ExpectedSessionRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision, ExpectedWorkspaceRevision: old.Revision}); err != nil {
		t.Fatal(err)
	}
	session, err = g.store.Session(t.Context(), owner, c.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	create, raw := transferPayload(t, session, []byte("reviewed workspace contents"))
	create.TargetLocation, create.TargetAccountID = "cloud", input.ProviderAccountID
	upload, err := g.store.CreateTransfer(t.Context(), owner, c.ID, session.ID, uuid.NewString(), create)
	if err != nil {
		t.Fatal(err)
	}
	upload = uploadTransfer(t, g.store, owner, c, session, upload, raw)
	upload, err = g.store.CompleteTransfer(t.Context(), owner, c.ID, session.ID, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(purpose) > 0 {
		input.Purpose = purpose[0]
	}
	target, err := g.CreateTransferWorkspace(t.Context(), owner, c.ID, session.ID, upload.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	return transferWorkspaceFixture{g, provider, owner, c, session, old, target, upload, grant, input, raw}
}

func TestTransferWorkspaceKeepsSourceAndRequiresClaimedReceipt(t *testing.T) {
	f := newTransferWorkspaceFixture(t)
	g, ctx := f.git, context.Background()
	if f.target.TransferID != f.upload.ID || f.target.ID == f.source.ID || f.target.BaseCommit != f.upload.BaseCommit {
		t.Fatal("destination did not retain reviewed binding")
	}
	if _, err := g.CreateTransferWorkspace(ctx, f.owner, f.camp.ID, f.session.ID, f.upload.ID, uuid.NewString(), f.input); !errors.Is(err, ErrTransition) {
		t.Fatal("duplicate active destination", err)
	}
	values, err := g.SessionWorkspaces(ctx, f.owner, f.camp.ID, f.session.ID)
	if err != nil || len(values) != 2 {
		t.Fatal("source workspace was replaced", err)
	}
	next, err := g.NextRunnerWorkspace(ctx, f.grant.Token)
	if err != nil || next == nil || next.ID != f.target.ID {
		t.Fatal("transfer preparation was not scheduled", err)
	}
	claim := uuid.NewString()
	if _, err = g.RunnerTransfer(ctx, f.grant.Token, f.target.ID, claim); !errors.Is(err, ErrDenied) {
		t.Fatal("payload read before claim", err)
	}
	if _, err = g.ClaimRunnerWorkspace(ctx, f.grant.Token, f.target.ID, claim); err != nil {
		t.Fatal(err)
	}
	metadata, err := g.RunnerTransfer(ctx, f.grant.Token, f.target.ID, claim)
	if err != nil || metadata.ID != f.upload.ID || metadata.BaseCommit != f.target.BaseCommit || metadata.FileCount != 1 {
		t.Fatal("scoped metadata", err)
	}
	chunk, err := g.RunnerTransferChunk(ctx, f.grant.Token, f.target.ID, claim, 0)
	if err != nil || string(chunk.Data) != string(f.raw) {
		t.Fatal("scoped payload", err)
	}
	if _, err = g.RunnerTransferChunk(ctx, f.grant.Token, f.target.ID, uuid.NewString(), 0); !errors.Is(err, ErrDenied) {
		t.Fatal("other claim read payload", err)
	}
	_, other := addCloud(t, g.store, f.owner, "Codex")
	if _, err = g.RunnerTransfer(ctx, other.Token, f.target.ID, claim); !errors.Is(err, ErrDenied) {
		t.Fatal("other installation read payload", err)
	}
	if _, err = g.RunnerWorkspaceToken(ctx, f.grant.Token, f.target.ID, claim); err != nil {
		t.Fatal(err)
	}
	report := WorkspaceReport{State: "ready", BaseCommit: f.target.BaseCommit, Branch: f.target.Branch}
	if _, err = g.ReportRunnerWorkspace(ctx, f.grant.Token, f.target.ID, claim, report); !errors.Is(err, ErrInput) {
		t.Fatal("plain checkout reported as staged", err)
	}
	report.TransferReceipt = &transfer.StageReceipt{ReviewSHA256: metadata.ReviewSHA256, BaseCommit: metadata.BaseCommit, Branch: f.target.Branch, Files: metadata.FileCount}
	bad := *report.TransferReceipt
	bad.Files++
	report.TransferReceipt = &bad
	if _, err = g.ReportRunnerWorkspace(ctx, f.grant.Token, f.target.ID, claim, report); !errors.Is(err, ErrInput) {
		t.Fatal("incorrect receipt accepted", err)
	}
	bad.Files--
	report.TransferReceipt = &bad
	ready, err := g.ReportRunnerWorkspace(ctx, f.grant.Token, f.target.ID, claim, report)
	if err != nil || ready.State != "ready" {
		t.Fatal("staging receipt", err)
	}
	again, err := g.ReportRunnerWorkspace(ctx, f.grant.Token, f.target.ID, claim, report)
	if err != nil || again != ready {
		t.Fatal("receipt replay", err)
	}
	if _, err = g.RunnerTransferChunk(ctx, f.grant.Token, f.target.ID, claim, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("completed claim still read payload", err)
	}
	_, err = g.store.AssignExecution(ctx, f.owner, f.camp.ID, f.session.ID, uuid.NewString(), ExecutionInput{Location: "cloud", WorkspaceID: ready.ID, ExpectedEpoch: f.session.ExecutionEpoch, ExpectedSessionRevision: f.session.Revision, ExpectedTranscriptRevision: f.session.TranscriptRevision, ExpectedWorkspaceRevision: ready.Revision})
	if !errors.Is(err, ErrTransition) {
		t.Fatal("staged files bypassed session handoff", err)
	}
	current, err := g.store.Session(ctx, f.owner, f.camp.ID, f.session.ID)
	if err != nil || !reflect.DeepEqual(current, f.session) {
		t.Fatal("staging changed source assignment", err)
	}
	old, err := g.Workspace(ctx, f.owner, f.camp.ID, f.source.ID)
	if err != nil || old != f.source {
		t.Fatal("staging retired source checkout", err)
	}
}

func TestTransferWorkspaceExpiryRevocationAndSourceDrift(t *testing.T) {
	for _, kind := range []string{"cancel", "expiry", "transcript", "permission", "epoch"} {
		t.Run(kind, func(t *testing.T) {
			f := newTransferWorkspaceFixture(t)
			g, ctx := f.git, context.Background()
			claim := uuid.NewString()
			if _, err := g.ClaimRunnerWorkspace(ctx, f.grant.Token, f.target.ID, claim); err != nil {
				t.Fatal(err)
			}
			if _, err := g.RunnerWorkspaceToken(ctx, f.grant.Token, f.target.ID, claim); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "cancel":
				_, err = g.store.CancelTransfer(ctx, f.owner, f.camp.ID, f.session.ID, f.upload.ID)
			case "expiry":
				_, err = g.store.pool.Exec(ctx, `UPDATE session_transfers SET expires_at=now()-interval '1 second' WHERE id=$1`, f.upload.ID)
			case "transcript":
				_, err = g.store.pool.Exec(ctx, `UPDATE sessions SET transcript_revision=transcript_revision+1 WHERE id=$1`, f.session.ID)
			case "permission":
				_, err = g.store.pool.Exec(ctx, `UPDATE sessions SET permission_revision=permission_revision+1 WHERE id=$1`, f.session.ID)
			case "epoch":
				_, err = g.store.pool.Exec(ctx, `UPDATE sessions SET execution_epoch=execution_epoch+1 WHERE id=$1`, f.session.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = g.RunnerTransfer(ctx, f.grant.Token, f.target.ID, claim); err == nil {
				t.Fatal("stale claim read transfer")
			}
			if err = g.ReapWorkspaces(ctx); err != nil {
				t.Fatal(err)
			}
			w, err := g.Workspace(ctx, f.owner, f.camp.ID, f.target.ID)
			if err != nil || !oneOf(w.State, "failed", "cancelled") {
				t.Fatal("invalid destination retained", w.State, err)
			}
			var state string
			if err = g.store.pool.QueryRow(ctx, `SELECT token_state FROM ticket_workspaces WHERE id=$1`, f.target.ID).Scan(&state); err != nil || state != "closed" {
				t.Fatal("read token not retired", err)
			}
			old, err := g.Workspace(ctx, f.owner, f.camp.ID, f.source.ID)
			if err != nil || old.State != "ready" {
				t.Fatal("source checkout retired", err)
			}
		})
	}
}

func TestTransferWorkspaceRevalidatesSourceAcrossGitRead(t *testing.T) {
	f := newTransferWorkspaceFixture(t)
	ctx := context.Background()
	if _, err := f.git.CancelWorkspace(ctx, f.owner, f.camp.ID, f.target.ID, uuid.NewString(), WorkspaceCancel{ExpectedRevision: f.target.Revision}); err != nil {
		t.Fatal(err)
	}
	f.provider.resolveStarted = make(chan struct{}, 1)
	f.provider.resolveProceed = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := f.git.CreateTransferWorkspace(ctx, f.owner, f.camp.ID, f.session.ID, f.upload.ID, uuid.NewString(), f.input)
		done <- err
	}()
	<-f.provider.resolveStarted
	if _, err := f.git.store.pool.Exec(ctx, `UPDATE sessions SET transcript_revision=transcript_revision+1 WHERE id=$1`, f.session.ID); err != nil {
		t.Fatal(err)
	}
	close(f.provider.resolveProceed)
	if err := <-done; !errors.Is(err, ErrRevision) {
		t.Fatal("source changed during Git read", err)
	}
}
