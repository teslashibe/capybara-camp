package camp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

type ExecutionTarget struct {
	Location         string                      `json:"location"`
	Epoch            int64                       `json:"epoch"`
	WorkspaceID      string                      `json:"workspace_id,omitempty"`
	Mode             string                      `json:"mode"`
	Permissions      localcli.PermissionSettings `json:"permissions"`
	Available        bool                        `json:"available"`
	RequiresTransfer bool                        `json:"requires_transfer,omitempty"`
}

type ExecutionInput struct {
	Location                   string                      `json:"location"`
	WorkspaceID                string                      `json:"workspace_id,omitempty"`
	ExpectedEpoch              int64                       `json:"expected_epoch"`
	ExpectedSessionRevision    int64                       `json:"expected_session_revision"`
	ExpectedTranscriptRevision int64                       `json:"expected_transcript_revision"`
	ExpectedWorkspaceRevision  int64                       `json:"expected_workspace_revision"`
	ExecutionApproved          bool                        `json:"execution_approved"`
	Permissions                localcli.PermissionSettings `json:"permissions,omitempty"`
}

// The session's execution epoch is independent from its display revision.
// Moving work does not change the approved repository/history snapshot.
func (s *Store) executionTarget(ctx context.Context, tx pgx.Tx, session Session) (ExecutionTarget, error) {
	if session.HistoryOnly {
		return ExecutionTarget{}, ErrTransition
	}
	out := ExecutionTarget{Location: session.ExecutionLocation, Epoch: session.ExecutionEpoch, WorkspaceID: session.ExecutionWorkspaceID, Mode: "chat", Permissions: localcli.PermissionSettings{}, Available: true}
	if out.Location == "local" {
		var raw []byte
		var purpose, profile, device, localID string
		err := tx.QueryRow(ctx, `SELECT h.commit_permissions,h.local_purpose,h.local_profile_id,t.target_device_id,h.local_session_id
 FROM sessions s JOIN transfer_handoffs h ON h.session_id=s.id AND h.execution_epoch=s.execution_epoch
 JOIN session_transfers t ON t.id=h.transfer_id
 WHERE s.id=$1 AND h.state='committed' AND t.target_location='local' AND h.owner_user_id=s.owner_user_id`, session.ID).Scan(&raw, &purpose, &profile, &device, &localID)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if profile != session.ProfileID || device != session.OwnerDeviceID || localID != session.LocalSessionID {
			out.Available = false
			return out, ErrTransition
		}
		if json.Unmarshal(raw, &out.Permissions) != nil {
			return out, ErrInput
		}
		if purpose == "implementation" {
			out.Mode = "implementation"
		}
		return out, nil
	}
	var raw []byte
	var purpose string
	err := tx.QueryRow(ctx, `SELECT s.execution_permissions,w.purpose FROM sessions s
 JOIN ticket_workspaces w ON w.id=s.execution_workspace_id
 JOIN provider_installations p ON p.id=w.provider_installation_id
 JOIN git_connections g ON g.id=w.git_connection_id
 JOIN projects j ON j.id=w.project_id
 WHERE s.id=$1 AND w.camp_id=s.camp_id AND w.session_id=s.id AND w.owner_user_id=s.owner_user_id AND w.project_id=s.project_id
 AND w.state='ready' AND w.source_revision=s.revision AND w.project_revision=j.revision AND NOT j.archived
 AND p.epoch=w.provider_epoch AND p.state='connected' AND (p.credential_expires_at IS NULL OR p.credential_expires_at>clock_timestamp())
 AND g.epoch=w.git_epoch AND g.state='connected'`, session.ID).Scan(&raw, &purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		out.Available = false
		return out, ErrTransition
	}
	if err != nil {
		return out, err
	}
	if json.Unmarshal(raw, &out.Permissions) != nil {
		return out, ErrInput
	}
	if purpose == "implementation" {
		out.Mode = "implementation"
	}
	return out, nil
}

func (s *Store) cloudActivity(ctx context.Context, tx pgx.Tx, r Request) error {
	if r.ExecutionLocation != "cloud" || r.Kind == "implementation" {
		return nil
	}
	state, stage := "", ""
	switch r.State {
	case "started", "cancel_requested":
		state, stage = "running", "In progress"
	case "completed":
		state, stage = "done", "Done"
	case "failed", "outcome_unknown":
		state, stage = "waiting", "Needs you"
	case "cancelled", "queued":
		state, stage = "idle", "Ready"
	case "pending_approval":
		state, stage = "waiting", "Needs you"
	default:
		return nil
	}
	var running, unknown, approval, queued bool
	err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM prompt_requests WHERE session_id=$1 AND state IN ('started','cancel_requested')),
 EXISTS(SELECT 1 FROM prompt_requests WHERE session_id=$1 AND state='outcome_unknown'),
 EXISTS(SELECT 1 FROM prompt_requests WHERE session_id=$1 AND state='pending_approval'),
 EXISTS(SELECT 1 FROM prompt_requests WHERE session_id=$1 AND state IN ('queued','claimed'))`, r.SessionID).Scan(&running, &unknown, &approval, &queued)
	if err != nil {
		return err
	}
	var providerApproval bool
	if running {
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM request_stream_events e JOIN prompt_requests p ON p.id=e.request_id WHERE p.session_id=$1 AND p.state IN ('started','cancel_requested') AND e.payload->>'type'='approval' AND NOT EXISTS(SELECT 1 FROM request_stream_events resolved WHERE resolved.request_id=e.request_id AND resolved.seq>e.seq AND resolved.payload->>'type'='approvalResolved' AND resolved.payload->>'requestID'=e.payload->>'id'))`, r.SessionID).Scan(&providerApproval); err != nil {
			return err
		}
	}
	switch {
	case running && !providerApproval:
		state, stage = "running", "In progress"
	case providerApproval || unknown || approval:
		state, stage = "waiting", "Needs you"
	case queued:
		state, stage = "idle", "Ready"
	}
	session := Session{ID: r.SessionID}
	err = tx.QueryRow(ctx, `UPDATE sessions SET state=$3,stage=$4,activity_revision=activity_revision+1,updated_at=now() WHERE id=$1 AND camp_id=$2 RETURNING revision,transcript_revision,activity_revision`, r.SessionID, r.CampID, state, stage).Scan(&session.Revision, &session.TranscriptRevision, &session.ActivityRevision)
	if err != nil {
		return err
	}
	return emitSession(ctx, tx, auth.Actor{UserID: r.OwnerUserID}, r.CampID, "session.activity", session, false)
}

func (s *Store) Execution(ctx context.Context, a auth.Actor, campID, id string) (ExecutionTarget, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ExecutionTarget{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return ExecutionTarget{}, err
	}
	session, err := s.readSession(ctx, tx, a, campID, id)
	if err != nil {
		return ExecutionTarget{}, err
	}
	if session.OwnerUserID != a.UserID {
		return ExecutionTarget{}, ErrDenied
	}
	if session.HistoryOnly {
		return ExecutionTarget{}, ErrTransition
	}
	out, err := s.executionTarget(ctx, tx, session)
	if err == nil || errors.Is(err, ErrTransition) {
		required, failure := requiresExecutionTransfer(ctx, tx, session)
		if failure != nil {
			return out, failure
		}
		out.RequiresTransfer = required
	}
	if errors.Is(err, ErrTransition) {
		if out.Location == "local" {
			return out, nil
		}
		var raw []byte
		var purpose string
		if err = tx.QueryRow(ctx, `SELECT s.execution_permissions,w.purpose FROM sessions s JOIN ticket_workspaces w ON w.id=s.execution_workspace_id WHERE s.id=$1`, session.ID).Scan(&raw, &purpose); err != nil {
			return out, err
		}
		if json.Unmarshal(raw, &out.Permissions) != nil {
			return out, ErrInput
		}
		if purpose == "implementation" {
			out.Mode = "implementation"
		}
		return out, nil
	}
	return out, err
}

func requiresExecutionTransfer(ctx context.Context, tx pgx.Tx, session Session) (bool, error) {
	if session.ExecutionLocation != "cloud" {
		return false, nil
	}
	var required bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_workspaces WHERE id=$2 AND transfer_id IS NOT NULL) OR EXISTS(SELECT 1 FROM prompt_requests r JOIN ticket_workspaces w ON w.id=r.execution_workspace_id WHERE r.session_id=$1 AND r.execution_workspace_id=$2 AND ((w.purpose='implementation' AND r.started_at IS NOT NULL) OR (r.stream_published AND r.stream_seq>0)))`, session.ID, session.ExecutionWorkspaceID).Scan(&required)
	return required, err
}

func workspacePermissions(provider, purpose string, input localcli.PermissionSettings, approved bool) (localcli.PermissionSettings, error) {
	permissions, err := localcli.NormalizePermissions(provider, input)
	if err != nil {
		return nil, ErrInput
	}
	if purpose == "planning" {
		if len(permissions) > 0 || approved {
			return nil, ErrInput
		}
		return permissions, nil
	}
	if purpose != "implementation" {
		return nil, ErrInput
	}
	if !approved {
		return nil, ErrDenied
	}
	if provider == "Codex" && ((permissions["profile"] == "" && permissions["sandbox"] == "") || permissions["approval"] == "" || permissions["reviewer"] == "") {
		return nil, ErrInput
	}
	if provider == "Claude" && permissions["claudeMode"] == "" {
		return nil, ErrInput
	}
	return permissions, nil
}

// AssignExecution only changes ownership of future work. The caller must
// review the prepared workspace and history. Active or queued requests block
// assignment changes; they cannot be silently rerouted to a different account.
func (s *Store) AssignExecution(ctx context.Context, a auth.Actor, campID, id, key string, in ExecutionInput) (ExecutionTarget, error) {
	if !oneOf(in.Location, "local", "cloud") || in.ExpectedEpoch < 1 || in.ExpectedSessionRevision < 1 || in.ExpectedTranscriptRevision < 0 {
		return ExecutionTarget{}, ErrInput
	}
	if in.Location == "local" && (in.WorkspaceID != "" || len(in.Permissions) > 0 || in.ExecutionApproved || in.ExpectedWorkspaceRevision != 0) {
		return ExecutionTarget{}, ErrInput
	}
	if in.Location == "cloud" && (!validID(in.WorkspaceID) || in.ExpectedWorkspaceRevision < 1) {
		return ExecutionTarget{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ExecutionTarget{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return ExecutionTarget{}, err
	}
	session, err := s.readSession(ctx, tx, a, campID, id)
	if err != nil {
		return ExecutionTarget{}, err
	}
	if session.OwnerUserID != a.UserID {
		return ExecutionTarget{}, ErrDenied
	}
	var out ExecutionTarget
	if hit, err := s.beginCommand(ctx, tx, a, key, "execution.assign:"+id, in, &out); err != nil {
		return out, err
	} else if hit {
		// A replay is observational and must never move a newer assignment back.
		return s.executionTarget(ctx, tx, session)
	}
	if session.ExecutionEpoch != in.ExpectedEpoch || session.Revision != in.ExpectedSessionRevision || session.TranscriptRevision != in.ExpectedTranscriptRevision {
		return out, ErrRevision
	}
	if err = requireUnheldSession(ctx, tx, id); err != nil {
		return out, err
	}
	if session.HistoryOnly || session.ExternalOwner || !(oneOf(session.State, "idle", "done") || session.ExecutionLocation == "cloud" && session.State == "waiting") {
		return out, ErrTransition
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM prompt_requests WHERE session_id=$1 AND state NOT IN ('completed','failed','cancelled'))`, id).Scan(&busy); err != nil {
		return out, err
	}
	if busy {
		return out, ErrTransition
	}
	if session.ExecutionLocation == "cloud" && (in.Location == "local" || in.WorkspaceID != session.ExecutionWorkspaceID) {
		needsTransfer, transferErr := requiresExecutionTransfer(ctx, tx, session)
		if transferErr != nil {
			return out, transferErr
		}
		// A destination change must not strand edits from an implementation.
		// The reviewed transfer protocol will supply this release condition.
		if needsTransfer {
			return out, ErrTransition
		}
		// No cloud history or implementation needs retaining. Retire the old
		// preparation slot atomically with the handoff so it cannot block the
		// account's next checkout. Used workspaces remain transfer-gated above.
		old, err := readWorkspace(ctx, tx, a, campID, session.ExecutionWorkspaceID)
		if err != nil {
			return out, err
		}
		if old.State != "cancelled" {
			if _, err = tx.Exec(ctx, `UPDATE git_revocations SET not_before=LEAST(not_before,now()) WHERE id=(SELECT read_revocation_id FROM ticket_workspaces WHERE id=$1)`, old.ID); err != nil {
				return out, err
			}
			if err = tx.QueryRow(ctx, `UPDATE ticket_workspaces SET state='cancelled',encrypted_read_token=NULL,token_state='closed',revision=revision+1,updated_at=now() WHERE id=$1 RETURNING revision`, old.ID).Scan(&old.Revision); err != nil {
				return out, err
			}
			old.State = "cancelled"
			if err = s.emitWorkspace(ctx, tx, a, old); err != nil {
				return out, err
			}
		}
	}
	permissions := localcli.PermissionSettings{}
	if in.Location == "cloud" {
		w, err := readWorkspace(ctx, tx, a, campID, in.WorkspaceID)
		if err != nil {
			return out, err
		}
		if w.SessionID != id || w.OwnerUserID != a.UserID || w.State != "ready" {
			return out, ErrDenied
		}
		if w.TransferID != "" {
			return out, ErrTransition
		}
		if w.Revision != in.ExpectedWorkspaceRevision || w.SourceRevision != session.Revision || w.ProjectID != session.ProjectID {
			return out, ErrRevision
		}
		permissions, err = workspacePermissions(session.Provider, w.Purpose, in.Permissions, in.ExecutionApproved)
		if err != nil {
			return out, err
		}
	} else if session.OwnerDeviceID != a.DeviceID {
		return out, ErrDenied
	}
	raw, _ := json.Marshal(permissions)
	err = tx.QueryRow(ctx, `UPDATE sessions SET execution_location=$2,execution_workspace_id=NULLIF($3,'')::uuid,execution_permissions=$4,execution_epoch=execution_epoch+1,activity_revision=activity_revision+1,updated_at=now() WHERE id=$1 RETURNING execution_epoch,activity_revision`, id, in.Location, in.WorkspaceID, raw).Scan(&session.ExecutionEpoch, &session.ActivityRevision)
	if err != nil {
		return out, err
	}
	session.ExecutionLocation, session.ExecutionWorkspaceID = in.Location, in.WorkspaceID
	out, err = s.executionTarget(ctx, tx, session)
	if err != nil {
		return out, err
	}
	if err = emitSession(ctx, tx, a, campID, "session.execution", session, false); err != nil {
		return out, err
	}
	if err = s.finishCommand(ctx, tx, a, key, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
