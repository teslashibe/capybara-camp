package camp

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func validLocalDestination(d LocalHandoffDestination) bool {
	id, err := uuid.Parse(d.SessionID)
	return validID(d.ProfileID) && err == nil && id != uuid.Nil && id.String() == d.SessionID &&
		oneOf(d.Purpose, "planning", "implementation") && digest64(d.ReceiptSHA256) && d.TranscriptRevision >= 0
}

func (s *Store) validateLocalHandoff(ctx context.Context, tx pgx.Tx, session Session, t Transfer, h Handoff) error {
	d := h.LocalDestination
	if d == nil || !validLocalDestination(*d) || t.SourceLocation != "cloud" || h.WorkspaceID != "" ||
		d.TranscriptRevision != t.SourceTranscriptRevision || h.TargetDeviceID != t.TargetDeviceID || t.BaseCommit == "" {
		return ErrRevision
	}
	var profile string
	err := tx.QueryRow(ctx, `SELECT p.id FROM profiles p JOIN devices d ON d.id=p.owner_device_id
 WHERE p.id=$1 AND p.owner_user_id=$2 AND p.owner_device_id=$3 AND p.provider=$4
 AND d.user_id=$2 AND d.revoked_at IS NULL AND d.expires_at>clock_timestamp() FOR SHARE OF p,d`, d.ProfileID, h.OwnerUserID, t.TargetDeviceID, session.Provider).Scan(&profile)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	// Reject a mapping already used by any ticket, including the source.
	// An existing provider conversation is never silently replaced.
	var occupied bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE profile_id=$1 AND local_session_id=$2)`, d.ProfileID, d.SessionID).Scan(&occupied); err != nil {
		return err
	}
	if occupied {
		return ErrConflict
	}
	_, err = s.executionTarget(ctx, tx, session)
	return err
}

func (s *Store) handoffPurpose(ctx context.Context, tx pgx.Tx, a auth.Actor, h Handoff) (string, error) {
	if h.TargetLocation == "local" {
		if h.LocalDestination == nil {
			return "", ErrInput
		}
		return h.LocalDestination.Purpose, nil
	}
	w, err := readWorkspace(ctx, tx, a, h.CampID, h.WorkspaceID)
	return w.Purpose, err
}
