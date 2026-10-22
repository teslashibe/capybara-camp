package camp

import (
	"context"
	"errors"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

type HandoffRecovery struct {
	Handoff Handoff `json:"handoff"`
	Session Session `json:"session"`
}

// RecoverHandoffs lets a reauthenticated owner reconcile opaque local markers.
// It never grants source execution or changes an assignment. Foreign IDs are
// omitted, so sharing a Camp does not disclose another owner's transfers.
func (s *Store) RecoverHandoffs(ctx context.Context, a auth.Actor, campID string, ids []string) ([]HandoffRecovery, error) {
	if len(ids) == 0 || len(ids) > 32 {
		return nil, ErrInput
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validID(id) || seen[id] {
			return nil, ErrInput
		}
		seen[id] = true
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM transfer_handoffs WHERE id=ANY($1::uuid[]) AND camp_id=$2 AND owner_user_id=$3 ORDER BY id`, ids, campID, a.UserID)
	if err != nil {
		return nil, err
	}
	allowed := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		allowed = append(allowed, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []HandoffRecovery{}
	for _, id := range allowed {
		h, e := readHandoff(ctx, tx, id, a.UserID)
		if errors.Is(e, ErrDenied) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if h.CampID != campID || h.SourceLocation != "local" {
			continue
		}
		session, e := s.readSession(ctx, tx, a, campID, h.SessionID)
		if e != nil {
			return nil, e
		}
		h, e = s.refreshHandoff(ctx, tx, a, h)
		if e != nil {
			return nil, e
		}
		out = append(out, HandoffRecovery{Handoff: h, Session: session})
	}
	return out, tx.Commit(ctx)
}
