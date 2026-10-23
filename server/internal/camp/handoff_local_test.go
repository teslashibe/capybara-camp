package camp

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func localDestinationFixture(t *testing.T, purpose string, withAttachments ...bool) (transferWorkspaceFixture, auth.Actor, HandoffInput) {
	t.Helper()
	g, w, grant, owner, input := executionFixture(t, "planning")
	var refs []AttachmentReference
	if len(withAttachments) > 0 && withAttachments[0] {
		body := []byte("preserved attachment")
		ref := AttachmentReference{ID: "a:0", Kind: "document", Source: "inline", SourceDigest: strings.Repeat("a", 64), State: "embedded", MediaType: "text/plain", SHA256: transferDigest(body), Size: len(body)}
		if _, err := g.store.PutAttachment(t.Context(), owner, w.CampID, w.SessionID, ref.SHA256, AttachmentUpload{Data: body}); err != nil {
			t.Fatal(err)
		}
		refs = []AttachmentReference{ref, {ID: "a:1", Kind: "image", Source: "remote", SourceDigest: strings.Repeat("b", 64), State: "reference"}}
	}
	revision, err := g.store.WriteTranscript(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "main", Ordinal: 0, Role: "user", Text: "Keep the original plan", Attachments: refs}}})
	if err != nil {
		t.Fatal(err)
	}
	child := childInput(revision)
	child.Complete = true
	child.Messages = []MessageInput{{ID: "review", Ordinal: 0, Role: "assistant", Text: "Preserve this review", Model: "recorded-model", Effort: "high", Speed: "fast"}}
	history, err := g.store.WriteChildHistory(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), uuid.NewString(), child)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedTranscriptRevision = history.Revision
	if _, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	session, err := g.store.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	device := owner
	device.DeviceID = uuid.NewString()
	if _, err = g.store.pool.Exec(t.Context(), `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Destination Mac',$3,now()+interval '1 day')`, device.DeviceID, device.UserID, auth.HashToken(device.DeviceID)); err != nil {
		t.Fatal(err)
	}
	profile, err := g.store.RegisterProfile(t.Context(), device, uuid.NewString(), ProfileInput{LocalID: strings.Repeat("e", 64), Provider: session.Provider, Label: "Destination profile"})
	if err != nil {
		t.Fatal(err)
	}
	in, raw := transferPayload(t, session, []byte("reviewed file"))
	upload, err := g.store.CreateTransfer(t.Context(), device, w.CampID, w.SessionID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	upload = uploadTransfer(t, g.store, device, Camp{ID: w.CampID}, session, upload, raw)
	upload, err = g.store.CompleteTransfer(t.Context(), device, w.CampID, w.SessionID, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	f := transferWorkspaceFixture{git: g, owner: owner, camp: Camp{ID: w.CampID}, session: session, source: w, upload: upload, grant: grant, raw: raw}
	return f, device, HandoffInput{ExpectedTransferRevision: upload.Revision, LocalDestination: &LocalHandoffDestination{ProfileID: profile.ID, SessionID: uuid.NewString(), Purpose: purpose, ReceiptSHA256: strings.Repeat("b", 64), TranscriptRevision: session.TranscriptRevision}}
}

func TestLocalDestinationHandoffFreshProofPreservesTicketAndFencesSource(t *testing.T) {
	for _, purpose := range []string{"planning", "implementation"} {
		t.Run(purpose, func(t *testing.T) {
			f, device, in := localDestinationFixture(t, purpose)
			s := f.git.store
			ctx := t.Context()
			key := uuid.NewString()
			h, err := s.BeginHandoff(ctx, device, f.camp.ID, f.session.ID, f.upload.ID, key, in)
			if err != nil {
				t.Fatal(err)
			}
			if h.TargetLocation != "local" || h.WorkspaceID != "" || h.TargetDeviceID != device.DeviceID || h.LocalDestination == nil || *h.LocalDestination != *in.LocalDestination {
				t.Fatal("Missing prepared destination")
			}
			replay, err := s.BeginHandoff(ctx, device, f.camp.ID, f.session.ID, f.upload.ID, key, in)
			if err != nil || replay.ID != h.ID {
				t.Fatal("begin replay", err)
			}
			if _, err = s.CreateRequest(ctx, f.owner, f.camp.ID, f.session.ID, uuid.NewString(), RequestInput{Prompt: "wait"}); !errors.Is(err, ErrTransferHeld) {
				t.Fatal("source accepted a turn", err)
			}
			next, err := s.NextRunnerHandoff(ctx, f.grant.Token)
			if err != nil || next != h.ID {
				t.Fatal("runner did not discover local destination", err)
			}
			h, a, token, ack := acknowledgedHandoff(t, f, h, false)
			commit := HandoffCommitInput{ExpectedHandoffRevision: h.Revision}
			if _, err = s.RequestHandoffCommit(ctx, device, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), commit); !errors.Is(err, ErrDenied) {
				t.Fatal("implicit context continuation", err)
			}
			commit.ContextContinuationApproved = true
			if _, err = s.RequestHandoffCommit(ctx, f.owner, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), commit); !errors.Is(err, ErrDenied) {
				t.Fatal("other Mac confirmed destination", err)
			}
			if purpose == "implementation" {
				if _, err = s.RequestHandoffCommit(ctx, device, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), commit); !errors.Is(err, ErrDenied) {
					t.Fatal("implicit implementation permission", err)
				}
				commit.ExecutionApproved = true
				commit.Permissions = localcli.PermissionSettings{"sandbox": "workspace-write", "approval": "on-request", "reviewer": "user"}
			}
			requested, err := s.RequestHandoffCommit(ctx, device, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), commit)
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.Session(ctx, device, f.camp.ID, f.session.ID)
			if err != nil || before.ExecutionLocation != "cloud" || before.OwnerDeviceID != f.owner.DeviceID {
				t.Fatal("confirmation prematurely moved source", err)
			}
			proof := HandoffFinishInput{HandoffAck: ack, CommitNonce: requested.CommitNonce}
			var wg sync.WaitGroup
			failures := make(chan error, 4)
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					v, e := s.FinishHandoff(ctx, a, token, h.ID, proof)
					if e == nil && (v.ExecutionEpoch != f.session.ExecutionEpoch+1 || v.State != "committed") {
						e = ErrInput
					}
					failures <- e
				}()
			}
			wg.Wait()
			close(failures)
			for e := range failures {
				if e != nil {
					t.Fatal(e)
				}
			}
			current, err := s.Session(ctx, device, f.camp.ID, f.session.ID)
			if err != nil || current.ExecutionLocation != "local" || current.ExecutionWorkspaceID != "" || current.OwnerDeviceID != device.DeviceID || current.ProfileID != in.LocalDestination.ProfileID || current.LocalSessionID != in.LocalDestination.SessionID || current.TranscriptRevision != f.session.TranscriptRevision || current.ExecutionEpoch != f.session.ExecutionEpoch+1 || current.PermissionRevision != f.session.PermissionRevision+1 {
				t.Fatal("move replaced history or destination", err)
			}
			main, err := s.Transcript(ctx, device, f.camp.ID, f.session.ID, 0, 0, 100)
			if err != nil || len(main.Messages) != 1 || main.Messages[0].ID != "main" || main.Messages[0].Text != "Keep the original plan" {
				t.Fatal("main history changed", err)
			}
			children, err := s.ChildHistories(ctx, device, f.camp.ID, f.session.ID, 0)
			if err != nil || len(children.Children) != 1 || children.Children[0].SourceKey != childInput(0).SourceKey {
				t.Fatal("child identity changed", err)
			}
			child, err := s.ChildTranscript(ctx, device, f.camp.ID, f.session.ID, children.Children[0].ID, 0, 0, 100)
			if err != nil || len(child.Transcript.Messages) != 1 || child.Transcript.Messages[0].Text != "Preserve this review" || child.Transcript.Messages[0].Effort != "high" || child.Transcript.Messages[0].Speed != "fast" {
				t.Fatal("child history or recorded settings changed", err)
			}
			target, err := s.Execution(ctx, device, f.camp.ID, f.session.ID)
			if err != nil || !target.Available || target.Location != "local" {
				t.Fatal("local destination unavailable", err)
			}
			if purpose == "implementation" && (target.Mode != "implementation" || target.Permissions["approval"] != "on-request") {
				t.Fatal("local permissions lost")
			}
			old, err := f.git.Workspace(ctx, device, f.camp.ID, f.source.ID)
			if err != nil || old.State != "cancelled" {
				t.Fatal("source remained runnable", err)
			}
			request, err := s.CreateRequest(ctx, device, f.camp.ID, f.session.ID, uuid.NewString(), RequestInput{Prompt: "Continue locally"})
			if err != nil {
				t.Fatal(err)
			}
			if request.OwnerDeviceID != device.DeviceID || request.ExecutionLocation != "local" {
				t.Fatal("next turn kept old owner")
			}
			if purpose == "implementation" && (request.Mode != "implementation" || request.Permissions["sandbox"] != "workspace-write" || request.Permissions["approval"] != "on-request") {
				t.Fatal("next turn lost approved permissions")
			}
			if claimed, e := s.ClaimAvailableWork(ctx, f.owner, f.camp.ID, uuid.NewString(), []string{f.session.ID}); e != nil || claimed != nil {
				t.Fatal("old Mac stole work", e)
			}
			if claimed, e := s.ClaimAvailableWork(ctx, device, f.camp.ID, uuid.NewString(), []string{f.session.ID}); e != nil || claimed == nil || claimed.ID != request.ID {
				t.Fatal("destination could not claim", e)
			}
		})
	}
}

func TestLocalDestinationHandoffRejectsChangedOrForeignPreparation(t *testing.T) {
	for _, change := range []string{"receipt", "history", "profile", "device", "existing", "provider", "revoked", "expired", "source"} {
		t.Run(change, func(t *testing.T) {
			f, device, in := localDestinationFixture(t, "planning")
			s := f.git.store
			ctx := t.Context()
			switch change {
			case "receipt":
				in.LocalDestination.ReceiptSHA256 = "bad"
			case "history":
				in.LocalDestination.TranscriptRevision++
			case "profile":
				in.LocalDestination.ProfileID = f.session.ProfileID
			case "device":
				device = f.owner
			case "existing":
				_, err := s.PublishSession(ctx, device, f.camp.ID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: f.session.ProjectID, ProfileID: in.LocalDestination.ProfileID, LocalSessionID: in.LocalDestination.SessionID, Title: "Existing local session", State: "idle", Stage: "Ready"})
				if err != nil {
					t.Fatal(err)
				}
			case "provider":
				if _, err := s.pool.Exec(ctx, `UPDATE profiles SET provider='Claude' WHERE id=$1`, in.LocalDestination.ProfileID); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if _, err := s.pool.Exec(ctx, `UPDATE devices SET revoked_at=now() WHERE id=$1`, device.DeviceID); err != nil {
					t.Fatal(err)
				}
			case "expired":
				if _, err := s.pool.Exec(ctx, `UPDATE devices SET expires_at=now()-interval '1 second' WHERE id=$1`, device.DeviceID); err != nil {
					t.Fatal(err)
				}
			case "source":
				if _, err := s.pool.Exec(ctx, `UPDATE sessions SET transcript_revision=transcript_revision+1 WHERE id=$1`, f.session.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.BeginHandoff(ctx, device, f.camp.ID, f.session.ID, f.upload.ID, uuid.NewString(), in); err == nil {
				t.Fatal("accepted invalid destination")
			}
		})
	}
}

func TestLocalDestinationRevocationAfterConfirmationDoesNotMove(t *testing.T) {
	f, device, in := localDestinationFixture(t, "planning")
	s := f.git.store
	ctx := t.Context()
	h, err := s.BeginHandoff(ctx, device, f.camp.ID, f.session.ID, f.upload.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	h, a, token, ack := acknowledgedHandoff(t, f, h, false)
	requested, err := s.RequestHandoffCommit(ctx, device, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), HandoffCommitInput{ExpectedHandoffRevision: h.Revision, ContextContinuationApproved: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE devices SET revoked_at=now() WHERE id=$1`, device.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishHandoff(ctx, a, token, h.ID, HandoffFinishInput{HandoffAck: ack, CommitNonce: requested.CommitNonce}); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked destination gained execution", err)
	}
	current, err := s.Session(ctx, f.owner, f.camp.ID, f.session.ID)
	if err != nil || current.ExecutionLocation != "cloud" || current.ExecutionEpoch != f.session.ExecutionEpoch {
		t.Fatal("failed move changed ownership", err)
	}
	refreshed, err := s.Handoff(ctx, f.owner, f.camp.ID, f.session.ID, h.ID)
	if err != nil || refreshed.State != "invalidated" {
		t.Fatal("revoked destination kept hold", err)
	}
}

func TestLocalDestinationCollisionInAnotherCampInvalidatesHold(t *testing.T) {
	f, device, in := localDestinationFixture(t, "planning")
	s, ctx := f.git.store, t.Context()
	h, err := s.BeginHandoff(ctx, device, f.camp.ID, f.session.ID, f.upload.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	h, actor, token, ack := acknowledgedHandoff(t, f, h, false)
	requested, err := s.RequestHandoffCommit(ctx, device, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), HandoffCommitInput{ExpectedHandoffRevision: h.Revision, ContextContinuationApproved: true})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(ctx, device, uuid.NewString(), "Other Camp")
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(ctx, device, other.ID, uuid.NewString(), "Other project")
	if err != nil {
		t.Fatal(err)
	}
	existing, err := s.PublishSession(ctx, device, other.ID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: project.ID, ProfileID: in.LocalDestination.ProfileID, LocalSessionID: in.LocalDestination.SessionID, Title: "Imported while reviewing", State: "idle", Stage: "Ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishHandoff(ctx, actor, token, h.ID, HandoffFinishInput{HandoffAck: ack, CommitNonce: requested.CommitNonce}); !errors.Is(err, ErrConflict) {
		t.Fatal("existing conversation replaced", err)
	}
	refreshed, err := s.Handoff(ctx, f.owner, f.camp.ID, f.session.ID, h.ID)
	if err != nil || refreshed.State != "invalidated" {
		t.Fatal("occupied destination kept source held", err)
	}
	current, err := s.Session(ctx, f.owner, f.camp.ID, f.session.ID)
	if err != nil || current.ExecutionEpoch != f.session.ExecutionEpoch || current.ExecutionLocation != "cloud" {
		t.Fatal("collision changed source owner", err)
	}
	if preserved, err := s.Session(ctx, device, other.ID, existing.ID); err != nil || preserved.LocalSessionID != existing.LocalSessionID {
		t.Fatal("collision damaged other Camp session", err)
	}
}
