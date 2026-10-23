package camp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func (g *GitStore) NextWorkspaceExport(ctx context.Context, token string) (*WorkspaceExport, error) {
	state, err := g.store.RunnerAccount(ctx, token)
	if err != nil {
		return nil, err
	}
	if state.Action != "idle" {
		return nil, nil
	}
	var id string
	err = g.store.pool.QueryRow(ctx, `SELECT e.id FROM workspace_exports e
 JOIN ticket_workspaces w ON w.id=e.workspace_id JOIN sessions s ON s.id=e.session_id
 JOIN projects p ON p.id=w.project_id JOIN git_connections g ON g.id=w.git_connection_id
 JOIN memberships m ON m.camp_id=w.camp_id AND m.user_id=w.owner_user_id
 WHERE w.provider_installation_id=$1 AND w.provider_epoch=$2 AND e.state IN ('pending','claimed') AND e.expires_at>clock_timestamp()
 AND w.state='ready' AND w.revision=e.workspace_revision AND s.execution_location='cloud' AND s.execution_workspace_id=w.id
 AND s.execution_epoch=e.source_epoch AND s.revision=e.source_revision AND s.transcript_revision=e.transcript_revision AND s.permission_revision=e.permission_revision
 AND `+workspaceSourceCurrentSQL+` AND s.project_id=w.project_id AND p.revision=w.project_revision AND NOT p.archived
 AND g.state='connected' AND g.epoch=w.git_epoch AND m.role<>'viewer'
 AND NOT EXISTS(SELECT 1 FROM prompt_requests r WHERE r.session_id=s.id AND r.state IN ('queued','claimed','started','cancel_requested','outcome_unknown'))
 ORDER BY e.created_at,e.id LIMIT 1`, state.InstallationID, state.Epoch).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Discovery reveals an opaque ID only; claim performs all source checks.
	return &WorkspaceExport{ID: id}, nil
}
func (g *GitStore) lockedWorkspaceExport(ctx context.Context, tx pgx.Tx, token, id string) (workspaceLease, WorkspaceExport, error) {
	var l workspaceLease
	var e WorkspaceExport
	var workspace string
	if !validID(id) {
		return l, e, ErrInput
	}
	err := tx.QueryRow(ctx, `SELECT e.workspace_id FROM workspace_exports e JOIN ticket_workspaces w ON w.id=e.workspace_id JOIN runner_grants r ON r.installation_id=w.provider_installation_id WHERE e.id=$1 AND r.token_hash=$2`, id, auth.HashToken(token)).Scan(&workspace)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, e, ErrDenied
	}
	if err != nil {
		return l, e, err
	}
	l, session, err := g.lockExecution(ctx, tx, token, workspace)
	if err != nil {
		return l, e, err
	}
	e, err = readWorkspaceExport(ctx, tx, id)
	if err != nil {
		return l, e, err
	}
	if !e.ExpiresAt.After(time.Now()) || !oneOf(e.State, "pending", "claimed", "ready") {
		return l, e, ErrTransition
	}
	if e.WorkspaceID != l.value.ID || e.WorkspaceRevision != l.value.Revision || e.OwnerUserID != session.OwnerUserID || e.CampID != session.CampID || e.SessionID != session.ID {
		return l, e, ErrRevision
	}
	if err = exportSource(ctx, tx, session, e); err != nil {
		return l, e, err
	}
	if err = requireUnheldSession(ctx, tx, e.SessionID); err != nil {
		return l, e, err
	}
	return l, e, nil
}
func (g *GitStore) ClaimWorkspaceExport(ctx context.Context, token, id, claim string) (RunnerWorkspaceExport, error) {
	var out RunnerWorkspaceExport
	if !validID(claim) {
		return out, ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	l, e, err := g.lockedWorkspaceExport(ctx, tx, token, id)
	if err != nil {
		return out, err
	}
	if e.State == "pending" {
		busy, err := installationOccupied(ctx, tx, l.value.ProviderInstallationID)
		if err != nil {
			return out, err
		}
		if busy {
			return out, ErrTransition
		}
		_, err = tx.Exec(ctx, `UPDATE workspace_exports SET state='claimed',claim_id=$2,grant_id=$3 WHERE id=$1`, e.ID, claim, l.runner.grantID)
		if err != nil {
			return out, err
		}
		e.State = "claimed"
		e.ClaimID = claim
		e.grant = l.runner.grantID
		if err = g.store.emitWorkspaceExport(ctx, tx, e); err != nil {
			return out, err
		}
	} else if e.ClaimID != claim || e.grant != l.runner.grantID {
		return out, ErrDenied
	}
	out = RunnerWorkspaceExport{Export: e, Workspace: l.value}
	return out, tx.Commit(ctx)
}
func (g *GitStore) ownedExportClaim(ctx context.Context, tx pgx.Tx, token, id, claim string) (WorkspaceExport, error) {
	l, e, err := g.lockedWorkspaceExport(ctx, tx, token, id)
	if err != nil {
		return e, err
	}
	if e.ClaimID != claim || e.grant != l.runner.grantID || !validID(claim) || !oneOf(e.State, "claimed", "ready") {
		return e, ErrDenied
	}
	return e, nil
}

type WorkspaceExportResultInput struct {
	ClaimID string `json:"claim_id"`
	SHA256  string `json:"sha256"`
	Bytes   int    `json:"bytes"`
}

func (g *GitStore) BeginWorkspaceExportResult(ctx context.Context, token, id string, in WorkspaceExportResultInput) (WorkspaceExport, error) {
	var e WorkspaceExport
	if !transferHash.MatchString(in.SHA256) || in.Bytes < 2 || in.Bytes > transfer.MaxWireBytes {
		return e, ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return e, err
	}
	defer tx.Rollback(ctx)
	e, err = g.ownedExportClaim(ctx, tx, token, id, in.ClaimID)
	if err != nil {
		return e, err
	}
	if e.Kind == "inspect" && in.Bytes > 8<<20 {
		return e, ErrInput
	}
	if e.ResultBytes != 0 {
		if e.ResultSHA256 != in.SHA256 || e.ResultBytes != in.Bytes {
			return e, ErrConflict
		}
		return e, tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `UPDATE workspace_exports SET result_sha256=$2,result_bytes=$3 WHERE id=$1`, id, in.SHA256, in.Bytes)
	if err != nil {
		return e, err
	}
	e.ResultSHA256 = in.SHA256
	e.ResultBytes = in.Bytes
	return e, tx.Commit(ctx)
}
func (g *GitStore) PutWorkspaceExportChunk(ctx context.Context, token, id, claim string, index int, in TransferChunk) (WorkspaceExport, error) {
	var e WorkspaceExport
	if index < 0 || len(in.Data) == 0 || len(in.Data) > TransferChunkBytes || transferDigest(in.Data) != in.SHA256 {
		return e, ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return e, err
	}
	defer tx.Rollback(ctx)
	e, err = g.ownedExportClaim(ctx, tx, token, id, claim)
	if err != nil {
		return e, err
	}
	if index >= (e.ResultBytes+TransferChunkBytes-1)/TransferChunkBytes || len(in.Data) != min(TransferChunkBytes, e.ResultBytes-index*TransferChunkBytes) {
		return e, ErrInput
	}
	var old string
	err = tx.QueryRow(ctx, `SELECT sha256 FROM workspace_export_chunks WHERE export_id=$1 AND ordinal=$2`, id, index).Scan(&old)
	if err == nil {
		if old != in.SHA256 {
			return e, ErrConflict
		}
		return e, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return e, err
	}
	if e.State != "claimed" {
		return e, ErrTransition
	}
	encrypted, err := g.store.sealProvider(in.Data, exportChunkBinding(e, index))
	if err != nil {
		return e, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO workspace_export_chunks(export_id,ordinal,sha256,encrypted_data) VALUES($1,$2,$3,$4)`, id, index, in.SHA256, encrypted)
	if err != nil {
		return e, err
	}
	e, err = readWorkspaceExport(ctx, tx, id)
	if err != nil {
		return e, err
	}
	return e, tx.Commit(ctx)
}
func validateWorkspaceExportResult(e WorkspaceExport, raw []byte) error {
	if e.Kind == "capture" {
		m, err := transfer.Decode(raw)
		if err != nil {
			return ErrInput
		}
		// Cloud history remains in the shared store; a runner cannot invent a
		// local history binding or include paths the owner did not select.
		if m.History != nil || len(m.Files)+len(m.Excluded) != len(e.Paths) {
			return ErrInput
		}
		selected := map[string]bool{}
		for _, p := range e.Paths {
			selected[p] = true
		}
		for _, f := range m.Files {
			if !selected[f.Path] {
				return ErrInput
			}
			delete(selected, f.Path)
		}
		for _, f := range m.Excluded {
			if !selected[f.Path] {
				return ErrInput
			}
			delete(selected, f.Path)
		}
		if len(selected) != 0 {
			return ErrInput
		}
		return nil
	}
	var value transfer.ChangeSet
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || len(value.Files) > 4096 {
		return ErrInput
	}
	if (transfer.Manifest{Version: 1, BaseCommit: value.BaseCommit, Branch: value.Branch}).Validate() != nil {
		return ErrInput
	}
	paths := []string{}
	for _, f := range value.Files {
		if !oneOf(f.Kind, "added", "modified", "deleted") || len(f.Excluded) > 80 {
			return ErrInput
		}
		paths = append(paths, f.Path)
	}
	if !exportPaths(paths, 4096) {
		return ErrInput
	}
	return nil
}

func (s *Store) exportPayload(ctx context.Context, tx pgx.Tx, e WorkspaceExport) ([]byte, error) {
	if e.ResultBytes < 2 || e.ResultBytes > transfer.MaxWireBytes {
		return nil, ErrInput
	}
	raw := make([]byte, 0, e.ResultBytes)
	for i := 0; len(raw) < e.ResultBytes; i++ {
		chunk, err := s.exportChunk(ctx, tx, e, i)
		if err != nil {
			return nil, err
		}
		if len(chunk.Data) == 0 || len(raw)+len(chunk.Data) > e.ResultBytes {
			return nil, ErrInput
		}
		raw = append(raw, chunk.Data...)
	}
	if transferDigest(raw) != e.ResultSHA256 {
		return nil, ErrInput
	}
	return raw, nil
}

func validateExportCommits(raw []byte, w Workspace) (transfer.Manifest, error) {
	m, err := transfer.Decode(raw)
	if err != nil || m.Branch != w.Branch {
		return transfer.Manifest{}, ErrInput
	}
	remote := w.BaseCommit
	if w.RemoteBaseCommit != "" {
		remote = w.RemoteBaseCommit
	}
	if m.Commits == nil {
		if m.BaseCommit != remote {
			return transfer.Manifest{}, ErrInput
		}
	} else if m.Commits.BaseCommit != remote {
		return transfer.Manifest{}, ErrInput
	}
	if w.BaseCommit != remote {
		found := false
		if m.Commits != nil {
			for _, object := range m.Commits.Objects {
				found = found || object.Kind == "commit" && object.ID == w.BaseCommit
			}
		}
		if !found {
			return transfer.Manifest{}, ErrInput
		}
	}
	return m, nil
}
func (g *GitStore) FinishWorkspaceExport(ctx context.Context, token, id, claim string, failed bool) (WorkspaceExport, error) {
	if failed {
		return g.failWorkspaceExport(ctx, token, id, claim)
	}
	var e WorkspaceExport
	select {
	case g.store.transferSlots <- struct{}{}:
		defer func() { <-g.store.transferSlots }()
	case <-ctx.Done():
		return e, ctx.Err()
	default:
		return e, auth.ErrRateLimited
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return e, err
	}
	defer tx.Rollback(ctx)
	e, err = g.ownedExportClaim(ctx, tx, token, id, claim)
	if err != nil {
		return e, err
	}
	if e.State == "ready" {
		return e, tx.Commit(ctx)
	}
	if e.ResultBytes < 2 {
		return e, ErrTransition
	}
	raw, err := g.store.exportPayload(ctx, tx, e)
	if err != nil {
		return e, err
	}
	if err = validateWorkspaceExportResult(e, raw); err != nil {
		return e, err
	}
	if e.Kind == "capture" {
		workspace, err := readWorkspace(ctx, tx, auth.Actor{UserID: e.OwnerUserID}, e.CampID, e.WorkspaceID)
		if err != nil {
			return e, err
		}
		if _, err = validateExportCommits(raw, workspace); err != nil {
			return e, err
		}
	}
	e.State = "ready"
	_, err = tx.Exec(ctx, `UPDATE workspace_exports SET state=$2 WHERE id=$1`, id, e.State)
	if err != nil {
		return e, err
	}
	if err = g.store.emitWorkspaceExport(ctx, tx, e); err != nil {
		return e, err
	}
	return e, tx.Commit(ctx)
}

// A claimant may discard its own upload after the source or Git permission has
// changed. This returns no payload and cannot transfer or restart execution.
func (g *GitStore) failWorkspaceExport(ctx context.Context, token, id, claim string) (WorkspaceExport, error) {
	var e WorkspaceExport
	if !validID(id) || !validID(claim) {
		return e, ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return e, err
	}
	defer tx.Rollback(ctx)
	var campID, owner string
	err = tx.QueryRow(ctx, `SELECT e.camp_id,e.owner_user_id FROM workspace_exports e JOIN ticket_workspaces w ON w.id=e.workspace_id JOIN runner_grants r ON r.installation_id=w.provider_installation_id WHERE e.id=$1 AND r.token_hash=$2`, id, auth.HashToken(token)).Scan(&campID, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrDenied
	}
	if err != nil {
		return e, err
	}
	if _, err = g.store.lock(ctx, tx, auth.Actor{UserID: owner}, campID, true); err != nil {
		return e, err
	}
	r, err := g.store.lockRunner(ctx, tx, token)
	if err != nil {
		return e, err
	}
	e, err = readWorkspaceExport(ctx, tx, id)
	if err != nil {
		return e, err
	}
	if r.cleanup || e.OwnerUserID != r.account.OwnerUserID || e.ClaimID != claim || e.grant != r.grantID {
		return e, ErrDenied
	}
	if e.State == "failed" {
		return e, tx.Commit(ctx)
	}
	if e.State != "claimed" {
		return e, ErrTransition
	}
	if _, err = tx.Exec(ctx, `UPDATE workspace_exports SET state='failed' WHERE id=$1`, id); err != nil {
		return e, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM workspace_export_chunks WHERE export_id=$1`, id); err != nil {
		return e, err
	}
	e.State = "failed"
	e.Uploaded = []int{}
	if err = g.store.emitWorkspaceExport(ctx, tx, e); err != nil {
		return e, err
	}
	return e, tx.Commit(ctx)
}
