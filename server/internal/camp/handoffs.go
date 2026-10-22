package camp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

var ErrTransferHeld = errors.New("session is held for a reviewed transfer")

type HandoffInput struct {
	WorkspaceID               string `json:"workspace_id"`
	ExpectedWorkspaceRevision int64  `json:"expected_workspace_revision"`
	ExpectedTransferRevision  int64  `json:"expected_transfer_revision"`
}

type Handoff struct {
	ID                string    `json:"id"`
	TransferID        string    `json:"transfer_id"`
	CampID            string    `json:"camp_id"`
	SessionID         string    `json:"session_id"`
	OwnerUserID       string    `json:"owner_user_id"`
	SourceDeviceID    string    `json:"source_device_id"`
	SourceLocation    string    `json:"source_location"`
	SourceWorkspaceID string    `json:"source_workspace_id,omitempty"`
	SourceEpoch       int64     `json:"source_epoch"`
	WorkspaceID       string    `json:"workspace_id"`
	WorkspaceRevision int64     `json:"workspace_revision"`
	State             string    `json:"state"`
	ClaimID           string    `json:"claim_id,omitempty"`
	Revision          int64     `json:"revision"`
	ExpiresAt         time.Time `json:"expires_at"`
	CommitNonce       string    `json:"commit_nonce,omitempty"`
	ExecutionEpoch    int64     `json:"execution_epoch,omitempty"`
	claimDevice       string
	claimGrant        string
	ackHash           []byte
	commitHash        []byte
	commitPermissions localcli.PermissionSettings
	commitApproved    bool
}

type HandoffAck struct {
	ClaimID       string `json:"claim_id"`
	Stopped       bool   `json:"stopped"`
	PayloadSHA256 string `json:"payload_sha256"`
	ReviewSHA256  string `json:"review_sha256"`
}

type HandoffSource struct {
	Handoff   Handoff    `json:"handoff"`
	Transfer  Transfer   `json:"transfer"`
	Session   Session    `json:"session"`
	Workspace *Workspace `json:"workspace,omitempty"`
}

const liveHandoffSQL = `h.state IN ('pending','claimed','acknowledged') AND h.expires_at>clock_timestamp()
 AND t.state='ready' AND t.expires_at>clock_timestamp()
 AND (t.source_location<>'local' OR t.history_sha256 IS NOT NULL)
 AND t.source_epoch=s.execution_epoch AND t.source_location=s.execution_location
 AND t.source_workspace_id IS NOT DISTINCT FROM s.execution_workspace_id
 AND t.source_revision=s.revision AND t.source_transcript_revision=s.transcript_revision
 AND t.source_permission_revision=s.permission_revision
 AND w.state='ready' AND w.revision=h.workspace_revision AND w.transfer_id=t.id`

func sessionTransferHeld(ctx context.Context, tx pgx.Tx, sessionID string) (bool, error) {
	var held bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM transfer_handoffs h JOIN session_transfers t ON t.id=h.transfer_id JOIN sessions s ON s.id=h.session_id JOIN ticket_workspaces w ON w.id=h.workspace_id WHERE h.session_id=$1 AND `+liveHandoffSQL+`)`, sessionID).Scan(&held)
	return held, err
}

func requireUnheldSession(ctx context.Context, tx pgx.Tx, id string) error {
	held, err := sessionTransferHeld(ctx, tx, id)
	if err != nil {
		return err
	}
	if held {
		return ErrTransferHeld
	}
	return nil
}

func readHandoff(ctx context.Context, tx pgx.Tx, id, owner string) (Handoff, error) {
	var h Handoff
	if !validID(id) {
		return h, ErrDenied
	}
	var permissions []byte
	err := tx.QueryRow(ctx, `SELECT h.id,h.transfer_id,h.camp_id,h.session_id,h.owner_user_id,h.source_device_id,t.source_location,COALESCE(t.source_workspace_id::text,''),t.source_epoch,h.workspace_id,h.workspace_revision,h.state,COALESCE(h.claim_id::text,''),COALESCE(h.claim_device_id::text,''),COALESCE(h.claim_grant_id::text,''),h.ack_hash,h.revision,h.expires_at,COALESCE(h.commit_nonce::text,''),COALESCE(h.execution_epoch,0),h.commit_hash,COALESCE(h.commit_permissions,'{}'::jsonb),COALESCE(h.commit_approved,false) FROM transfer_handoffs h JOIN session_transfers t ON t.id=h.transfer_id WHERE h.id=$1 AND h.owner_user_id=$2 FOR UPDATE OF h`, id, owner).Scan(&h.ID, &h.TransferID, &h.CampID, &h.SessionID, &h.OwnerUserID, &h.SourceDeviceID, &h.SourceLocation, &h.SourceWorkspaceID, &h.SourceEpoch, &h.WorkspaceID, &h.WorkspaceRevision, &h.State, &h.ClaimID, &h.claimDevice, &h.claimGrant, &h.ackHash, &h.Revision, &h.ExpiresAt, &h.CommitNonce, &h.ExecutionEpoch, &h.commitHash, &permissions, &h.commitApproved)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, ErrDenied
	}
	if err == nil {
		err = json.Unmarshal(permissions, &h.commitPermissions)
	}
	return h, err
}

func (s *Store) emitHandoff(ctx context.Context, tx pgx.Tx, a auth.Actor, h Handoff) error {
	if err := emit(ctx, tx, a, h.CampID, "transfer.handoff", map[string]any{"id": h.ID, "transfer_id": h.TransferID, "session_id": h.SessionID, "state": h.State, "revision": h.Revision}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE events SET recipient_user_id=$2,session_id=$3 WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, h.CampID, h.OwnerUserID, h.SessionID)
	return err
}

func (s *Store) validateHandoff(ctx context.Context, tx pgx.Tx, a auth.Actor, h Handoff) (Transfer, error) {
	t, err := readTransfer(ctx, tx, a, h.CampID, h.SessionID, h.TransferID)
	if err != nil {
		return t, err
	}
	if t.SourceLocation == "local" && t.HistorySHA256 == "" {
		return t, ErrTransition
	}
	if !oneOf(h.State, "pending", "claimed", "acknowledged") || !h.ExpiresAt.After(time.Now()) || t.State != "ready" || !t.ExpiresAt.After(time.Now()) {
		return t, ErrTransition
	}
	session, err := s.readSession(ctx, tx, a, h.CampID, h.SessionID)
	if err != nil {
		return t, err
	}
	if session.OwnerDeviceID != h.SourceDeviceID {
		return t, ErrRevision
	}
	if err = transferSource(ctx, tx, session, t); err != nil {
		return t, err
	}
	if _, err = s.transferTarget(ctx, tx, a, session, t); err != nil {
		return t, err
	}
	w, err := readWorkspace(ctx, tx, a, h.CampID, h.WorkspaceID)
	if err != nil {
		return t, err
	}
	if w.TransferID != t.ID || w.State != "ready" || w.Revision != h.WorkspaceRevision || w.ProviderAccountID != t.TargetAccountID || w.ProviderEpoch != t.TargetAccountEpoch || w.BaseCommit != t.BaseCommit {
		return t, ErrRevision
	}
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM git_connections g JOIN projects p ON p.id=$4 WHERE g.id=$1 AND g.owner_user_id=$2 AND g.state='connected' AND g.epoch=$3 AND p.camp_id=$5 AND p.revision=$6 AND NOT p.archived)`, w.GitConnectionID, a.UserID, w.GitEpoch, w.ProjectID, h.CampID, w.ProjectRevision).Scan(&allowed); err != nil {
		return t, err
	}
	if !allowed {
		return t, ErrRevision
	}
	return t, nil
}

func (s *Store) BeginHandoff(ctx context.Context, a auth.Actor, campID, sessionID, transferID, key string, in HandoffInput) (Handoff, error) {
	if !validID(in.WorkspaceID) || in.ExpectedTransferRevision < 1 || in.ExpectedWorkspaceRevision < 1 {
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
	var h Handoff
	if hit, e := s.beginCommand(ctx, tx, a, key, "handoff.begin:"+transferID, in, &h); e != nil {
		return h, e
	} else if hit {
		h, err = readHandoff(ctx, tx, h.ID, a.UserID)
		if err != nil {
			return h, err
		}
		return h, tx.Commit(ctx)
	}
	t, err := readTransfer(ctx, tx, a, campID, sessionID, transferID)
	if err != nil {
		return h, err
	}
	if t.Revision != in.ExpectedTransferRevision {
		return h, ErrRevision
	}
	if t.TargetLocation != "cloud" {
		return h, ErrTransition
	}
	if _, err = tx.Exec(ctx, `UPDATE transfer_handoffs h SET state=CASE WHEN h.expires_at<=clock_timestamp() THEN 'expired' ELSE 'invalidated' END,revision=revision+1 WHERE h.session_id=$1 AND h.state IN ('pending','claimed','acknowledged') AND NOT EXISTS(SELECT 1 FROM session_transfers t JOIN sessions s ON s.id=h.session_id JOIN ticket_workspaces w ON w.id=h.workspace_id WHERE t.id=h.transfer_id AND `+liveHandoffSQL+`)`, sessionID); err != nil {
		return h, err
	}
	if err = requireUnheldSession(ctx, tx, sessionID); err != nil {
		return h, err
	}
	h = Handoff{ID: uuid.NewString(), TransferID: t.ID, CampID: campID, SessionID: sessionID, OwnerUserID: a.UserID, SourceDeviceID: session.OwnerDeviceID, SourceLocation: t.SourceLocation, SourceWorkspaceID: t.SourceWorkspaceID, SourceEpoch: t.SourceEpoch, WorkspaceID: in.WorkspaceID, WorkspaceRevision: in.ExpectedWorkspaceRevision, State: "pending", ExpiresAt: time.Now().Add(5 * time.Minute)}
	if _, err = s.validateHandoff(ctx, tx, a, h); err != nil {
		return h, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO transfer_handoffs(id,transfer_id,camp_id,session_id,owner_user_id,source_device_id,workspace_id,workspace_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, h.ID, h.TransferID, campID, sessionID, a.UserID, h.SourceDeviceID, h.WorkspaceID, h.WorkspaceRevision)
	if err != nil {
		return h, err
	}
	h, err = readHandoff(ctx, tx, h.ID, a.UserID)
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

func (s *Store) Handoff(ctx context.Context, a auth.Actor, campID, sessionID, id string) (Handoff, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Handoff{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.transferOwner(ctx, tx, a, campID, sessionID, false); err != nil {
		return Handoff{}, err
	}
	h, err := readHandoff(ctx, tx, id, a.UserID)
	if err != nil {
		return h, err
	}
	if h.CampID != campID || h.SessionID != sessionID {
		return h, ErrDenied
	}
	h, err = s.refreshHandoff(ctx, tx, a, h)
	if err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

func (s *Store) refreshHandoff(ctx context.Context, tx pgx.Tx, a auth.Actor, h Handoff) (Handoff, error) {
	id := h.ID
	var err error
	if oneOf(h.State, "pending", "claimed", "acknowledged") {
		var alive bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM transfer_handoffs h JOIN session_transfers t ON t.id=h.transfer_id JOIN sessions s ON s.id=h.session_id JOIN ticket_workspaces w ON w.id=h.workspace_id WHERE h.id=$1 AND `+liveHandoffSQL+`)`, id).Scan(&alive); err != nil {
			return h, err
		}
		if !alive {
			state := "invalidated"
			if !h.ExpiresAt.After(time.Now()) {
				state = "expired"
			}
			if _, err = tx.Exec(ctx, `UPDATE transfer_handoffs SET state=$2,revision=revision+1 WHERE id=$1`, id, state); err != nil {
				return h, err
			}
			h, err = readHandoff(ctx, tx, id, a.UserID)
			if err != nil {
				return h, err
			}
			if err = s.emitHandoff(ctx, tx, a, h); err != nil {
				return h, err
			}
		}
	}
	return h, nil
}

func (s *Store) ReleaseHandoff(ctx context.Context, a auth.Actor, campID, sessionID, id string) (Handoff, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Handoff{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.transferOwner(ctx, tx, a, campID, sessionID, true); err != nil {
		return Handoff{}, err
	}
	h, err := readHandoff(ctx, tx, id, a.UserID)
	if err != nil {
		return h, err
	}
	if h.CampID != campID || h.SessionID != sessionID {
		return h, ErrDenied
	}
	if oneOf(h.State, "released", "expired", "invalidated") {
		return h, tx.Commit(ctx)
	}
	if h.State == "committed" {
		return h, ErrTransition
	}
	if _, err = tx.Exec(ctx, `UPDATE transfer_handoffs SET state='released',revision=revision+1 WHERE id=$1`, id); err != nil {
		return h, err
	}
	h, err = readHandoff(ctx, tx, id, a.UserID)
	if err != nil {
		return h, err
	}
	if err = s.emitHandoff(ctx, tx, a, h); err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

func (s *Store) sourceHandoff(ctx context.Context, tx pgx.Tx, a auth.Actor, token, id string) (Handoff, Transfer, string, error) {
	var h Handoff
	var campID, sessionID, owner string
	err := tx.QueryRow(ctx, `SELECT camp_id,session_id,owner_user_id FROM transfer_handoffs WHERE id=$1`, id).Scan(&campID, &sessionID, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, Transfer{}, "", ErrDenied
	}
	if err != nil {
		return h, Transfer{}, "", err
	}
	if token != "" {
		a = auth.Actor{UserID: owner}
	} else if a.UserID != owner {
		return h, Transfer{}, "", ErrDenied
	}
	if _, err = s.transferOwner(ctx, tx, a, campID, sessionID, true); err != nil {
		return h, Transfer{}, "", err
	}
	h, err = readHandoff(ctx, tx, id, a.UserID)
	if err != nil {
		return h, Transfer{}, "", err
	}
	var t Transfer
	if h.State == "committed" {
		t, err = readTransfer(ctx, tx, a, campID, sessionID, h.TransferID)
	} else {
		t, err = s.validateHandoff(ctx, tx, a, h)
	}
	if err != nil {
		return h, t, "", err
	}
	if token == "" {
		if h.SourceLocation != "local" || a.DeviceID != h.SourceDeviceID {
			return h, t, "", ErrDenied
		}
		return h, t, "", nil
	}
	if h.SourceLocation != "cloud" {
		return h, t, "", ErrDenied
	}
	r, err := s.lockRunner(ctx, tx, token)
	if err != nil {
		return h, t, "", err
	}
	w, err := readWorkspace(ctx, tx, a, campID, h.SourceWorkspaceID)
	if err != nil {
		return h, t, "", err
	}
	if r.cleanup || (h.State != "committed" && w.State != "ready") || w.ProviderAccountID != r.account.ID || w.ProviderEpoch != r.account.Epoch {
		return h, t, "", ErrDenied
	}
	if h.State == "committed" {
		return h, t, r.grantID, nil
	}
	session, err := s.readSession(ctx, tx, a, campID, sessionID)
	if err != nil {
		return h, t, "", err
	}
	if _, err = s.executionTarget(ctx, tx, session); err != nil {
		return h, t, "", err
	}
	return h, t, r.grantID, nil
}

func (s *Store) ClaimHandoff(ctx context.Context, a auth.Actor, token, id, claim string) (Handoff, error) {
	if !validID(id) || !validID(claim) {
		return Handoff{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Handoff{}, err
	}
	defer tx.Rollback(ctx)
	h, _, grant, err := s.sourceHandoff(ctx, tx, a, token, id)
	if err != nil {
		return h, err
	}
	device := a.DeviceID
	if token != "" {
		device = ""
	}
	if h.State != "pending" {
		if h.ClaimID != claim || h.claimDevice != device || h.claimGrant != grant {
			return h, ErrDenied
		}
		return h, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE transfer_handoffs SET state='claimed',claim_id=$2,claim_device_id=NULLIF($3,'')::uuid,claim_grant_id=NULLIF($4,'')::uuid,revision=revision+1 WHERE id=$1`, id, claim, device, grant); err != nil {
		return h, err
	}
	h, err = readHandoff(ctx, tx, id, h.OwnerUserID)
	if err != nil {
		return h, err
	}
	if err = s.emitHandoff(ctx, tx, auth.Actor{UserID: h.OwnerUserID}, h); err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

func (s *Store) AcknowledgeHandoff(ctx context.Context, a auth.Actor, token, id string, in HandoffAck) (Handoff, error) {
	if !validID(id) || !validID(in.ClaimID) || !in.Stopped {
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
	if in.PayloadSHA256 != t.PayloadSHA256 || in.ReviewSHA256 != t.ReviewSHA256 {
		return h, ErrInput
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	if h.State == "acknowledged" {
		if subtle.ConstantTimeCompare(h.ackHash, sum[:]) != 1 {
			return h, ErrConflict
		}
		return h, tx.Commit(ctx)
	}
	if h.State != "claimed" {
		return h, ErrTransition
	}
	if _, err = tx.Exec(ctx, `UPDATE transfer_handoffs SET state='acknowledged',ack_hash=$2,revision=revision+1 WHERE id=$1`, id, sum[:]); err != nil {
		return h, err
	}
	h, err = readHandoff(ctx, tx, id, h.OwnerUserID)
	if err != nil {
		return h, err
	}
	if err = s.emitHandoff(ctx, tx, auth.Actor{UserID: h.OwnerUserID}, h); err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

func (s *Store) claimedHandoff(ctx context.Context, tx pgx.Tx, a auth.Actor, token, id, claim string) (Handoff, Transfer, error) {
	if !validID(id) || !validID(claim) {
		return Handoff{}, Transfer{}, ErrInput
	}
	h, t, grant, err := s.sourceHandoff(ctx, tx, a, token, id)
	if err != nil {
		return h, t, err
	}
	device := a.DeviceID
	if token != "" {
		device = ""
	}
	if !oneOf(h.State, "claimed", "acknowledged") || h.ClaimID != claim || h.claimDevice != device || h.claimGrant != grant {
		return h, t, ErrDenied
	}
	return h, t, nil
}

func (s *Store) HandoffSource(ctx context.Context, a auth.Actor, token, id, claim string) (HandoffSource, error) {
	var out HandoffSource
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	h, t, err := s.claimedHandoff(ctx, tx, a, token, id, claim)
	if err != nil {
		return out, err
	}
	out.Handoff, out.Transfer = h, t
	out.Session, err = s.readSession(ctx, tx, auth.Actor{UserID: h.OwnerUserID}, h.CampID, h.SessionID)
	if err != nil {
		return out, err
	}
	if h.SourceLocation == "cloud" {
		w, err := readWorkspace(ctx, tx, auth.Actor{UserID: h.OwnerUserID}, h.CampID, h.SourceWorkspaceID)
		if err != nil {
			return out, err
		}
		out.Workspace = &w
	}
	return out, tx.Commit(ctx)
}

func (s *Store) HandoffChunk(ctx context.Context, a auth.Actor, token, id, claim string, index int) (TransferChunk, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TransferChunk{}, err
	}
	defer tx.Rollback(ctx)
	_, t, err := s.claimedHandoff(ctx, tx, a, token, id, claim)
	if err != nil {
		return TransferChunk{}, err
	}
	chunk, err := s.readTransferChunk(ctx, tx, t, index)
	if err != nil {
		return chunk, err
	}
	return chunk, tx.Commit(ctx)
}

func (s *Store) RejectHandoff(ctx context.Context, a auth.Actor, token, id, claim string) (Handoff, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Handoff{}, err
	}
	defer tx.Rollback(ctx)
	h, _, err := s.claimedHandoff(ctx, tx, a, token, id, claim)
	if err != nil {
		return h, err
	}
	if _, err = tx.Exec(ctx, `UPDATE transfer_handoffs SET state='invalidated',revision=revision+1 WHERE id=$1`, id); err != nil {
		return h, err
	}
	h, err = readHandoff(ctx, tx, id, h.OwnerUserID)
	if err != nil {
		return h, err
	}
	if err = s.emitHandoff(ctx, tx, auth.Actor{UserID: h.OwnerUserID}, h); err != nil {
		return h, err
	}
	return h, tx.Commit(ctx)
}

func (s *Store) NextLocalHandoff(ctx context.Context, a auth.Actor, campID string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT h.id FROM transfer_handoffs h JOIN session_transfers t ON t.id=h.transfer_id JOIN sessions s ON s.id=h.session_id JOIN ticket_workspaces w ON w.id=h.workspace_id WHERE h.camp_id=$1 AND h.owner_user_id=$2 AND h.source_device_id=$3 AND t.source_location='local' AND h.state IN ('pending','claimed') AND (h.claim_device_id IS NULL OR h.claim_device_id=$3) AND `+liveHandoffSQL+` ORDER BY h.created_at,h.id LIMIT 1`, campID, a.UserID, a.DeviceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) NextRunnerHandoff(ctx context.Context, token string) (string, error) {
	state, err := s.RunnerAccount(ctx, token)
	if err != nil {
		return "", err
	}
	if state.Action != "idle" {
		return "", nil
	}
	var id string
	err = s.pool.QueryRow(ctx, `SELECT h.id FROM transfer_handoffs h JOIN session_transfers t ON t.id=h.transfer_id JOIN sessions s ON s.id=h.session_id JOIN ticket_workspaces w ON w.id=h.workspace_id JOIN ticket_workspaces source ON source.id=t.source_workspace_id JOIN runner_grants g ON g.installation_id=source.provider_installation_id JOIN memberships m ON m.camp_id=h.camp_id AND m.user_id=h.owner_user_id WHERE source.provider_installation_id=$1 AND source.provider_epoch=$2 AND g.token_hash=$3 AND g.epoch=$2 AND g.revoked_at IS NULL AND g.expires_at>clock_timestamp() AND NOT g.cleanup_only AND m.role<>'viewer' AND t.source_location='cloud' AND h.state IN ('pending','claimed') AND (h.claim_grant_id IS NULL OR h.claim_grant_id=g.id) AND `+liveHandoffSQL+` ORDER BY h.created_at,h.id LIMIT 1`, state.InstallationID, state.Epoch, auth.HashToken(token)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}
