package camp

import (
	"bytes"
	"context"
)

func (g *GitStore) RevokeGitHubUser(ctx context.Context, delivery string, user int64, hash []byte) error {
	if !validID(delivery) || user <= 0 || len(hash) != 32 {
		return ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO git_webhook_deliveries(id,payload_hash) VALUES($1,$2) ON CONFLICT DO NOTHING`, delivery, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var old []byte
		if err = tx.QueryRow(ctx, `SELECT payload_hash FROM git_webhook_deliveries WHERE id=$1`, delivery).Scan(&old); err != nil {
			return err
		}
		if !bytes.Equal(old, hash) {
			return ErrConflict
		}
		return tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `UPDATE git_connections SET state='needs_login',encrypted_credentials=NULL,access_expires_at=NULL,epoch=epoch+1,revision=revision+1,refresh_claim_id=NULL,refresh_started_at=NULL,updated_at=now() WHERE github_user_id=$1 AND state<>'disconnected'`, user)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE git_logins SET state='cancelled',encrypted_context=NULL,updated_at=now() WHERE connection_id IN (SELECT id FROM git_connections WHERE github_user_id=$1) AND state IN ('waiting','exchanging')`, user)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
