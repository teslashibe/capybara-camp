package camp

import (
	"context"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

type OwnershipInput struct {
	ProfileID        string `json:"profile_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

// AdoptSession is an explicit action by the same human owner on a new device.
// In-flight or uncertain provider work must be resolved on its original device.
func (s *Store) AdoptSession(ctx context.Context, a auth.Actor, campID, id, key string, input OwnershipInput) (Session, error) {
	if !validID(input.ProfileID) || input.ExpectedRevision < 1 {
		return Session{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockWorker(ctx, tx, a); err != nil {
		return Session{}, err
	}
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return Session{}, err
	}
	current, err := s.readSession(ctx, tx, a, campID, id)
	if err != nil {
		return current, err
	}
	if current.OwnerUserID != a.UserID {
		return Session{}, ErrDenied
	}
	if current.HistoryOnly {
		return Session{}, ErrTransition
	}
	var result Session
	if hit, err := s.beginCommand(ctx, tx, a, key, "session.adopt:"+id, input, &result); err != nil {
		return result, err
	} else if hit {
		return current, nil
	}
	if current.Revision != input.ExpectedRevision {
		return current, ErrRevision
	}
	var profileAllowed, active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM profiles WHERE id=$1 AND owner_user_id=$2 AND owner_device_id=$3 AND provider=$4),
 EXISTS(SELECT 1 FROM prompt_requests WHERE session_id=$5 AND state IN ('claimed','started','cancel_requested','outcome_unknown'))`, input.ProfileID, a.UserID, a.DeviceID, current.Provider, id).Scan(&profileAllowed, &active); err != nil {
		return current, err
	}
	if !profileAllowed {
		return current, ErrDenied
	}
	// A cloud session is run by its assigned runner. Adoption would set an
	// external owner that no cloud path can clear.
	if active || current.ExternalOwner || current.State == "running" || current.ExecutionLocation == "cloud" {
		return current, ErrTransition
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET profile_id=$2,owner_device_id=$3,revision=revision+1,permission_revision=permission_revision+1,external_owner=true,updated_at=now() WHERE id=$1`, id, input.ProfileID, a.DeviceID); err != nil {
		return current, err
	}
	if err = s.invalidateSessionRequests(ctx, tx, a, campID, id); err != nil {
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
