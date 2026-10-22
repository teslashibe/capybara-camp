package camp

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Call while holding the provider account lock. Prepared checkouts do not
// occupy the worker; a preparation, claimed turn or uncertain turn does.
func installationOccupied(ctx context.Context, tx pgx.Tx, installation string) (bool, error) {
	var busy bool
	err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM ticket_workspaces WHERE provider_installation_id=$1 AND state='preparing') OR
 EXISTS(SELECT 1 FROM prompt_requests r JOIN ticket_workspaces w ON w.id=r.execution_workspace_id
 WHERE w.provider_installation_id=$1 AND r.state IN ('claimed','started','cancel_requested','outcome_unknown'))`, installation).Scan(&busy)
	return busy, err
}

// RetiredRunnerWorkspaces checks only the supplied local checkout IDs. A
// missing/terminal row cannot become active again. Never infer retirement
// from a failed request, a partial listing or a different account's state.
func (g *GitStore) RetiredRunnerWorkspaces(ctx context.Context, token string, ids []string) ([]string, error) {
	if err := g.store.cloudEnabled(); err != nil {
		return nil, err
	}
	if len(ids) == 0 || len(ids) > 64 {
		return nil, ErrInput
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validID(id) || seen[id] {
			return nil, ErrInput
		}
		seen[id] = true
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	r, err := g.store.lockRunner(ctx, tx, token)
	if err != nil {
		return nil, err
	}
	if r.cleanup {
		return nil, ErrDenied
	}
	rows, err := tx.Query(ctx, `SELECT id FROM ticket_workspaces WHERE provider_installation_id=$1 AND provider_epoch=$2 AND id=ANY($3::uuid[]) AND state IN ('queued','preparing','ready')`, r.account.InstallationID, r.account.Epoch, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		delete(seen, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	retired := []string{}
	for _, id := range ids {
		if seen[id] {
			retired = append(retired, id)
		}
	}
	return retired, tx.Commit(ctx)
}
