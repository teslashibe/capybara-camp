package camp

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
)

func (g *GitStore) queueRevocation(ctx context.Context, tx pgx.Tx, owner string, creds gitprovider.Credentials) error {
	id := uuid.NewString()
	encrypted, err := g.seal(creds, "git-revoke:v1:"+owner+":"+id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO git_revocations(id,owner_user_id,encrypted_credentials,expires_at) VALUES($1,$2,$3,$4)`, id, owner, encrypted, creds.RefreshExpiresAt)
	return err
}
func (g *GitStore) enqueueRevocation(ctx context.Context, owner string, creds gitprovider.Credentials) error {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = g.queueRevocation(ctx, tx, owner, creds); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Refresh is serialized before calling GitHub. An interrupted refresh may have
// consumed its one-use refresh token; recovery requires sign-in, never replay.
func (g *GitStore) token(ctx context.Context, a auth.Actor, id string) (string, error) {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	c, err := g.lock(ctx, tx, a, id)
	if err != nil {
		return "", err
	}
	if c.State != "connected" {
		return "", gitprovider.ErrAuthorization
	}
	var encrypted []byte
	var claim *string
	err = tx.QueryRow(ctx, `SELECT encrypted_credentials,refresh_claim_id FROM git_connections WHERE id=$1`, id).Scan(&encrypted, &claim)
	if err != nil {
		return "", err
	}
	if claim != nil {
		return "", ErrTransition
	}
	var old gitprovider.Credentials
	if err = g.open(encrypted, gitCredentialBinding(c), &old); err != nil {
		return "", err
	}
	if old.ExpiresAt.After(time.Now().Add(2 * time.Minute)) {
		return old.AccessToken, tx.Commit(ctx)
	}
	claimID := uuid.NewString()
	_, err = tx.Exec(ctx, `UPDATE git_connections SET refresh_claim_id=$2,refresh_started_at=now() WHERE id=$1`, id, claimID)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	if !old.RefreshExpiresAt.After(time.Now()) {
		_ = g.failRefresh(ctx, c, claimID)
		return "", gitprovider.ErrAuthorization
	}
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fresh, err := g.provider.Refresh(work, old)
	if err == nil {
		var user gitprovider.User
		user, err = g.provider.User(work, fresh.AccessToken)
		if err == nil && user.ID != c.GitHubUserID {
			err = gitprovider.ErrAuthorization
		}
	}
	if err != nil {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer done()
		if fresh.AccessToken != "" {
			_ = g.enqueueRevocation(cleanup, c.OwnerUserID, fresh)
		}
		_ = g.failRefresh(cleanup, c, claimID)
		return "", gitprovider.ErrAuthorization
	}
	if !fresh.Valid() {
		_ = g.failRefresh(ctx, c, claimID)
		return "", gitprovider.ErrResponse
	}
	encrypted, err = g.seal(fresh, gitCredentialBinding(c))
	if err != nil {
		return "", err
	}
	tag, err := g.store.pool.Exec(ctx, `UPDATE git_connections SET encrypted_credentials=$4,access_expires_at=$5,revision=revision+1,refresh_claim_id=NULL,refresh_started_at=NULL,updated_at=now() WHERE id=$1 AND epoch=$2 AND refresh_claim_id=$3 AND state='connected'`, c.ID, c.Epoch, claimID, encrypted, fresh.ExpiresAt)
	if err != nil || tag.RowsAffected() != 1 {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer done()
		_ = g.enqueueRevocation(cleanup, c.OwnerUserID, fresh)
		if err != nil {
			return "", err
		}
		return "", ErrTransition
	}
	return fresh.AccessToken, nil
}
func (g *GitStore) failRefresh(ctx context.Context, c GitConnection, claim string) error {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := g.lock(ctx, tx, auth.Actor{UserID: c.OwnerUserID}, c.ID)
	if err != nil {
		return err
	}
	if current.Epoch != c.Epoch {
		return nil
	}
	var encrypted []byte
	var active *string
	if err = tx.QueryRow(ctx, `SELECT encrypted_credentials,refresh_claim_id FROM git_connections WHERE id=$1`, c.ID).Scan(&encrypted, &active); err != nil {
		return err
	}
	if active == nil || *active != claim {
		return nil
	}
	var old gitprovider.Credentials
	if err = g.open(encrypted, gitCredentialBinding(c), &old); err != nil {
		return err
	}
	if err = g.queueRevocation(ctx, tx, c.OwnerUserID, old); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE git_connections SET state='needs_login',encrypted_credentials=NULL,access_expires_at=NULL,revision=revision+1,epoch=epoch+1,refresh_claim_id=NULL,refresh_started_at=NULL,updated_at=now() WHERE id=$1`, c.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (g *GitStore) markRejected(ctx context.Context, a auth.Actor, id, token string, err error) {
	if !errors.Is(err, gitprovider.ErrAuthorization) {
		return
	}
	tx, e := g.store.pool.Begin(ctx)
	if e != nil {
		return
	}
	defer tx.Rollback(ctx)
	c, e := g.lock(ctx, tx, a, id)
	if e != nil || c.State != "connected" {
		return
	}
	var encrypted []byte
	if e = tx.QueryRow(ctx, `SELECT encrypted_credentials FROM git_connections WHERE id=$1`, id).Scan(&encrypted); e != nil {
		return
	}
	var creds gitprovider.Credentials
	if g.open(encrypted, gitCredentialBinding(c), &creds) != nil || creds.AccessToken != token {
		return
	}
	_, e = tx.Exec(ctx, `UPDATE git_connections SET state='needs_login',encrypted_credentials=NULL,access_expires_at=NULL,epoch=epoch+1,revision=revision+1,refresh_claim_id=NULL,refresh_started_at=NULL,updated_at=now() WHERE id=$1`, id)
	if e == nil {
		_ = tx.Commit(ctx)
	}
}
func (g *GitStore) Installations(ctx context.Context, a auth.Actor, id string, page int) (gitprovider.Installations, error) {
	token, err := g.token(ctx, a, id)
	if err != nil {
		return gitprovider.Installations{}, err
	}
	out, err := g.provider.Installations(ctx, token, page)
	g.markRejected(ctx, a, id, token, err)
	return out, err
}
func (g *GitStore) Repositories(ctx context.Context, a auth.Actor, id string, installationID int64, installationPage, page int) (gitprovider.Repositories, error) {
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	token, err := g.token(work, a, id)
	if err != nil {
		return gitprovider.Repositories{}, err
	}
	installs, err := g.provider.Installations(work, token, installationPage)
	if err != nil {
		g.markRejected(ctx, a, id, token, err)
		return gitprovider.Repositories{}, err
	}
	for _, installation := range installs.Values {
		if installation.ID == installationID {
			out, err := g.provider.Repositories(work, token, installation, page)
			g.markRejected(ctx, a, id, token, err)
			return out, err
		}
	}
	return gitprovider.Repositories{}, ErrDenied
}

// Reap clears expired attempts and resumes idempotent token revocations. It does
// not retry OAuth exchanges or refreshes after their outcome becomes uncertain.
func (g *GitStore) Reap(ctx context.Context) error {
	if err := g.ReapWorkspaces(ctx); err != nil {
		return err
	}
	if err := g.reapExportTokens(ctx); err != nil {
		return err
	}
	if _, err := g.store.pool.Exec(ctx, `UPDATE git_logins SET state='expired',encrypted_context=NULL,updated_at=now() WHERE state IN ('waiting','exchanging') AND expires_at<=clock_timestamp()`); err != nil {
		return err
	}
	rows, err := g.store.pool.Query(ctx, `SELECT id,owner_user_id,epoch,refresh_claim_id FROM git_connections WHERE refresh_started_at<now()-interval '30 seconds' ORDER BY refresh_started_at LIMIT 16`)
	if err != nil {
		return err
	}
	type abandoned struct {
		connection GitConnection
		claim      string
	}
	var claims []abandoned
	for rows.Next() {
		var item abandoned
		if err = rows.Scan(&item.connection.ID, &item.connection.OwnerUserID, &item.connection.Epoch, &item.claim); err != nil {
			rows.Close()
			return err
		}
		claims = append(claims, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range claims {
		if err = g.failRefresh(ctx, item.connection, item.claim); err != nil {
			return err
		}
	}
	if _, err = g.store.pool.Exec(ctx, `DELETE FROM git_revocations WHERE expires_at<=clock_timestamp()`); err != nil {
		return err
	}
	for i := 0; i < 8; i++ {
		tx, err := g.store.pool.Begin(ctx)
		if err != nil {
			return err
		}
		var id, owner string
		var encrypted []byte
		err = tx.QueryRow(ctx, `SELECT id,owner_user_id,encrypted_credentials FROM git_revocations WHERE not_before<=now() AND (lease_until IS NULL OR lease_until<=now()) ORDER BY not_before,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &owner, &encrypted)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return nil
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		claim := uuid.NewString()
		_, err = tx.Exec(ctx, `UPDATE git_revocations SET claim_id=$2,lease_until=now()+interval '30 seconds' WHERE id=$1`, id, claim)
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		var creds gitprovider.Credentials
		if err = g.open(encrypted, "git-revoke:v1:"+owner+":"+id, &creds); err != nil {
			return err
		}
		err = g.provider.Revoke(ctx, creds)
		if err == nil {
			_, err = g.store.pool.Exec(ctx, `DELETE FROM git_revocations WHERE id=$1 AND claim_id=$2`, id, claim)
		} else {
			_, err = g.store.pool.Exec(ctx, `UPDATE git_revocations SET not_before=now()+interval '5 minutes',claim_id=NULL,lease_until=NULL WHERE id=$1 AND claim_id=$2`, id, claim)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
