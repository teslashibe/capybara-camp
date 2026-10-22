package camp

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// An installation grant is not a device login. Resolve and lock the assigned
// workspace before it can inspect a prompt or change a request's state.
func (g *GitStore) lockExecution(ctx context.Context, tx pgx.Tx, token, workspaceID string) (workspaceLease, Session, error) {
	l, err := g.lockRunnerWorkspace(ctx, tx, token, workspaceID)
	if err != nil {
		return l, Session{}, err
	}
	if l.value.State != "ready" {
		return l, Session{}, ErrTransition
	}
	var signingIn bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provider_login_attempts WHERE installation_id=$1 AND state IN ('queued','running','waiting') AND expires_at>clock_timestamp())`, l.value.ProviderInstallationID).Scan(&signingIn); err != nil {
		return l, Session{}, err
	}
	if signingIn {
		return l, Session{}, ErrTransition
	}
	session, err := g.store.readSession(ctx, tx, auth.Actor{UserID: l.value.OwnerUserID}, l.value.CampID, l.value.SessionID)
	if err != nil {
		return l, session, err
	}
	if session.ExecutionLocation != "cloud" || session.ExecutionWorkspaceID != workspaceID || session.ExternalOwner {
		return l, session, ErrDenied
	}
	return l, session, nil
}

func (g *GitStore) ClaimRunnerRequest(ctx context.Context, token, workspaceID, claim string) (*Request, error) {
	if !validID(claim) {
		return nil, ErrInput
	}
	s := g.store
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	l, session, err := g.lockExecution(ctx, tx, token, workspaceID)
	if err != nil {
		return nil, err
	}
	if err = requireUnheldSession(ctx, tx, session.ID); err != nil {
		return nil, err
	}
	if err = s.reapCamp(ctx, tx, l.value.CampID); err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id FROM prompt_requests WHERE runner_grant_id=$1 AND lease_token=$2`, l.runner.grantID, claim).Scan(&id)
	if err == nil {
		r, err := loadRequest(ctx, tx, l.value.CampID, id)
		if err != nil {
			return nil, err
		}
		if r.ExecutionWorkspaceID != workspaceID {
			return nil, ErrConflict
		}
		live, err := hasLiveLease(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !live || !oneOf(r.State, "claimed", "started", "cancel_requested") {
			return nil, ErrTransition
		}
		if _, err = s.requestAccess(ctx, tx, r); err != nil {
			return nil, err
		}
		return &r, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	busy, err := installationOccupied(ctx, tx, l.value.ProviderInstallationID)
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, tx.Commit(ctx)
	}
	err = tx.QueryRow(ctx, `SELECT r.id FROM prompt_requests r WHERE r.session_id=$1 AND r.execution_location='cloud' AND r.execution_workspace_id=$2 AND r.execution_epoch=$3 AND r.state='queued'
 AND `+eligiblePlanningRequestSQL+`
 AND NOT EXISTS(SELECT 1 FROM prompt_requests active WHERE active.session_id=r.session_id AND active.state IN ('claimed','started','cancel_requested','outcome_unknown'))
 ORDER BY r.created_at,r.id LIMIT 1 FOR UPDATE OF r`, session.ID, workspaceID, session.ExecutionEpoch).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	r, err := loadRequest(ctx, tx, l.value.CampID, id)
	if err != nil {
		return nil, err
	}
	if _, err = s.requestAccess(ctx, tx, r); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `UPDATE prompt_requests SET state='claimed',revision=revision+1,lease_token=$2,runner_grant_id=$3,lease_until=clock_timestamp()+interval '60 seconds' WHERE id=$1 RETURNING revision,lease_until`, id, claim, l.runner.grantID).Scan(&r.Revision, &r.LeaseUntil)
	if err != nil {
		return nil, err
	}
	r.State, r.LeaseToken, r.RunnerGrantID = "claimed", claim, l.runner.grantID
	if err = s.emitCloudRequest(ctx, tx, r, "request.claimed"); err != nil {
		return nil, err
	}
	return &r, tx.Commit(ctx)
}

func (s *Store) emitCloudRequest(ctx context.Context, tx pgx.Tx, r Request, kind string) error {
	return emitRequest(ctx, tx, auth.Actor{UserID: r.OwnerUserID}, r, kind, false)
}

func (g *GitStore) ActOnRunnerRequest(ctx context.Context, token, workspaceID, id, key string, input RequestAction) (Request, error) {
	if !oneOf(input.Action, "start", "heartbeat", "release", "finish", "reconcile") || len(input.Evidence) > 1024 || len(input.Output) > 8192 || !validPlanningUsage(input.Usage) {
		return Request{}, ErrInput
	}
	s := g.store
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	l, session, err := g.lockExecution(ctx, tx, token, workspaceID)
	if err != nil {
		return Request{}, err
	}
	if err = s.reapCamp(ctx, tx, l.value.CampID); err != nil {
		return Request{}, err
	}
	r, err := loadRequest(ctx, tx, l.value.CampID, id)
	if err != nil {
		return Request{}, err
	}
	if r.ExecutionWorkspaceID != workspaceID || r.ExecutionEpoch != session.ExecutionEpoch || r.ExecutionLocation != "cloud" || r.RunnerGrantID != l.runner.grantID {
		return Request{}, ErrDenied
	}
	return s.actOnRequest(ctx, tx, auth.Actor{UserID: r.OwnerUserID}, r.CampID, r.ID, key, input, r, true, "runner:"+l.runner.grantID+":request.action:"+id)
}
