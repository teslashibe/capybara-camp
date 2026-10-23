package camp

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func localContinuationFixture(t *testing.T, mode string) (*Store, auth.Actor, auth.Actor, Session, Request) {
	t.Helper()
	f, device, in := localDestinationFixture(t, mode)
	s, ctx := f.git.store, t.Context()
	h, err := s.BeginHandoff(ctx, device, f.camp.ID, f.session.ID, f.upload.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	h, actor, token, ack := acknowledgedHandoff(t, f, h, false)
	commit := HandoffCommitInput{ExpectedHandoffRevision: h.Revision, ContextContinuationApproved: true}
	if mode == "implementation" {
		commit.ExecutionApproved = true
		commit.Permissions = localcli.PermissionSettings{"sandbox": "workspace-write", "approval": "on-request", "reviewer": "user"}
	}
	requested, err := s.RequestHandoffCommit(ctx, device, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), commit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishHandoff(ctx, actor, token, h.ID, HandoffFinishInput{HandoffAck: ack, CommitNonce: requested.CommitNonce}); err != nil {
		t.Fatal(err)
	}
	session, err := s.Session(ctx, device, f.camp.ID, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := s.CreateRequest(ctx, device, session.CampID, session.ID, uuid.NewString(), RequestInput{Prompt: "Continue the original plan"})
	if err != nil || !queued.LocalContinuation {
		t.Fatal("continuation admission", err)
	}
	claimed, err := s.ClaimAvailableRequest(ctx, device, session.CampID, uuid.NewString(), []string{session.ID})
	if err != nil || claimed == nil || claimed.ID != queued.ID {
		t.Fatal("claim", err)
	}
	r, err := s.ActOnRequest(ctx, device, session.CampID, claimed.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claimed.LeaseToken})
	if err != nil {
		t.Fatal(err)
	}
	r.LeaseToken = claimed.LeaseToken
	return s, device, f.owner, session, r
}

func continuationEvent(t *testing.T, s *Store, a auth.Actor, r Request, seq int64, event any) error {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AppendLocalContinuationEvent(t.Context(), a, r.CampID, r.ID, LocalPlanningEvent{LeaseToken: r.LeaseToken, StreamEvent: StreamEvent{Seq: seq, Event: raw}})
	return err
}

func TestLocalContinuationStreamPreservesHistoryAndIdentity(t *testing.T) {
	for _, mode := range []string{"planning", "implementation"} {
		t.Run(mode, func(t *testing.T) {
			s, owner, source, session, r := localContinuationFixture(t, mode)
			ctx := t.Context()
			if _, err := s.ActOnRequest(ctx, owner, r.CampID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "No provider identity"}); !errors.Is(err, ErrTransition) {
				t.Fatal("unbound continuation reported success", err)
			}
			identity := uuid.NewString()
			event := map[string]any{"type": "session", "sessionID": identity}
			if err := continuationEvent(t, s, source, r, 1, event); !errors.Is(err, ErrDenied) {
				t.Fatal("source device wrote", err)
			}
			if err := continuationEvent(t, s, owner, r, 1, event); err != nil {
				t.Fatal(err)
			}
			if err := continuationEvent(t, s, owner, r, 1, event); err != nil {
				t.Fatal("replay", err)
			}
			if err := continuationEvent(t, s, owner, r, 2, map[string]any{"type": "session", "sessionID": uuid.NewString()}); !errors.Is(err, ErrConflict) {
				t.Fatal("rebound identity", err)
			}
			if err := continuationEvent(t, s, owner, r, 3, map[string]any{"type": "done"}); !errors.Is(err, ErrTransition) {
				t.Fatal("gap", err)
			}
			events := []map[string]any{
				{"type": "settings", "model": "actual-model", "effort": "high", "speed": "fast"},
				{"type": "delta", "text": "New answer"},
				{"type": "boundary"},
			}
			for i, e := range events {
				if err := continuationEvent(t, s, owner, r, int64(i+2), e); err != nil {
					t.Fatal(err)
				}
			}
			head, err := s.SessionStream(ctx, owner, r.CampID, r.SessionID)
			if err != nil || head == nil || !head.LocalContinuation || head.Seq != 4 || head.Effort != "high" || head.Speed != "fast" {
				t.Fatal("local stream missing", head, err)
			}
			active, err := s.Session(ctx, owner, r.CampID, r.SessionID)
			if err != nil || active.State != "running" || active.Stage != "In progress" {
				t.Fatal("running state", active, err)
			}
			if _, err = s.WriteTranscript(ctx, owner, session.CampID, session.ID, uuid.NewString(), TranscriptInput{ExpectedRevision: session.TranscriptRevision, Messages: []MessageInput{{ID: "main", Ordinal: 0, Role: "user", Text: "Clobbered"}}}); !errors.Is(err, ErrTransition) {
				t.Fatal("native snapshot replaced imported history", err)
			}
			if _, err = s.PublishSession(ctx, owner, session.CampID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: session.ProjectID, ProfileID: session.ProfileID, LocalSessionID: identity, Title: "Duplicate", State: "idle", Stage: "Ready"}); !errors.Is(err, ErrConflict) {
				t.Fatal("duplicate continuation published", err)
			}
			steerID := uuid.NewString()
			steer := ControlInput{ID: steerID, Control: map[string]any{"type": "steer", "id": steerID, "text": "Keep the original constraints"}}
			if _, err = s.SendControl(ctx, owner, r.CampID, r.ID, steer); err != nil {
				t.Fatal(err)
			}
			if _, err = s.LocalImplementationControls(ctx, source, r.CampID, r.ID, r.LeaseToken); !errors.Is(err, ErrDenied) {
				t.Fatal("source fetched controls", err)
			}
			controls, err := s.LocalImplementationControls(ctx, owner, r.CampID, r.ID, r.LeaseToken)
			if err != nil || len(controls) != 1 || controls[0].ID != steerID {
				t.Fatal("steer delivery", err)
			}
			if err = continuationEvent(t, s, owner, r, 5, map[string]any{"type": "steerAccepted", "requestID": steerID}); err != nil {
				t.Fatal(err)
			}
			if err = continuationEvent(t, s, owner, r, 6, map[string]any{"type": "delta", "text": "Following the original constraints"}); err != nil {
				t.Fatal(err)
			}
			if err = continuationEvent(t, s, owner, r, 7, map[string]any{"type": "done"}); err != nil {
				t.Fatal(err)
			}
			key := uuid.NewString()
			finish := RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Synthetic continuation stream"}
			for range 2 {
				if _, err = s.ActOnRequest(ctx, owner, r.CampID, r.ID, key, finish); err != nil {
					t.Fatal("finish", err)
				}
			}
			transcript, err := s.Transcript(ctx, owner, session.CampID, session.ID, 0, 0, 100)
			if err != nil || len(transcript.Messages) != 5 || transcript.Messages[0].ID != "main" || transcript.Messages[0].Text != "Keep the original plan" {
				t.Fatal("lost or repeated history", transcript, err)
			}
			want := []string{"Continue the original plan", "New answer", "Keep the original constraints", "Following the original constraints"}
			for i, text := range want {
				if transcript.Messages[i+1].Text != text || transcript.Messages[i+1].Ordinal != int64(i+1) {
					t.Fatal(transcript.Messages)
				}
			}
			children, err := s.ChildHistories(ctx, owner, session.CampID, session.ID, 0)
			if err != nil || len(children.Children) != 1 {
				t.Fatal("child history lost", err)
			}
			next, err := s.CreateRequest(ctx, owner, session.CampID, session.ID, uuid.NewString(), RequestInput{Prompt: "Next turn"})
			if err != nil || !next.LocalContinuation || next.ProviderSessionID != identity {
				t.Fatal("follow-up identity lost", next, err)
			}
			current, err := s.Session(ctx, owner, session.CampID, session.ID)
			if err != nil || current.LocalSessionID != session.LocalSessionID || current.ExecutionEpoch != session.ExecutionEpoch {
				t.Fatal("ticket identity changed", err)
			}
		})
	}
}

func TestLocalContinuationStreamAuthorityAndLease(t *testing.T) {
	for _, change := range []string{"lease", "epoch", "permissions", "receipt"} {
		t.Run(change, func(t *testing.T) {
			s, owner, _, session, r := localContinuationFixture(t, "implementation")
			ctx := t.Context()
			var err error
			switch change {
			case "lease":
				_, err = s.pool.Exec(ctx, `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID)
			case "epoch":
				_, err = s.pool.Exec(ctx, `UPDATE sessions SET execution_epoch=execution_epoch+1 WHERE id=$1`, session.ID)
			case "permissions":
				_, err = s.pool.Exec(ctx, `UPDATE sessions SET permission_revision=permission_revision+1 WHERE id=$1`, session.ID)
			case "receipt":
				_, err = s.pool.Exec(ctx, `DELETE FROM transfer_handoffs WHERE session_id=$1`, session.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = continuationEvent(t, s, owner, r, 1, map[string]any{"type": "delta", "text": "Must not persist"}); err == nil {
				t.Fatal("invalid authority accepted")
			}
			if change == "lease" {
				if err = s.ReapRequests(ctx); err != nil {
					t.Fatal(err)
				}
				current, readErr := s.Session(ctx, owner, session.CampID, session.ID)
				if readErr != nil || current.State != "waiting" || current.Stage != "Needs you" {
					t.Fatal("expired continuation looked runnable", current, readErr)
				}
			}
			var count int
			if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM request_stream_events WHERE request_id=$1`, r.ID).Scan(&count); err != nil || count != 0 {
				t.Fatal("rejected event persisted", count, err)
			}
		})
	}
}
