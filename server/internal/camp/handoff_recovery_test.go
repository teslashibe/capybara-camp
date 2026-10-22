package camp

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func TestHandoffRecoveryIsOwnerScopedAndDoesNotMoveExecution(t *testing.T) {
	f, h := handoffFixture(t, true)
	s := f.git.store
	h, _, _, _ = acknowledgedHandoff(t, f, h, true)
	again := f.owner
	again.DeviceID = uuid.NewString()
	rows, err := s.RecoverHandoffs(t.Context(), again, f.camp.ID, []string{h.ID, uuid.NewString()})
	if err != nil || len(rows) != 1 || rows[0].Handoff.ClaimID != h.ClaimID || rows[0].Session.ExecutionLocation != "local" {
		t.Fatal("Owner did not recover the saved handoff", err)
	}
	var member string
	if err = s.pool.QueryRow(t.Context(), `SELECT user_id FROM memberships WHERE camp_id=$1 AND user_id<>$2 LIMIT 1`, f.camp.ID, f.owner.UserID).Scan(&member); err != nil {
		t.Fatal(err)
	}
	rows, err = s.RecoverHandoffs(t.Context(), auth.Actor{UserID: member, DeviceID: uuid.NewString()}, f.camp.ID, []string{h.ID})
	if err != nil || len(rows) != 0 {
		t.Fatal("Teammate read private handoff", err)
	}
	for _, ids := range [][]string{nil, {h.ID, h.ID}, {"invalid"}} {
		if _, err = s.RecoverHandoffs(t.Context(), again, f.camp.ID, ids); !errors.Is(err, ErrInput) {
			t.Fatal("Invalid recovery request accepted", err)
		}
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE transfer_handoffs SET expires_at=now()-interval '1 second' WHERE id=$1`, h.ID); err != nil {
		t.Fatal(err)
	}
	rows, err = s.RecoverHandoffs(t.Context(), again, f.camp.ID, []string{h.ID})
	if err != nil || len(rows) != 1 || rows[0].Handoff.State != "expired" || rows[0].Session.ExecutionEpoch != f.session.ExecutionEpoch {
		t.Fatal("Expired hold was not reconciled without moving execution", err)
	}
}

func TestHandoffRecoveryReturnsCommittedAssignmentAfterReauthentication(t *testing.T) {
	f, h := handoffFixture(t, true)
	s := f.git.store
	h, actor, token, ack := acknowledgedHandoff(t, f, h, true)
	requested, err := s.RequestHandoffCommit(t.Context(), f.owner, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), HandoffCommitInput{ExpectedHandoffRevision: h.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishHandoff(t.Context(), actor, token, h.ID, HandoffFinishInput{HandoffAck: ack, CommitNonce: requested.CommitNonce}); err != nil {
		t.Fatal(err)
	}
	again := f.owner
	again.DeviceID = uuid.NewString()
	rows, err := s.RecoverHandoffs(t.Context(), again, f.camp.ID, []string{h.ID})
	if err != nil || len(rows) != 1 || rows[0].Handoff.State != "committed" || rows[0].Session.ExecutionWorkspaceID != f.target.ID || rows[0].Session.ExecutionEpoch != f.session.ExecutionEpoch+1 {
		t.Fatal("Committed assignment was not recovered", err)
	}
	if _, err = s.ClaimHandoff(t.Context(), again, "", h.ID, h.ClaimID); !errors.Is(err, ErrDenied) {
		t.Fatal("Recovery granted source control to another device", err)
	}
	if _, err = s.pool.Exec(t.Context(), `DELETE FROM memberships WHERE camp_id=$1 AND user_id=$2`, f.camp.ID, again.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecoverHandoffs(t.Context(), again, f.camp.ID, []string{h.ID}); !errors.Is(err, ErrDenied) {
		t.Fatal("Revoked membership recovered private state", err)
	}
}
