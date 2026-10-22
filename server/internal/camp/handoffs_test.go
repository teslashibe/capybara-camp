package camp

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func handoffFixture(t *testing.T, local bool) (transferWorkspaceFixture, Handoff) {
	t.Helper()
	var f transferWorkspaceFixture
	if !local {
		f = newTransferWorkspaceFixture(t)
	} else {
		g, provider, actors, c, session, input := workspaceFixture(t)
		create, raw := transferPayload(t, session, []byte("reviewed source"))
		create.TargetLocation = "cloud"
		create.TargetAccountID = input.ProviderAccountID
		upload, err := g.store.CreateTransfer(t.Context(), actors[0], c.ID, session.ID, uuid.NewString(), create)
		if err != nil {
			t.Fatal(err)
		}
		upload = uploadTransfer(t, g.store, actors[0], c, session, upload, raw)
		upload, err = g.store.CompleteTransfer(t.Context(), actors[0], c.ID, session.ID, upload.ID)
		if err != nil {
			t.Fatal(err)
		}
		target, err := g.CreateTransferWorkspace(t.Context(), actors[0], c.ID, session.ID, upload.ID, uuid.NewString(), input)
		if err != nil {
			t.Fatal(err)
		}
		grant, err := g.store.CreateRunnerGrant(t.Context(), actors[0], input.ProviderAccountID, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		f = transferWorkspaceFixture{git: g, provider: provider, owner: actors[0], camp: c, session: session, target: target, upload: upload, grant: grant, input: input, raw: raw}
	}
	claim := uuid.NewString()
	if _, err := f.git.ClaimRunnerWorkspace(t.Context(), f.grant.Token, f.target.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := f.git.RunnerWorkspaceToken(t.Context(), f.grant.Token, f.target.ID, claim); err != nil {
		t.Fatal(err)
	}
	var err error
	f.target, err = f.git.ReportRunnerWorkspace(t.Context(), f.grant.Token, f.target.ID, claim, WorkspaceReport{State: "ready", BaseCommit: f.target.BaseCommit, Branch: f.target.Branch, TransferReceipt: &transfer.StageReceipt{ReviewSHA256: f.upload.ReviewSHA256, BaseCommit: f.upload.BaseCommit, Branch: f.target.Branch, Files: f.upload.FileCount}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.git.store.BeginHandoff(t.Context(), f.owner, f.camp.ID, f.session.ID, f.upload.ID, uuid.NewString(), HandoffInput{WorkspaceID: f.target.ID, ExpectedWorkspaceRevision: f.target.Revision, ExpectedTransferRevision: f.upload.Revision})
	if err != nil {
		t.Fatal(err)
	}
	return f, h
}

func TestLocalHandoffRequiresOriginalDeviceAndBlocksNewWork(t *testing.T) {
	f, h := handoffFixture(t, true)
	s, ctx := f.git.store, context.Background()
	if h.State != "pending" || h.SourceLocation != "local" {
		t.Fatal(h)
	}
	if _, err := s.CreateRequest(ctx, f.owner, f.camp.ID, f.session.ID, uuid.NewString(), RequestInput{Prompt: "Should wait"}); !errors.Is(err, ErrTransferHeld) {
		t.Fatal("handoff allowed a new prompt", err)
	}
	if _, err := s.AssignExecution(ctx, f.owner, f.camp.ID, f.session.ID, uuid.NewString(), ExecutionInput{Location: "local", ExpectedEpoch: f.session.ExecutionEpoch, ExpectedSessionRevision: f.session.Revision, ExpectedTranscriptRevision: f.session.TranscriptRevision}); !errors.Is(err, ErrTransferHeld) {
		t.Fatal("handoff allowed another assignment", err)
	}
	next, err := s.NextLocalHandoff(ctx, f.owner, f.camp.ID)
	if err != nil || next != h.ID {
		t.Fatal("source did not discover handoff", err)
	}
	other := f.owner
	other.DeviceID = uuid.NewString()
	if _, err = s.ClaimHandoff(ctx, other, "", h.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
		t.Fatal("other device claimed source", err)
	}
	claim := uuid.NewString()
	h, err = s.ClaimHandoff(ctx, f.owner, "", h.ID, claim)
	if err != nil || h.State != "claimed" {
		t.Fatal(err)
	}
	if _, err = s.HandoffSource(ctx, other, "", h.ID, claim); !errors.Is(err, ErrDenied) {
		t.Fatal("other device read source", err)
	}
	source, err := s.HandoffSource(ctx, f.owner, "", h.ID, claim)
	if err != nil || source.Workspace != nil || source.Transfer.ID != f.upload.ID {
		t.Fatal("wrong local source metadata", err)
	}
	chunk, err := s.HandoffChunk(ctx, f.owner, "", h.ID, claim, 0)
	if err != nil || !bytes.Equal(chunk.Data, f.raw) {
		t.Fatal("source could not read reviewed bytes", err)
	}
	ack := HandoffAck{ClaimID: claim, Stopped: true, PayloadSHA256: f.upload.PayloadSHA256, ReviewSHA256: f.upload.ReviewSHA256}
	bad := ack
	bad.Stopped = false
	if _, err = s.AcknowledgeHandoff(ctx, f.owner, "", h.ID, bad); !errors.Is(err, ErrInput) {
		t.Fatal("running source acknowledged", err)
	}
	bad = ack
	bad.ReviewSHA256 = "wrong"
	if _, err = s.AcknowledgeHandoff(ctx, f.owner, "", h.ID, bad); !errors.Is(err, ErrInput) {
		t.Fatal("wrong review acknowledged", err)
	}
	h, err = s.AcknowledgeHandoff(ctx, f.owner, "", h.ID, ack)
	if err != nil || h.State != "acknowledged" {
		t.Fatal(err)
	}
	replay, err := s.AcknowledgeHandoff(ctx, f.owner, "", h.ID, ack)
	if err != nil || replay.Revision != h.Revision {
		t.Fatal("source ack duplicated", err)
	}
	if _, err = s.CreateRequest(ctx, f.owner, f.camp.ID, f.session.ID, uuid.NewString(), RequestInput{Prompt: "Still wait"}); !errors.Is(err, ErrTransferHeld) {
		t.Fatal("source ack unlocked before commit", err)
	}
	h, err = s.ReleaseHandoff(ctx, f.owner, f.camp.ID, f.session.ID, h.ID)
	if err != nil || h.State != "released" {
		t.Fatal(err)
	}
	if _, err = s.CreateRequest(ctx, f.owner, f.camp.ID, f.session.ID, uuid.NewString(), RequestInput{Prompt: "Resume source"}); err != nil {
		t.Fatal("release did not restore prompts", err)
	}
	current, err := s.Session(ctx, f.owner, f.camp.ID, f.session.ID)
	if err != nil || current.ExecutionEpoch != f.session.ExecutionEpoch || current.ExecutionLocation != "local" {
		t.Fatal("hold moved the session", err)
	}
}

func TestCloudHandoffRequiresSourceInstallation(t *testing.T) {
	f, h := handoffFixture(t, false)
	s, ctx := f.git.store, context.Background()
	next, err := s.NextRunnerHandoff(ctx, f.grant.Token)
	if err != nil || next != h.ID {
		t.Fatal("cloud source did not discover handoff", err)
	}
	if next, err = s.NextLocalHandoff(ctx, f.owner, f.camp.ID); err != nil || next != "" {
		t.Fatal("cloud source appeared as local work", err)
	}
	_, other := addCloud(t, s, f.owner, "Codex")
	if _, err = s.ClaimHandoff(ctx, auth.Actor{}, other.Token, h.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign installation claimed source", err)
	}
	if _, err = s.ClaimHandoff(ctx, f.owner, "", h.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
		t.Fatal("Mac acknowledged cloud source", err)
	}
	claim := uuid.NewString()
	if _, err = s.ClaimHandoff(ctx, auth.Actor{}, f.grant.Token, h.ID, claim); err != nil {
		t.Fatal(err)
	}
	source, err := s.HandoffSource(ctx, auth.Actor{}, f.grant.Token, h.ID, claim)
	if err != nil || source.Workspace == nil || source.Workspace.ID != f.source.ID {
		t.Fatal("wrong cloud source", err)
	}
	if _, err = s.ClaimHandoff(ctx, auth.Actor{}, f.grant.Token, h.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
		t.Fatal("source claim replaced", err)
	}
	if _, err = s.AcknowledgeHandoff(ctx, auth.Actor{}, f.grant.Token, h.ID, HandoffAck{ClaimID: claim, Stopped: true, PayloadSHA256: f.upload.PayloadSHA256, ReviewSHA256: f.upload.ReviewSHA256}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.git.ClaimRunnerRequest(ctx, f.grant.Token, f.source.ID, uuid.NewString()); err != nil && !errors.Is(err, ErrTransferHeld) {
		t.Fatal(err)
	}
	if _, err = s.RejectHandoff(ctx, auth.Actor{}, f.grant.Token, h.ID, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateRequest(ctx, f.owner, f.camp.ID, f.session.ID, uuid.NewString(), RequestInput{Prompt: "Source resumes"}); err != nil {
		t.Fatal(err)
	}
}

func TestHandoffExpiryCancellationAndDriftReleaseGuard(t *testing.T) {
	for _, kind := range []string{"expiry", "cancel", "drift"} {
		t.Run(kind, func(t *testing.T) {
			f, h := handoffFixture(t, true)
			s, ctx := f.git.store, context.Background()
			claim := uuid.NewString()
			if _, err := s.ClaimHandoff(ctx, f.owner, "", h.ID, claim); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "expiry":
				_, err = s.pool.Exec(ctx, `UPDATE transfer_handoffs SET expires_at=now()-interval '1 second' WHERE id=$1`, h.ID)
			case "cancel":
				_, err = s.CancelTransfer(ctx, f.owner, f.camp.ID, f.session.ID, f.upload.ID)
			case "drift":
				_, err = s.pool.Exec(ctx, `UPDATE sessions SET transcript_revision=transcript_revision+1 WHERE id=$1`, f.session.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.AcknowledgeHandoff(ctx, f.owner, "", h.ID, HandoffAck{ClaimID: claim, Stopped: true, PayloadSHA256: f.upload.PayloadSHA256, ReviewSHA256: f.upload.ReviewSHA256}); err == nil {
				t.Fatal("stale source acknowledged")
			}
			observed, err := s.Handoff(ctx, f.owner, f.camp.ID, f.session.ID, h.ID)
			if err != nil || !oneOf(observed.State, "expired", "invalidated") {
				t.Fatal("stale hold remained active", observed.State, err)
			}
			if _, err = s.CreateRequest(ctx, f.owner, f.camp.ID, f.session.ID, uuid.NewString(), RequestInput{Prompt: "Continue"}); err != nil {
				t.Fatal("stale hold blocked source", err)
			}
		})
	}
}

func TestHandoffCreationIsExclusiveAndRefusesQueuedPrompts(t *testing.T) {
	f, h := handoffFixture(t, true)
	s, ctx := f.git.store, context.Background()
	if _, err := s.ReleaseHandoff(ctx, f.owner, f.camp.ID, f.session.ID, h.ID); err != nil {
		t.Fatal(err)
	}
	in := HandoffInput{WorkspaceID: f.target.ID, ExpectedWorkspaceRevision: f.target.Revision, ExpectedTransferRevision: f.upload.Revision}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.BeginHandoff(ctx, f.owner, f.camp.ID, f.session.ID, f.upload.ID, uuid.NewString(), in)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	wins := 0
	for err := range errs {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrTransferHeld) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("nonexclusive source hold", wins)
	}
	var id string
	if err := s.pool.QueryRow(ctx, `SELECT id FROM transfer_handoffs WHERE session_id=$1 AND state='pending'`, f.session.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseHandoff(ctx, f.owner, f.camp.ID, f.session.ID, id); err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequest(ctx, f.owner, f.camp.ID, f.session.ID, uuid.NewString(), RequestInput{Prompt: "Queued first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginHandoff(ctx, f.owner, f.camp.ID, f.session.ID, f.upload.ID, uuid.NewString(), in); !errors.Is(err, ErrTransition) {
		t.Fatal("hold ignored existing prompt", err)
	}
	current, err := s.Request(ctx, f.owner, f.camp.ID, r.ID)
	if err != nil || current.State != "queued" {
		t.Fatal("hold discarded prompt", err)
	}
}
