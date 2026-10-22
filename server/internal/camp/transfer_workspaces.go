package camp

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// Queries using this expression provide workspace w and source session s.
// A staged destination cannot outlive the source snapshot it was reviewed for.
const validWorkspaceTransferSQL = `(w.transfer_id IS NULL OR EXISTS(
 SELECT 1 FROM session_transfers t WHERE t.id=w.transfer_id
 AND t.owner_user_id=w.owner_user_id AND t.camp_id=w.camp_id AND t.session_id=w.session_id
 AND t.target_location='cloud' AND t.target_account_id=w.provider_account_id AND t.target_account_epoch=w.provider_epoch
 AND t.base_commit=w.base_commit AND t.source_revision=w.source_revision
 AND ((t.state='ready' AND t.expires_at>clock_timestamp() AND t.source_epoch=s.execution_epoch AND t.source_location=s.execution_location
 AND t.source_workspace_id IS NOT DISTINCT FROM s.execution_workspace_id
 AND t.source_transcript_revision=s.transcript_revision AND t.source_permission_revision=s.permission_revision)
 OR (t.state='completed' AND s.execution_location='cloud' AND s.execution_workspace_id=w.id
 AND EXISTS(SELECT 1 FROM transfer_handoffs h WHERE h.transfer_id=t.id AND h.workspace_id=w.id AND h.state='committed'
 AND h.camp_id=t.camp_id AND h.session_id=t.session_id AND h.owner_user_id=t.owner_user_id AND h.execution_epoch=s.execution_epoch)))))`

func (s *Store) cancelTransferWorkspaces(ctx context.Context, tx pgx.Tx, a auth.Actor, t Transfer) error {
	if _, err := tx.Exec(ctx, `UPDATE transfer_handoffs SET state='invalidated',revision=revision+1 WHERE transfer_id=$1 AND state IN ('pending','claimed','acknowledged')`, t.ID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM ticket_workspaces WHERE transfer_id=$1 AND camp_id=$2 AND owner_user_id=$3 AND state IN ('queued','preparing','ready')`, t.ID, t.CampID, a.UserID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = retireWorkspaceToken(ctx, tx, id); err != nil {
			return err
		}
		w := Workspace{ID: id, CampID: t.CampID, SessionID: t.SessionID, State: "cancelled"}
		if err = tx.QueryRow(ctx, `UPDATE ticket_workspaces SET state='cancelled',revision=revision+1,updated_at=now() WHERE id=$1 RETURNING revision`, id).Scan(&w.Revision); err != nil {
			return err
		}
		if err = s.emitWorkspace(ctx, tx, a, w); err != nil {
			return err
		}
	}
	return nil
}

// Run before account/Git locks so all transfer operations keep the same
// Camp -> owner -> transfer -> account/connection lock order.
func (g *GitStore) transferWorkspaceSource(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, sessionID, id, accountID string) (Transfer, error) {
	if id == "" {
		return Transfer{}, nil
	}
	session, err := g.store.transferOwner(ctx, tx, a, campID, sessionID, true)
	if err != nil {
		return Transfer{}, err
	}
	t, err := readTransfer(ctx, tx, a, campID, sessionID, id)
	if err != nil {
		return t, err
	}
	if t.TargetLocation != "cloud" || t.TargetAccountID != accountID {
		return t, ErrDenied
	}
	if t.State == "completed" {
		var committed bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM transfer_handoffs h WHERE h.transfer_id=$1 AND h.state='committed' AND h.execution_epoch=$2 AND h.workspace_id=$3 AND h.owner_user_id=$4 AND h.camp_id=$5 AND h.session_id=$6)`, t.ID, session.ExecutionEpoch, session.ExecutionWorkspaceID, a.UserID, campID, sessionID).Scan(&committed); err != nil {
			return t, err
		}
		if !committed || session.ExecutionLocation != "cloud" {
			return t, ErrTransition
		}
		return t, nil
	}
	if t.State != "ready" || !t.ExpiresAt.After(time.Now()) || t.BaseCommit == "" {
		return t, ErrTransition
	}
	if err = transferSource(ctx, tx, session, t); err != nil {
		return t, err
	}
	_, err = g.store.transferTarget(ctx, tx, a, session, t)
	return t, err
}

func (g *GitStore) RunnerTransfer(ctx context.Context, token, workspaceID, claim string) (Transfer, error) {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback(ctx)
	lease, err := g.lockRunnerWorkspace(ctx, tx, token, workspaceID)
	if err != nil {
		return Transfer{}, err
	}
	if !lease.active(claim) || lease.transfer == nil {
		return Transfer{}, ErrDenied
	}
	return *lease.transfer, tx.Commit(ctx)
}

func (g *GitStore) RunnerTransferChunk(ctx context.Context, token, workspaceID, claim string, index int) (TransferChunk, error) {
	if index < 0 {
		return TransferChunk{}, ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return TransferChunk{}, err
	}
	defer tx.Rollback(ctx)
	lease, err := g.lockRunnerWorkspace(ctx, tx, token, workspaceID)
	if err != nil {
		return TransferChunk{}, err
	}
	if !lease.active(claim) || lease.transfer == nil {
		return TransferChunk{}, ErrDenied
	}
	chunk, err := g.store.readTransferChunk(ctx, tx, *lease.transfer, index)
	if err != nil {
		return chunk, err
	}
	return chunk, tx.Commit(ctx)
}

func (s *Store) readTransferChunk(ctx context.Context, tx pgx.Tx, t Transfer, index int) (TransferChunk, error) {
	if index < 0 || index >= (t.PayloadBytes+TransferChunkBytes-1)/TransferChunkBytes {
		return TransferChunk{}, ErrInput
	}
	var out TransferChunk
	var encrypted []byte
	if err := tx.QueryRow(ctx, `SELECT sha256,encrypted_data FROM transfer_chunks WHERE transfer_id=$1 AND ordinal=$2`, t.ID, index).Scan(&out.SHA256, &encrypted); errors.Is(err, pgx.ErrNoRows) {
		return out, ErrDenied
	} else if err != nil {
		return out, err
	}
	var err error
	out.Data, err = s.openProvider(encrypted, transferBinding(t, index))
	if err != nil || transferDigest(out.Data) != out.SHA256 {
		return TransferChunk{}, ErrInput
	}
	return out, nil
}
