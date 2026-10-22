package camp

import (
	"context"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func (s *Store) MoveSession(ctx context.Context, a auth.Actor, campID, id, key, stage string, revision int64) (Session, error) {
	if !oneOf(stage, "Ready", "In progress", "Needs you", "Done") || revision < 1 {
		return Session{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return Session{}, err
	}
	current, err := s.readSession(ctx, tx, a, campID, id)
	if err != nil {
		return current, err
	}
	if current.HistoryOnly {
		return current, ErrTransition
	}
	var result Session
	input := struct {
		Stage    string
		Revision int64
	}{stage, revision}
	if hit, err := s.beginCommand(ctx, tx, a, key, "session.board:"+id, input, &result); err != nil {
		return result, err
	} else if hit {
		return current, nil
	}
	if current.Revision != revision {
		return current, ErrRevision
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET stage=$2,revision=revision+1,updated_at=now() WHERE id=$1`, id, stage); err != nil {
		return current, err
	}
	result, err = s.readSession(ctx, tx, a, campID, id)
	if err != nil {
		return result, err
	}
	if err = emitSession(ctx, tx, a, campID, "session.updated", result, false); err != nil {
		return result, err
	}
	if err = s.finishCommand(ctx, tx, a, key, result); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
