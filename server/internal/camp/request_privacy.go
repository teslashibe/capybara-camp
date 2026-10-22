package camp

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// Use the same predicate for list, detail and receipt reads. The actor is $1;
// r and s are the request and its target session. Computing visibility in SQL keeps the
// paged request list bounded without per-row queries or hiding lifecycle state.
const readableRequestContentSQL = `
 (s.shared OR s.owner_user_id=$1)
 AND NOT EXISTS(SELECT 1 FROM request_sources x JOIN sessions source ON source.id=x.session_id
   WHERE x.request_id=r.id AND (source.permission_revision<>x.permission_revision
     OR source.transcript_revision<x.transcript_revision OR NOT(source.shared OR source.owner_user_id=$1)))
 AND (r.kind<>'planning' OR EXISTS(
   SELECT 1 FROM planning_tickets p WHERE p.id=r.plan_id
   AND (p.shared OR p.creator_user_id=$1 OR EXISTS(SELECT 1 FROM planning_participants member
     WHERE member.plan_id=p.id AND member.version=p.active_version AND member.owner_user_id=$1))
   AND NOT EXISTS(SELECT 1 FROM planning_participants participant JOIN sessions source ON source.id=participant.session_id
     WHERE participant.plan_id=p.id AND participant.version=r.plan_version AND NOT(source.shared OR source.owner_user_id=$1))))`

// Read access is independent of current execution availability. A completed
// request remains readable when its runner is offline, but never restores a
// revoked context grant just because the target chat is still shared.
func requestContentAccess(ctx context.Context, tx pgx.Tx, a auth.Actor, r Request) error {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM prompt_requests r JOIN sessions s ON s.id=r.session_id
 JOIN memberships m ON m.camp_id=r.camp_id AND m.user_id=$1 WHERE r.id=$2 AND `+readableRequestContentSQL+`)`, a.UserID, r.ID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrDenied
	}
	return nil
}

// Workers must still acknowledge cancellation, finish and reconcile after a
// source is revoked. Return lifecycle metadata without its frozen content.
func redactRequestContent(r Request) Request {
	r.ContentRestricted = true
	r.Prompt = ""
	r.Sources = []SourceRef{}
	r.QuotedSources = nil
	r.QuotedContextDigest = ""
	r.Evidence = ""
	return r
}
