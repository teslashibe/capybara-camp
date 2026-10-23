package camp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
)

// This response is only available to the claiming installation. Never embed it
// in a human API response, shared snapshot, event, log, or model prompt.
type RunnerWorkspaceToken struct {
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expires_at"`
	WorkspaceID string    `json:"workspace_id"`
	ClaimID     string    `json:"claim_id"`
}

type workspaceTokenProvider interface {
	ScopeRead(context.Context, string, time.Time, int64, gitprovider.Installation, gitprovider.Repository) (gitprovider.ReadGrant, error)
}

func workspaceTokenBinding(l workspaceLease) string {
	return fmt.Sprintf("workspace-read:v1:%s:%s:%s:%s:%d", l.value.OwnerUserID, l.value.ID, l.claim, l.grant, l.value.GitEpoch)
}

func (g *GitStore) RunnerWorkspaceToken(ctx context.Context, token, id, claim string) (RunnerWorkspaceToken, error) {
	var out RunnerWorkspaceToken
	provider, ok := g.provider.(workspaceTokenProvider)
	if !ok {
		return out, ErrCloudUnavailable
	}
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := g.store.pool.Begin(work)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(work)
	l, err := g.lockRunnerWorkspace(work, tx, token, id)
	if err != nil {
		return out, err
	}
	if !l.active(claim) {
		return out, ErrTransition
	}
	if l.tokenState == "ready" {
		var encrypted []byte
		if err = tx.QueryRow(work, `SELECT encrypted_read_token FROM ticket_workspaces WHERE id=$1`, id).Scan(&encrypted); err != nil {
			return out, err
		}
		if err = g.open(encrypted, workspaceTokenBinding(l), &out); err != nil {
			return out, err
		}
		if !out.ExpiresAt.After(time.Now()) {
			return RunnerWorkspaceToken{}, ErrTransition
		}
		return out, nil
	}
	if l.tokenState != "" {
		return out, ErrTransition
	}
	if _, err = tx.Exec(work, `UPDATE ticket_workspaces SET token_state='issuing' WHERE id=$1`, id); err != nil {
		return out, err
	}
	if err = tx.Commit(work); err != nil {
		return out, err
	}

	// The durable issuing state prevents a second token request after an
	// uncertain result. A saved token can be delivered again to the same claim.
	rootToken, err := g.token(work, auth.Actor{UserID: l.value.OwnerUserID}, l.value.GitConnectionID)
	var grant gitprovider.ReadGrant
	if err == nil {
		grant, err = g.scopeWorkspaceRead(work, l, rootToken, provider)
	}
	if err != nil {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		_ = g.failWorkspaceToken(cleanup, l)
		return out, err
	}
	accepted := false
	defer func() {
		if !accepted {
			cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer done()
			_ = g.enqueueRevocation(cleanup, l.value.OwnerUserID, gitprovider.Credentials{AccessToken: grant.Token, ExpiresAt: grant.ExpiresAt, RefreshExpiresAt: grant.ExpiresAt})
		}
	}()
	tx, err = g.store.pool.Begin(work)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(work)
	current, err := g.lockRunnerWorkspace(work, tx, token, id)
	if err != nil {
		return out, err
	}
	if !current.active(claim) || current.tokenState != "issuing" {
		return out, ErrTransition
	}
	expires := grant.ExpiresAt
	if current.deadline.Before(expires) {
		expires = current.deadline
	}
	expires = expires.UTC()
	out = RunnerWorkspaceToken{Token: grant.Token, ExpiresAt: expires, WorkspaceID: id, ClaimID: claim}
	encrypted, err := g.seal(out, workspaceTokenBinding(current))
	if err != nil {
		return RunnerWorkspaceToken{}, err
	}
	revocation, err := g.queueWorkspaceRevocation(work, tx, current, grant, expires)
	if err != nil {
		return RunnerWorkspaceToken{}, err
	}
	_, err = tx.Exec(work, `UPDATE ticket_workspaces SET token_state='ready',encrypted_read_token=$2,read_token_expires_at=$3,read_revocation_id=$4 WHERE id=$1`, id, encrypted, expires, revocation)
	if err != nil {
		return RunnerWorkspaceToken{}, err
	}
	if err = tx.Commit(work); err != nil {
		return RunnerWorkspaceToken{}, err
	}
	accepted = true
	return out, nil
}

func (g *GitStore) scopeWorkspaceRead(ctx context.Context, l workspaceLease, token string, provider workspaceTokenProvider) (gitprovider.ReadGrant, error) {
	var empty gitprovider.ReadGrant
	installs, err := g.provider.Installations(ctx, token, l.installationPage)
	if err != nil {
		return empty, err
	}
	var install *gitprovider.Installation
	for i := range installs.Values {
		if installs.Values[i].ID == l.value.GitHubInstallationID {
			install = &installs.Values[i]
			break
		}
	}
	if install == nil || install.Suspended {
		return empty, gitprovider.ErrAccess
	}
	pages, err := g.provider.Repositories(ctx, token, *install, l.repositoryPage)
	if err != nil {
		return empty, err
	}
	var repo *gitprovider.Repository
	for i := range pages.Values {
		if pages.Values[i].ID == l.value.RepositoryID {
			repo = &pages.Values[i]
			break
		}
	}
	if repo == nil || repo.FullName != l.value.RepositoryName || repo.InstallationID != install.ID || !repo.Read || (l.value.Purpose == "implementation" && !repo.Write) {
		return empty, gitprovider.ErrAccess
	}
	var expiry time.Time
	err = g.store.pool.QueryRow(ctx, `SELECT access_expires_at FROM git_connections WHERE id=$1 AND owner_user_id=$2 AND state='connected' AND epoch=$3`, l.value.GitConnectionID, l.value.OwnerUserID, l.value.GitEpoch).Scan(&expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, gitprovider.ErrAuthorization
	}
	if err != nil {
		return empty, err
	}
	grant, err := provider.ScopeRead(ctx, token, expiry, l.value.GitHubUserID, *install, *repo)
	if err != nil {
		return empty, err
	}
	if !strings.HasPrefix(grant.Token, "ghu_") || len(grant.Token) < 12 || len(grant.Token) > 512 || strings.ContainsFunc(grant.Token, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}) || grant.Token == token || grant.RepositoryID != repo.ID || grant.UserID != l.value.GitHubUserID || !grant.ExpiresAt.After(time.Now().Add(30*time.Second)) || grant.ExpiresAt.After(expiry) {
		if grant.Token != "" && grant.Token != token {
			_ = g.enqueueRevocation(ctx, l.value.OwnerUserID, gitprovider.Credentials{AccessToken: grant.Token, ExpiresAt: grant.ExpiresAt, RefreshExpiresAt: expiry})
		}
		return empty, gitprovider.ErrResponse
	}
	return grant, nil
}

func (g *GitStore) queueWorkspaceRevocation(ctx context.Context, tx pgx.Tx, l workspaceLease, grant gitprovider.ReadGrant, at time.Time) (string, error) {
	id := uuid.NewString()
	creds := gitprovider.Credentials{AccessToken: grant.Token, ExpiresAt: grant.ExpiresAt, RefreshExpiresAt: grant.ExpiresAt}
	encrypted, err := g.seal(creds, "git-revoke:v1:"+l.value.OwnerUserID+":"+id)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO git_revocations(id,owner_user_id,encrypted_credentials,expires_at,not_before) VALUES($1,$2,$3,$4,$5)`, id, l.value.OwnerUserID, encrypted, grant.ExpiresAt, at)
	return id, err
}

func (g *GitStore) failWorkspaceToken(ctx context.Context, l workspaceLease) error {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = g.store.lock(ctx, tx, auth.Actor{UserID: l.value.OwnerUserID}, l.value.CampID, false); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE ticket_workspaces SET token_state='failed',state='failed',failure_code='git_grant_failed',revision=revision+1,updated_at=now() WHERE id=$1 AND claim_id=$2 AND state='preparing' AND token_state='issuing'`, l.value.ID, l.claim)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		value, err := readWorkspace(ctx, tx, auth.Actor{UserID: l.value.OwnerUserID}, l.value.CampID, l.value.ID)
		if err != nil {
			return err
		}
		if err = g.emitWorkspace(ctx, tx, auth.Actor{UserID: l.value.OwnerUserID}, value); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Reaping fences stale preparation and retires credentials without trusting
// a disconnected worker. It never resubmits a provider or GitHub operation.
func (g *GitStore) ReapWorkspaces(ctx context.Context) error {
	rows, err := g.store.pool.Query(ctx, `SELECT w.id,w.camp_id,w.owner_user_id FROM ticket_workspaces w
 JOIN provider_installations p ON p.id=w.provider_installation_id JOIN git_connections g ON g.id=w.git_connection_id JOIN sessions s ON s.id=w.session_id JOIN projects j ON j.id=w.project_id
 LEFT JOIN runner_grants r ON r.id=w.claim_grant_id LEFT JOIN memberships m ON m.camp_id=w.camp_id AND m.user_id=w.owner_user_id
 WHERE w.state IN ('queued','preparing','ready') AND (p.state<>'connected' OR p.epoch<>w.provider_epoch OR p.credential_expires_at<=clock_timestamp() OR g.state<>'connected' OR g.epoch<>w.git_epoch OR m.role IS NULL OR m.role='viewer' OR NOT `+workspaceSourceCurrentSQL+` OR s.project_id<>w.project_id OR j.revision<>w.project_revision OR j.archived
 OR NOT `+validWorkspaceTransferSQL+`
 OR (w.state='preparing' AND (w.lease_until<=clock_timestamp() OR w.prepare_deadline<=clock_timestamp() OR r.revoked_at IS NOT NULL OR r.expires_at<=clock_timestamp()))) ORDER BY w.created_at LIMIT 32`)
	if err != nil {
		return err
	}
	type candidate struct{ id, camp, owner string }
	var values []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.camp, &c.owner); err != nil {
			rows.Close()
			return err
		}
		values = append(values, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range values {
		if err = g.reapWorkspace(ctx, c.id, c.camp, c.owner); err != nil {
			return err
		}
	}
	return nil
}

func (g *GitStore) reapWorkspace(ctx context.Context, id, campID, owner string) error {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM camps WHERE id=$1 FOR UPDATE`, campID).Scan(&locked); err != nil {
		return err
	}
	var state, reason string
	err = tx.QueryRow(ctx, `SELECT w.state,CASE WHEN w.state='preparing' AND (w.lease_until<=clock_timestamp() OR w.prepare_deadline<=clock_timestamp()) THEN 'lease_expired' ELSE 'authority_changed' END
 FROM ticket_workspaces w JOIN provider_installations p ON p.id=w.provider_installation_id JOIN git_connections g ON g.id=w.git_connection_id JOIN sessions s ON s.id=w.session_id JOIN projects j ON j.id=w.project_id
 LEFT JOIN runner_grants r ON r.id=w.claim_grant_id LEFT JOIN memberships m ON m.camp_id=w.camp_id AND m.user_id=w.owner_user_id
 WHERE w.id=$1 AND w.state IN ('queued','preparing','ready') AND (p.state<>'connected' OR p.epoch<>w.provider_epoch OR p.credential_expires_at<=clock_timestamp() OR g.state<>'connected' OR g.epoch<>w.git_epoch OR m.role IS NULL OR m.role='viewer' OR NOT `+workspaceSourceCurrentSQL+` OR s.project_id<>w.project_id OR j.revision<>w.project_revision OR j.archived
 OR NOT `+validWorkspaceTransferSQL+`
 OR (w.state='preparing' AND (w.lease_until<=clock_timestamp() OR w.prepare_deadline<=clock_timestamp() OR r.revoked_at IS NOT NULL OR r.expires_at<=clock_timestamp()))) FOR UPDATE OF w`, id).Scan(&state, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = retireWorkspaceToken(ctx, tx, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE ticket_workspaces SET state='failed',failure_code=$2,revision=revision+1,updated_at=now() WHERE id=$1`, id, reason); err != nil {
		return err
	}
	value, err := readWorkspaceForReap(ctx, tx, owner, campID, id)
	if err != nil {
		return err
	}
	if err = g.emitWorkspace(ctx, tx, auth.Actor{UserID: owner}, value); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func readWorkspaceForReap(ctx context.Context, tx pgx.Tx, owner, campID, id string) (Workspace, error) {
	// A removed member cannot read this event, but its private token still needs
	// retirement. The reaper only emits identifiers, state and revision.
	value := Workspace{ID: id, CampID: campID, OwnerUserID: owner}
	err := tx.QueryRow(ctx, `SELECT session_id,state,revision FROM ticket_workspaces WHERE id=$1 AND owner_user_id=$2`, id, owner).Scan(&value.SessionID, &value.State, &value.Revision)
	return value, err
}
