package camp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

type RunnerWorkspaceClaim struct {
	Workspace  Workspace `json:"workspace"`
	ClaimID    string    `json:"claim_id"`
	LeaseUntil time.Time `json:"lease_until"`
	Deadline   time.Time `json:"deadline"`
}

type WorkspaceReport struct {
	State           string                 `json:"state"`
	BaseCommit      string                 `json:"base_commit,omitempty"`
	Branch          string                 `json:"branch,omitempty"`
	FailureCode     string                 `json:"failure_code,omitempty"`
	TransferReceipt *transfer.StageReceipt `json:"transfer_receipt,omitempty"`
}

type workspaceLease struct {
	value            Workspace
	runner           runnerAccount
	claim            string
	grant            string
	until            time.Time
	deadline         time.Time
	tokenState       string
	installationPage int
	repositoryPage   int
	transfer         *Transfer
}

// The Camp lock precedes account/connection locks, as it does during human
// preparation. The initial lookup grants nothing; the token is checked again
// under the account lock before any row or credential is returned.
func (g *GitStore) lockRunnerWorkspace(ctx context.Context, tx pgx.Tx, token, id string) (workspaceLease, error) {
	var out workspaceLease
	if !validID(id) {
		return out, ErrInput
	}
	var campID, owner string
	err := tx.QueryRow(ctx, `SELECT w.camp_id,w.owner_user_id FROM ticket_workspaces w JOIN runner_grants r ON r.installation_id=w.provider_installation_id WHERE w.id=$1 AND r.token_hash=$2`, id, auth.HashToken(token)).Scan(&campID, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrDenied
	}
	if err != nil {
		return out, err
	}
	a := auth.Actor{UserID: owner}
	if _, err = g.store.lock(ctx, tx, a, campID, true); err != nil {
		return out, err
	}
	out.value, err = readWorkspace(ctx, tx, a, campID, id)
	if err != nil {
		return out, err
	}
	if out.value.TransferID != "" {
		t, err := g.transferWorkspaceSource(ctx, tx, a, campID, out.value.SessionID, out.value.TransferID, out.value.ProviderAccountID)
		if err != nil {
			return out, err
		}
		if t.BaseCommit != out.value.BaseCommit || t.RemoteBaseCommit != out.value.RemoteBaseCommit || t.SourceRevision != out.value.SourceRevision {
			return out, ErrRevision
		}
		out.transfer = &t
	}
	out.runner, err = g.store.lockRunner(ctx, tx, token)
	if err != nil {
		return out, err
	}
	if out.runner.cleanup {
		return out, ErrDenied
	}
	if out.runner.account.ID != out.value.ProviderAccountID || out.runner.account.Epoch != out.value.ProviderEpoch || out.runner.account.State != "connected" {
		return out, ErrDenied
	}
	authority, err := g.workspaceAuthority(ctx, tx, a, campID, out.value.SessionID, WorkspaceInput{ProviderAccountID: out.value.ProviderAccountID, GitConnectionID: out.value.GitConnectionID})
	if err != nil {
		return out, err
	}
	if authority.git.Epoch != out.value.GitEpoch || authority.project.ID != out.value.ProjectID || authority.project.AuthorityRevision != out.value.ProjectRevision || authority.session.Revision != out.value.SourceRevision && authority.session.ExecutionWorkspaceID != out.value.ID {
		return out, ErrRevision
	}
	err = tx.QueryRow(ctx, `SELECT COALESCE(claim_id::text,''),COALESCE(claim_grant_id::text,''),COALESCE(lease_until,'epoch'),COALESCE(prepare_deadline,'epoch'),token_state,installation_page,repository_page FROM ticket_workspaces WHERE id=$1 FOR UPDATE`, id).Scan(&out.claim, &out.grant, &out.until, &out.deadline, &out.tokenState, &out.installationPage, &out.repositoryPage)
	return out, err
}

func (g *GitStore) NextRunnerWorkspace(ctx context.Context, token string) (*Workspace, error) {
	state, err := g.store.RunnerAccount(ctx, token)
	if err != nil {
		return nil, err
	}
	if state.Action != "idle" {
		return nil, nil
	}
	var id string
	err = g.store.pool.QueryRow(ctx, `SELECT w.id FROM ticket_workspaces w
 JOIN sessions s ON s.id=w.session_id JOIN projects p ON p.id=w.project_id
 JOIN git_connections g ON g.id=w.git_connection_id
 JOIN memberships m ON m.camp_id=w.camp_id AND m.user_id=w.owner_user_id
 LEFT JOIN LATERAL (
 SELECT EXISTS(SELECT 1 FROM prompt_requests r WHERE r.execution_workspace_id=w.id
 AND r.execution_epoch=s.execution_epoch AND r.state IN ('claimed','started','cancel_requested','outcome_unknown')) AS active,
 (SELECT r.created_at FROM prompt_requests r WHERE r.execution_workspace_id=w.id
 AND r.execution_epoch=s.execution_epoch AND r.state='queued' AND `+eligiblePlanningRequestSQL+`
 ORDER BY r.created_at,r.id LIMIT 1) AS queued_at
 ) requests ON true
 WHERE w.provider_installation_id=$1 AND w.provider_epoch=$2 AND w.state IN ('queued','preparing','ready')
 AND `+workspaceSourceCurrentSQL+` AND s.project_id=w.project_id AND p.authority_revision=w.project_revision AND NOT p.archived
 AND g.state='connected' AND g.epoch=w.git_epoch AND m.role<>'viewer'
 AND `+validWorkspaceTransferSQL+`
 AND NOT EXISTS(SELECT 1 FROM transfer_handoffs h JOIN session_transfers t ON t.id=h.transfer_id LEFT JOIN ticket_workspaces w ON w.id=h.workspace_id WHERE h.session_id=s.id AND `+liveHandoffSQL+`)
 ORDER BY CASE WHEN requests.active THEN 0 WHEN w.state='preparing' THEN 1
 WHEN w.state='queued' OR requests.queued_at IS NOT NULL THEN 2 ELSE 3 END,
 COALESCE(requests.queued_at,w.created_at),w.id LIMIT 1`, state.InstallationID, state.Epoch).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	lease, err := g.lockRunnerWorkspace(ctx, tx, token, id)
	if err != nil {
		return nil, err
	}
	return &lease.value, nil
}

// Status lets a claimant reconcile a lost response without minting another
// credential or adopting work owned by a different grant.
func (g *GitStore) RunnerWorkspaceStatus(ctx context.Context, token, id, claim string) (Workspace, error) {
	if !validID(claim) {
		return Workspace{}, ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(ctx)
	lease, err := g.lockRunnerWorkspace(ctx, tx, token, id)
	if err != nil {
		return Workspace{}, err
	}
	if lease.claim != claim || lease.grant != lease.runner.grantID {
		return Workspace{}, ErrDenied
	}
	return lease.value, nil
}

func (g *GitStore) ClaimRunnerWorkspace(ctx context.Context, token, id, claim string) (RunnerWorkspaceClaim, error) {
	var out RunnerWorkspaceClaim
	if !validID(claim) {
		return out, ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	lease, err := g.lockRunnerWorkspace(ctx, tx, token, id)
	if err != nil {
		return out, err
	}
	if lease.value.State == "preparing" {
		if lease.claim != claim || lease.grant != lease.runner.grantID || !lease.until.After(time.Now()) || !lease.deadline.After(time.Now()) {
			return out, ErrTransition
		}
	} else if lease.value.State == "queued" {
		var busy bool
		busy, err = installationOccupied(ctx, tx, lease.value.ProviderInstallationID)
		if err != nil {
			return out, err
		}
		if !busy {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provider_login_attempts WHERE installation_id=$1 AND state IN ('queued','running','waiting') AND expires_at>clock_timestamp())`, lease.value.ProviderInstallationID).Scan(&busy)
			if err != nil {
				return out, err
			}
		}
		if busy {
			return out, ErrTransition
		}
		err = tx.QueryRow(ctx, `UPDATE ticket_workspaces SET state='preparing',claim_id=$2,claim_grant_id=$3,lease_until=clock_timestamp()+interval '90 seconds',prepare_deadline=clock_timestamp()+interval '10 minutes',revision=revision+1,updated_at=now() WHERE id=$1 RETURNING lease_until,prepare_deadline`, id, claim, lease.runner.grantID).Scan(&lease.until, &lease.deadline)
		if err != nil {
			return out, err
		}
		lease.value, err = readWorkspace(ctx, tx, auth.Actor{UserID: lease.value.OwnerUserID}, lease.value.CampID, id)
		if err != nil {
			return out, err
		}
		if err = g.emitWorkspace(ctx, tx, auth.Actor{UserID: lease.value.OwnerUserID}, lease.value); err != nil {
			return out, err
		}
	} else {
		return out, ErrTransition
	}
	out = RunnerWorkspaceClaim{Workspace: lease.value, ClaimID: claim, LeaseUntil: lease.until, Deadline: lease.deadline}
	return out, tx.Commit(ctx)
}

func (g *GitStore) HeartbeatRunnerWorkspace(ctx context.Context, token, id, claim string) (RunnerWorkspaceClaim, error) {
	var out RunnerWorkspaceClaim
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	lease, err := g.lockRunnerWorkspace(ctx, tx, token, id)
	if err != nil {
		return out, err
	}
	if !lease.active(claim) {
		return out, ErrTransition
	}
	err = tx.QueryRow(ctx, `UPDATE ticket_workspaces SET lease_until=LEAST(prepare_deadline,clock_timestamp()+interval '90 seconds') WHERE id=$1 RETURNING lease_until`, id).Scan(&lease.until)
	if err != nil {
		return out, err
	}
	out = RunnerWorkspaceClaim{Workspace: lease.value, ClaimID: claim, LeaseUntil: lease.until, Deadline: lease.deadline}
	return out, tx.Commit(ctx)
}

func (l workspaceLease) active(claim string) bool {
	return validID(claim) && l.value.State == "preparing" && l.claim == claim && l.grant == l.runner.grantID && l.until.After(time.Now()) && l.deadline.After(time.Now())
}

func (g *GitStore) ReportRunnerWorkspace(ctx context.Context, token, id, claim string, input WorkspaceReport) (Workspace, error) {
	if input.State != "ready" && input.State != "failed" {
		return Workspace{}, ErrInput
	}
	if input.State == "ready" && input.FailureCode != "" || input.State == "failed" && (!oneOf(input.FailureCode, "checkout_failed", "checkout_unsafe", "checkout_too_large", "checkout_cancelled") || input.BaseCommit != "" || input.Branch != "" || input.TransferReceipt != nil) {
		return Workspace{}, ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(ctx)
	lease, err := g.lockRunnerWorkspace(ctx, tx, token, id)
	if err != nil {
		return Workspace{}, err
	}
	if lease.claim != claim || lease.grant != lease.runner.grantID {
		return Workspace{}, ErrDenied
	}
	if input.State == "ready" && (input.BaseCommit != lease.value.BaseCommit || input.Branch != lease.value.Branch) {
		return Workspace{}, ErrInput
	}
	if lease.transfer == nil && input.TransferReceipt != nil {
		return Workspace{}, ErrInput
	}
	if lease.transfer != nil && input.State == "ready" {
		expected := transfer.StageReceipt{ReviewSHA256: lease.transfer.ReviewSHA256, BaseCommit: lease.transfer.BaseCommit, Branch: lease.value.Branch, Files: lease.transfer.FileCount}
		if input.TransferReceipt == nil || *input.TransferReceipt != expected {
			return Workspace{}, ErrInput
		}
	}
	raw, _ := json.Marshal(input)
	hash := sha256.Sum256(raw)
	if oneOf(lease.value.State, "ready", "failed") {
		var stored []byte
		if err = tx.QueryRow(ctx, `SELECT result_hash FROM ticket_workspaces WHERE id=$1`, id).Scan(&stored); err != nil {
			return Workspace{}, err
		}
		if subtle.ConstantTimeCompare(stored, hash[:]) != 1 {
			return Workspace{}, ErrConflict
		}
		return lease.value, nil
	}
	if !lease.active(claim) {
		return Workspace{}, ErrTransition
	}
	if input.State == "ready" && lease.tokenState != "ready" {
		return Workspace{}, ErrTransition
	}
	if err = retireWorkspaceToken(ctx, tx, id); err != nil {
		return Workspace{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE ticket_workspaces SET state=$2,failure_code=$3,result_hash=$4,revision=revision+1,updated_at=now() WHERE id=$1`, id, input.State, input.FailureCode, hash[:])
	if err != nil {
		return Workspace{}, err
	}
	out, err := readWorkspace(ctx, tx, auth.Actor{UserID: lease.value.OwnerUserID}, lease.value.CampID, id)
	if err != nil {
		return out, err
	}
	if err = g.emitWorkspace(ctx, tx, auth.Actor{UserID: out.OwnerUserID}, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func retireWorkspaceToken(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `UPDATE git_revocations SET not_before=LEAST(not_before,now()) WHERE id=(SELECT read_revocation_id FROM ticket_workspaces WHERE id=$1)`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE ticket_workspaces SET encrypted_read_token=NULL,token_state='closed' WHERE id=$1`, id)
	return err
}
