package camp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

type HandoffCommitInput struct {
	ExpectedHandoffRevision     int64                       `json:"expected_handoff_revision"`
	ExecutionApproved           bool                        `json:"execution_approved"`
	Permissions                 localcli.PermissionSettings `json:"permissions"`
	ContextContinuationApproved bool                        `json:"context_continuation_approved,omitempty"`
	AttachmentReviewSHA256      string                      `json:"attachment_review_sha256,omitempty"`
}
type HandoffFinishInput struct {
	HandoffAck
	CommitNonce string `json:"commit_nonce"`
}

// RequestHandoffCommit records the owner's exact destination permissions. It
// does not move execution: the source must answer this fresh challenge first.
func (s *Store) RequestHandoffCommit(ctx context.Context, a auth.Actor, campID, sessionID, id, key string, in HandoffCommitInput) (Handoff, error) {
	if in.ExpectedHandoffRevision < 1 {
		return Handoff{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Handoff{}, err
	}
	defer tx.Rollback(ctx)
	session, err := s.transferOwner(ctx, tx, a, campID, sessionID, true)
	if err != nil {
		return Handoff{}, err
	}
	h, err := readHandoff(ctx, tx, id, a.UserID)
	if err != nil {
		return h, err
	}
	if h.CampID != campID || h.SessionID != sessionID {
		return h, ErrDenied
	}
	if hit, e := s.beginCommand(ctx, tx, a, key, "handoff.commit:"+id, in, &h); e != nil {
		return h, e
	} else if hit {
		h, err = readHandoff(ctx, tx, id, a.UserID)
		if err != nil {
			return h, err
		}
		return h, tx.Commit(ctx)
	}
	if h.State != "acknowledged" || h.CommitNonce != "" {
		return h, ErrTransition
	}
	if h.TargetLocation == "local" && (a.DeviceID != h.TargetDeviceID || !in.ContextContinuationApproved) {
		return h, ErrDenied
	}
	if h.TargetLocation != "local" && in.ContextContinuationApproved {
		return h, ErrInput
	}
	if h.Revision != in.ExpectedHandoffRevision {
		return h, ErrRevision
	}
	if _, err = s.validateHandoff(ctx, tx, a, h); err != nil {
		return h, err
	}
	review, err := transferAttachmentReview(ctx, tx, session.ID, session.TranscriptRevision, true)
	if err != nil {
		return h, err
	}
	if review == nil && in.AttachmentReviewSHA256 != "" || review != nil && in.AttachmentReviewSHA256 != review.SHA256 {
		return h, ErrRevision
	}
	purpose, err := s.handoffPurpose(ctx, tx, a, h)
	if err != nil {
		return h, err
	}
	permissions, err := workspacePermissions(session.Provider, purpose, in.Permissions, in.ExecutionApproved)
	if err != nil {
		return h, err
	}
	raw, _ := json.Marshal(permissions)
	if _, err = tx.Exec(ctx, `UPDATE transfer_handoffs SET commit_nonce=$2,commit_permissions=$3,commit_approved=$4,commit_requested_at=now(),revision=revision+1 WHERE id=$1`, id, uuid.NewString(), raw, in.ExecutionApproved); err != nil {
		return h, err
	}
	h, err = readHandoff(ctx, tx, id, a.UserID)
	if err != nil {
		return h, err
	}
	if err = s.emitHandoff(ctx, tx, a, h); err != nil {
		return h, err
	}
	if err = s.finishCommand(ctx, tx, a, key, h); err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

// FinishHandoff atomically records the fresh source proof and changes the
// execution epoch. Replaying a receipt only observes the original move.
func (s *Store) FinishHandoff(ctx context.Context, a auth.Actor, token, id string, in HandoffFinishInput) (Handoff, error) {
	if !validID(id) || !validID(in.ClaimID) || !validID(in.CommitNonce) || !in.Stopped {
		return Handoff{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Handoff{}, err
	}
	defer tx.Rollback(ctx)
	h, t, grant, err := s.sourceHandoff(ctx, tx, a, token, id)
	if err != nil {
		return h, err
	}
	device := a.DeviceID
	if token != "" {
		device = ""
	}
	if h.ClaimID != in.ClaimID || h.claimDevice != device || h.claimGrant != grant {
		return h, ErrDenied
	}
	if h.CommitNonce != in.CommitNonce || in.PayloadSHA256 != t.PayloadSHA256 || in.ReviewSHA256 != t.ReviewSHA256 {
		return h, ErrConflict
	}
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(raw)
	if h.State == "committed" {
		if subtle.ConstantTimeCompare(h.commitHash, digest[:]) != 1 {
			return h, ErrConflict
		}
		return h, tx.Commit(ctx)
	}
	if h.State != "acknowledged" {
		return h, ErrTransition
	}
	owner := auth.Actor{UserID: h.OwnerUserID}
	session, err := s.readSession(ctx, tx, owner, h.CampID, h.SessionID)
	if err != nil {
		return h, err
	}
	purpose, err := s.handoffPurpose(ctx, tx, owner, h)
	if err != nil {
		return h, err
	}
	permissions, err := workspacePermissions(session.Provider, purpose, h.commitPermissions, h.commitApproved)
	if err != nil {
		return h, err
	}
	if session.ExecutionLocation == "cloud" {
		old, err := readWorkspace(ctx, tx, owner, h.CampID, session.ExecutionWorkspaceID)
		if err != nil {
			return h, err
		}
		if old.ID == h.WorkspaceID {
			return h, ErrTransition
		}
		if err = retireWorkspaceToken(ctx, tx, old.ID); err != nil {
			return h, err
		}
		if err = tx.QueryRow(ctx, `UPDATE ticket_workspaces SET state='cancelled',revision=revision+1,updated_at=now() WHERE id=$1 RETURNING revision`, old.ID).Scan(&old.Revision); err != nil {
			return h, err
		}
		old.State = "cancelled"
		if err = s.emitWorkspace(ctx, tx, owner, old); err != nil {
			return h, err
		}
	}
	encoded, _ := json.Marshal(permissions)
	if h.TargetLocation == "local" {
		// Local permissions remain in the immutable approved handoff receipt.
		// The session column continues to describe cloud workspace permissions.
		encoded = []byte("{}")
		d := h.LocalDestination
		if d == nil {
			return h, ErrInput
		}
		err = tx.QueryRow(ctx, `UPDATE sessions SET execution_location='local',execution_workspace_id=NULL,execution_permissions=$2,
 profile_id=$3,owner_device_id=$4,local_session_id=$5,revision=revision+1,permission_revision=permission_revision+1,
 execution_epoch=execution_epoch+1,activity_revision=activity_revision+1,updated_at=now()
 WHERE id=$1 RETURNING execution_epoch,activity_revision,revision,permission_revision`, session.ID, encoded, d.ProfileID, h.TargetDeviceID, d.SessionID).Scan(&session.ExecutionEpoch, &session.ActivityRevision, &session.Revision, &session.PermissionRevision)
		session.ProfileID, session.OwnerDeviceID, session.LocalSessionID = d.ProfileID, h.TargetDeviceID, d.SessionID
	} else {
		err = tx.QueryRow(ctx, `UPDATE sessions SET execution_location='cloud',execution_workspace_id=$2,execution_permissions=$3,execution_epoch=execution_epoch+1,activity_revision=activity_revision+1,updated_at=now() WHERE id=$1 RETURNING execution_epoch,activity_revision`, session.ID, h.WorkspaceID, encoded).Scan(&session.ExecutionEpoch, &session.ActivityRevision)
	}
	if err != nil {
		return h, err
	}
	session.ExecutionLocation, session.ExecutionWorkspaceID = h.TargetLocation, h.WorkspaceID
	if _, err = tx.Exec(ctx, `UPDATE transfer_handoffs SET state='committed',commit_hash=$2,execution_epoch=$3,revision=revision+1 WHERE id=$1`, h.ID, digest[:], session.ExecutionEpoch); err != nil {
		return h, err
	}
	if _, err = tx.Exec(ctx, `UPDATE session_transfers SET state='completed',uploaded_bytes=0,revision=revision+1,updated_at=now() WHERE id=$1`, t.ID); err != nil {
		return h, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM transfer_chunks WHERE transfer_id=$1`, t.ID); err != nil {
		return h, err
	}
	if _, err = s.executionTarget(ctx, tx, session); err != nil {
		return h, err
	}
	h, err = readHandoff(ctx, tx, id, h.OwnerUserID)
	if err != nil {
		return h, err
	}
	t, err = readTransfer(ctx, tx, owner, h.CampID, h.SessionID, t.ID)
	if err != nil {
		return h, err
	}
	if err = s.emitTransfer(ctx, tx, owner, t); err != nil {
		return h, err
	}
	if err = emitSession(ctx, tx, owner, h.CampID, "session.execution", session, false); err != nil {
		return h, err
	}
	if err = s.emitHandoff(ctx, tx, owner, h); err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}
