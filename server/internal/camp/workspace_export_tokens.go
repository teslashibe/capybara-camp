package camp

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
)

// ExportCheckoutToken is a private delivery to the requesting owner device.
// Never persist this response in commands, shared snapshots, events or prompts.
type ExportCheckoutToken struct {
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expires_at"`
	ExportID    string    `json:"export_id"`
	DeviceID    string    `json:"device_id"`
	WorkspaceID string    `json:"workspace_id"`
	Repository  string    `json:"repository"`
	BaseCommit  string    `json:"base_commit"`
	Branch      string    `json:"branch"`
	SHA256      string    `json:"sha256"`
}

func exportTokenBinding(e WorkspaceExport, device string) string {
	return "export-checkout:v1:" + e.OwnerUserID + ":" + e.ID + ":" + device + ":" + e.ResultSHA256
}

func (g *GitStore) exportCheckoutAuthority(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, sessionID, id, digest string) (WorkspaceExport, workspaceLease, error) {
	e, l, err := g.authorizedWorkspaceExport(ctx, tx, a, campID, sessionID, id)
	if err != nil {
		return e, l, err
	}
	if e.Kind != "capture" || !e.ExpiresAt.After(time.Now().Add(30*time.Second)) {
		return e, l, ErrTransition
	}
	if digest != e.ResultSHA256 {
		return e, l, ErrRevision
	}
	if !validID(a.DeviceID) {
		return e, l, ErrDenied
	}
	var device string
	err = tx.QueryRow(ctx, `SELECT id FROM devices WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>clock_timestamp() FOR SHARE`, a.DeviceID, a.UserID).Scan(&device)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return e, l, err
}

// A committed issuing row fences uncertain provider work. Replays return only
// the same encrypted result, after checking the source and device again.
func (g *GitStore) WorkspaceExportCheckoutToken(ctx context.Context, a auth.Actor, campID, sessionID, id, digest string) (ExportCheckoutToken, error) {
	var out ExportCheckoutToken
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
	e, l, err := g.exportCheckoutAuthority(work, tx, a, campID, sessionID, id, digest)
	if err != nil {
		return out, err
	}
	var state string
	var encrypted []byte
	err = tx.QueryRow(work, `SELECT state,encrypted_token FROM workspace_export_git_grants WHERE export_id=$1 AND device_id=$2 FOR UPDATE`, id, a.DeviceID).Scan(&state, &encrypted)
	if err == nil {
		if state != "ready" {
			return out, ErrTransition
		}
		if err = g.open(encrypted, exportTokenBinding(e, a.DeviceID), &out); err != nil {
			return ExportCheckoutToken{}, err
		}
		if !out.ExpiresAt.After(time.Now()) {
			return ExportCheckoutToken{}, ErrTransition
		}
		return out, tx.Commit(work)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if _, err = tx.Exec(work, `INSERT INTO workspace_export_git_grants(export_id,device_id,next_check_at) VALUES($1,$2,now()+interval '35 seconds')`, id, a.DeviceID); err != nil {
		return out, err
	}
	if err = tx.Commit(work); err != nil {
		return out, err
	}
	accepted := false
	var grant gitprovider.ReadGrant
	defer func() {
		if accepted {
			return
		}
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		if grant.Token != "" {
			_ = g.enqueueRevocation(cleanup, a.UserID, gitprovider.Credentials{AccessToken: grant.Token, ExpiresAt: grant.ExpiresAt, RefreshExpiresAt: grant.ExpiresAt})
		}
		// Also close a ready row after an uncertain database commit: the
		// candidate is being revoked and must not be redelivered on replay.
		_, _ = g.store.pool.Exec(cleanup, `UPDATE workspace_export_git_grants SET state='closed',encrypted_token=NULL WHERE export_id=$1 AND device_id=$2`, id, a.DeviceID)
	}()
	root, err := g.token(work, a, l.value.GitConnectionID)
	if err != nil {
		return out, err
	}
	grant, err = g.scopeWorkspaceRead(work, l, root, provider)
	if err != nil {
		return out, err
	}
	tx, err = g.store.pool.Begin(work)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(work)
	current, lease, err := g.exportCheckoutAuthority(work, tx, a, campID, sessionID, id, digest)
	if err != nil {
		return out, err
	}
	if lease.value != l.value {
		return out, ErrRevision
	}
	if err = tx.QueryRow(work, `SELECT state FROM workspace_export_git_grants WHERE export_id=$1 AND device_id=$2 FOR UPDATE`, id, a.DeviceID).Scan(&state); err != nil {
		return out, err
	}
	if state != "issuing" {
		return out, ErrTransition
	}
	expires := time.Now().Add(5 * time.Minute)
	if grant.ExpiresAt.Before(expires) {
		expires = grant.ExpiresAt
	}
	if current.ExpiresAt.Before(expires) {
		expires = current.ExpiresAt
	}
	out = ExportCheckoutToken{Token: grant.Token, ExpiresAt: expires.UTC(), ExportID: id, DeviceID: a.DeviceID, WorkspaceID: l.value.ID, Repository: l.value.RepositoryName, BaseCommit: l.value.BaseCommit, Branch: l.value.Branch, SHA256: digest}
	encrypted, err = g.seal(out, exportTokenBinding(current, a.DeviceID))
	if err != nil {
		return ExportCheckoutToken{}, err
	}
	revocation, err := g.queueWorkspaceRevocation(work, tx, lease, grant, expires)
	if err != nil {
		return ExportCheckoutToken{}, err
	}
	_, err = tx.Exec(work, `UPDATE workspace_export_git_grants SET state='ready',encrypted_token=$3,expires_at=$4,revocation_id=$5,next_check_at=now()+interval '30 seconds' WHERE export_id=$1 AND device_id=$2`, id, a.DeviceID, encrypted, expires, revocation)
	if err != nil {
		return ExportCheckoutToken{}, err
	}
	if err = tx.Commit(work); err != nil {
		return ExportCheckoutToken{}, err
	}
	accepted = true
	return out, nil
}

func retireExportToken(ctx context.Context, tx pgx.Tx, id, device string) error {
	if _, err := tx.Exec(ctx, `UPDATE git_revocations SET not_before=LEAST(not_before,now()) WHERE id IN (SELECT revocation_id FROM workspace_export_git_grants WHERE export_id=$1 AND device_id=$2)`, id, device); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE workspace_export_git_grants SET state='closed',encrypted_token=NULL WHERE export_id=$1 AND device_id=$2`, id, device)
	return err
}

func (g *GitStore) reapExportTokens(ctx context.Context) error {
	rows, err := g.store.pool.Query(ctx, `SELECT e.id,e.camp_id,e.session_id,e.owner_user_id,t.device_id,e.result_sha256 FROM workspace_export_git_grants t JOIN workspace_exports e ON e.id=t.export_id WHERE t.state<>'closed' AND t.next_check_at<=clock_timestamp() ORDER BY t.next_check_at,e.id,t.device_id LIMIT 32`)
	if err != nil {
		return err
	}
	type candidate struct{ id, camp, session, owner, device, digest string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.camp, &c.session, &c.owner, &c.device, &c.digest); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		err = func() error {
			tx, err := g.store.pool.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			// Match ordinary Camp-first lock ordering even after membership removal.
			if _, err = tx.Exec(ctx, `SELECT id FROM camps WHERE id=$1 FOR UPDATE`, c.camp); err != nil {
				return err
			}
			_, _, access := g.exportCheckoutAuthority(ctx, tx, auth.Actor{UserID: c.owner, DeviceID: c.device}, c.camp, c.session, c.id, c.digest)
			if access != nil && !errors.Is(access, ErrDenied) && !errors.Is(access, ErrRevision) && !errors.Is(access, ErrTransition) && !errors.Is(access, gitprovider.ErrAuthorization) {
				return access
			}
			var expired bool
			if err = tx.QueryRow(ctx, `SELECT state='closed' OR (state='issuing' AND created_at<now()-interval '35 seconds') OR (state='ready' AND expires_at<=clock_timestamp()) FROM workspace_export_git_grants WHERE export_id=$1 AND device_id=$2 FOR UPDATE`, c.id, c.device).Scan(&expired); err != nil {
				return err
			}
			if access != nil || expired {
				if err = retireExportToken(ctx, tx, c.id, c.device); err != nil {
					return err
				}
			} else {
				if _, err = tx.Exec(ctx, `UPDATE workspace_export_git_grants SET next_check_at=now()+interval '30 seconds' WHERE export_id=$1 AND device_id=$2`, c.id, c.device); err != nil {
					return err
				}
			}
			return tx.Commit(ctx)
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
