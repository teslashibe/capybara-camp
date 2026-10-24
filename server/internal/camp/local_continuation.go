package camp

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// The committed return receipt identifies the exact local execution epoch.
// Ordinary imported sessions continue to publish their native transcripts.
const localContinuationSQL = `r.kind='chat' AND r.execution_location='local' AND EXISTS(
 SELECT 1 FROM transfer_handoffs h WHERE h.session_id=r.session_id AND h.execution_epoch=r.execution_epoch
 AND h.state='committed' AND h.local_profile_id IS NOT NULL AND h.owner_user_id=r.owner_user_id)`

func (s *Store) AppendLocalContinuationEvent(ctx context.Context, a auth.Actor, campID, id string, in LocalPlanningEvent) (int64, error) {
	event, raw, err := validStreamEvent(in.Event)
	if err != nil || in.Seq < 1 || !validID(in.LeaseToken) {
		return 0, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return 0, err
	}
	r, err := loadRequest(ctx, tx, campID, id)
	if err != nil {
		return 0, err
	}
	if !r.LocalContinuation || r.OwnerUserID != a.UserID || r.OwnerDeviceID != a.DeviceID ||
		subtle.ConstantTimeCompare([]byte(r.LeaseToken), []byte(in.LeaseToken)) != 1 {
		return 0, ErrDenied
	}
	seq, err := s.appendRequestEvent(ctx, tx, r, in.StreamEvent, event, raw, "")
	if err != nil {
		return 0, err
	}
	return seq, tx.Commit(ctx)
}

func saveLocalContinuationSession(ctx context.Context, tx pgx.Tx, r Request, id string) error {
	// Lock the owner as well as the Camp so another Camp cannot bind the same
	// physical CLI conversation concurrently through ordinary publication.
	var profile string
	if err := tx.QueryRow(ctx, `SELECT profile_id FROM sessions WHERE id=$1`, r.SessionID).Scan(&profile); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, r.OwnerUserID); err != nil {
		return err
	}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE profile_id=$1 AND local_session_id=$2 AND id<>$3)
        OR EXISTS(SELECT 1 FROM transfer_handoffs WHERE local_profile_id=$1 AND local_provider_session_id=$2 AND (session_id<>$3 OR execution_epoch<>$4))`,
		profile, id, r.SessionID, r.ExecutionEpoch).Scan(&used); err != nil {
		return err
	}
	if used {
		return ErrConflict
	}
	var saved string
	err := tx.QueryRow(ctx, `UPDATE transfer_handoffs SET local_provider_session_id=$3
        WHERE session_id=$1 AND execution_epoch=$2 AND state='committed' AND local_profile_id=$4
        AND (local_provider_session_id IS NULL OR local_provider_session_id=$3) RETURNING local_provider_session_id`,
		r.SessionID, r.ExecutionEpoch, id, profile).Scan(&saved)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	return err
}
