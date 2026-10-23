package camp

import (
	"context"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// Planning sources expose only the caller's prepared cloud assignments. Team
// membership does not expose another person's provider account or Git binding.
type PlanningSource struct {
	Session        Session `json:"session"`
	AccountID      string  `json:"account_id"`
	RepositoryName string  `json:"repository_name"`
}

func (s *Store) PlanningSources(ctx context.Context, a auth.Actor, campID, projectID, accountID string) ([]PlanningSource, error) {
	if !validID(projectID) || accountID != "" && !validID(accountID) {
		return nil, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+sessionColumns+`,w.provider_account_id,w.repository_name
 FROM sessions s JOIN profiles p ON p.id=s.profile_id
 JOIN ticket_workspaces w ON w.id=s.execution_workspace_id AND w.session_id=s.id AND w.camp_id=s.camp_id AND w.owner_user_id=s.owner_user_id AND w.project_id=s.project_id
 JOIN projects j ON j.id=w.project_id
 JOIN provider_accounts a ON a.id=w.provider_account_id AND a.owner_user_id=s.owner_user_id AND a.provider=p.provider
 JOIN provider_installations i ON i.id=w.provider_installation_id AND i.account_id=a.id AND i.owner_user_id=s.owner_user_id
 JOIN git_connections g ON g.id=w.git_connection_id AND g.owner_user_id=s.owner_user_id
 WHERE s.camp_id=$1 AND s.project_id=$2 AND s.owner_user_id=$3 AND s.execution_location='cloud' AND ($4='' OR w.provider_account_id=NULLIF($4,'')::uuid)
 AND w.state='ready' AND w.project_revision=j.revision AND NOT j.archived
 AND i.state='connected' AND i.epoch=w.provider_epoch AND (i.credential_expires_at IS NULL OR i.credential_expires_at>clock_timestamp())
 AND g.state='connected' AND g.epoch=w.git_epoch
 ORDER BY s.updated_at DESC,s.id LIMIT 100`, campID, projectID, a.UserID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlanningSource{}
	for rows.Next() {
		var source PlanningSource
		source.Session, err = scanSession(rows, &source.AccountID, &source.RepositoryName)
		if err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
