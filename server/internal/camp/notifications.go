package camp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

type Notification struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	SessionID   string    `json:"session_id,omitempty"`
	RequestID   string    `json:"request_id,omitempty"`
	PlanID      string    `json:"plan_id,omitempty"`
	PlanVersion int64     `json:"plan_version,omitempty"`
	Revision    int64     `json:"revision"`
	Read        bool      `json:"read"`
	CreatedAt   time.Time `json:"created_at"`
}
type NotificationPage struct {
	Notifications []Notification `json:"notifications"`
	Next          string         `json:"next"`
	Unread        int            `json:"unread"`
}
type NotificationAction struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
}

// A recipient must still be a member and able to read the destination. For a
// plan, its pinned participant/source sessions must also remain accessible.
const notificationAccess = `(n.session_id IS NOT NULL AND (s.shared OR s.owner_user_id=$1)) OR
 (n.plan_id IS NOT NULL AND (p.shared OR p.creator_user_id=$1 OR EXISTS(
   SELECT 1 FROM planning_participants x WHERE x.plan_id=p.id AND x.version=p.active_version AND x.owner_user_id=$1))
  AND NOT EXISTS(SELECT 1 FROM planning_participants x JOIN sessions z ON z.id=x.session_id
    WHERE x.plan_id=n.plan_id AND x.version=n.plan_version AND NOT(z.shared OR z.owner_user_id=$1))
  AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(v.sources) source
    WHERE NOT EXISTS(SELECT 1 FROM sessions z WHERE z.id=(source->>'session_id')::uuid AND z.camp_id=n.camp_id AND (z.shared OR z.owner_user_id=$1))))`
const notificationFrom = ` FROM notifications n
 JOIN memberships m ON m.camp_id=n.camp_id AND m.user_id=n.recipient_user_id
 LEFT JOIN sessions s ON s.id=n.session_id
 LEFT JOIN planning_tickets p ON p.id=n.plan_id
 LEFT JOIN planning_versions v ON v.plan_id=n.plan_id AND v.version=n.plan_version
 WHERE n.recipient_user_id=$1 AND n.camp_id=$2 AND n.created_at>now()-interval '30 days'
 AND (` + notificationAccess + `)`
const notificationColumns = `n.id,n.kind,COALESCE(s.title,p.title),COALESCE(n.session_id::text,''),COALESCE(n.request_id::text,''),COALESCE(n.plan_id::text,''),COALESCE(n.plan_version,0),n.revision,n.read_at IS NOT NULL,n.created_at`

func scanNotification(row rowScanner) (Notification, error) {
	var n Notification
	err := row.Scan(&n.ID, &n.Kind, &n.Title, &n.SessionID, &n.RequestID, &n.PlanID, &n.PlanVersion, &n.Revision, &n.Read, &n.CreatedAt)
	return n, err
}
func (s *Store) Notifications(ctx context.Context, a auth.Actor, campID, before string, limit int) (NotificationPage, error) {
	return retryRead(ctx, func() (NotificationPage, error) { return s.notificationsOnce(ctx, a, campID, before, limit) })
}
func (s *Store) notificationsOnce(ctx context.Context, a auth.Actor, campID, before string, limit int) (NotificationPage, error) {
	out := NotificationPage{Notifications: []Notification{}}
	if limit < 1 || limit > 100 || before != "" && !validID(before) {
		return out, ErrInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return out, err
	}
	var cursorTime time.Time
	if before != "" {
		err = tx.QueryRow(ctx, `SELECT n.created_at`+notificationFrom+` AND n.id=$3`, a.UserID, campID, before).Scan(&cursorTime)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, ErrCursor
		}
		if err != nil {
			return out, err
		}
	}
	err = tx.QueryRow(ctx, `SELECT count(*)`+notificationFrom+` AND NOT n.archived AND n.read_at IS NULL`, a.UserID, campID).Scan(&out.Unread)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT `+notificationColumns+notificationFrom+` AND NOT n.archived
      AND ($3::uuid IS NULL OR (n.created_at,n.id)<($4::timestamptz,$3::uuid)) ORDER BY n.created_at DESC,n.id DESC LIMIT $5`, a.UserID, campID, nullableID(before), cursorTime, limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		n, e := scanNotification(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Notifications = append(out.Notifications, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Notifications) > limit {
		out.Notifications = out.Notifications[:limit]
		out.Next = out.Notifications[limit-1].ID
	}
	return out, tx.Commit(ctx)
}
func nullableID(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func (s *Store) Notification(ctx context.Context, a auth.Actor, campID, id string) (Notification, error) {
	if !validID(campID) || !validID(id) {
		return Notification{}, ErrDenied
	}
	n, err := scanNotification(s.pool.QueryRow(ctx, `SELECT `+notificationColumns+notificationFrom+` AND n.id=$3 AND NOT n.archived`, a.UserID, campID, id))
	return n, accessError(err)
}
func (s *Store) ActOnNotifications(ctx context.Context, a auth.Actor, campID, key string, input NotificationAction) error {
	if len(input.IDs) == 0 || len(input.IDs) > 1000 || !oneOf(input.Action, "read", "archive") {
		return ErrInput
	}
	ids := map[string]bool{}
	for _, id := range input.IDs {
		if !validID(id) || ids[id] {
			return ErrInput
		}
		ids[id] = true
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return err
	}
	// Check current access before replaying a receipt as well as before a write.
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*)`+notificationFrom+` AND n.id=ANY($3::uuid[])`, a.UserID, campID, input.IDs).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return ErrDenied
	}
	if hit, e := s.beginCommand(ctx, tx, a, key, "notification.action:"+campID, input, &struct{}{}); e != nil {
		return e
	} else if hit {
		return nil
	}
	if input.Action == "archive" {
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE id=ANY($1::uuid[]) AND read_at IS NULL`, input.IDs).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrTransition
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE notifications SET read_at=COALESCE(read_at,now()),archived=archived OR $4,revision=revision+1,updated_at=now()
       WHERE camp_id=$1 AND recipient_user_id=$2 AND id=ANY($3::uuid[])
       AND (read_at IS NULL OR ($4 AND NOT archived))`, campID, a.UserID, input.IDs, input.Action == "archive")
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if err = emitNotification(ctx, tx, a, campID, a.UserID); err != nil {
			return err
		}
	}
	if err = s.finishCommand(ctx, tx, a, key, struct{}{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func emitNotification(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, recipient string) error {
	if err := emit(ctx, tx, a, campID, "notification.changed", map[string]bool{"notifications_changed": true}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE events SET recipient_user_id=$2 WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, campID, recipient)
	return err
}
func upsertNotification(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, recipient, key, kind, sessionID, requestID, planID string, version int64) error {
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("camp-notification-v1:"+campID+":"+recipient+":"+key)).String()
	tag, err := tx.Exec(ctx, `INSERT INTO notifications(id,camp_id,recipient_user_id,completion_key,kind,session_id,request_id,plan_id,plan_version)
      SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9 WHERE EXISTS(SELECT 1 FROM memberships WHERE camp_id=$2 AND user_id=$3)
      ON CONFLICT(camp_id,recipient_user_id,completion_key) DO UPDATE SET kind=EXCLUDED.kind,revision=notifications.revision+1,updated_at=now()
      WHERE notifications.kind<>EXCLUDED.kind`, id, campID, recipient, key, kind, nullableID(sessionID), nullableID(requestID), nullableID(planID), nullableVersion(version))
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	// The Camp lock serializes retention and insertion for this recipient.
	_, err = tx.Exec(ctx, `DELETE FROM notifications WHERE camp_id=$1 AND recipient_user_id=$2 AND (created_at<=now()-interval '30 days' OR id IN(
      SELECT id FROM notifications WHERE camp_id=$1 AND recipient_user_id=$2 ORDER BY created_at DESC,id DESC OFFSET 1000))`, campID, recipient)
	if err != nil {
		return err
	}
	return emitNotification(ctx, tx, a, campID, recipient)
}
func nullableVersion(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}
func notifyRequest(ctx context.Context, tx pgx.Tx, a auth.Actor, r Request) error {
	if !oneOf(r.State, "completed", "failed", "outcome_unknown") {
		return nil
	}
	if r.Kind == "planning" {
		if r.State == "outcome_unknown" {
			return notifyPlan(ctx, tx, a, r.CampID, r.PlanID, r.PlanVersion, "attention")
		}
		return nil
	}
	var owner, requester string
	if err := tx.QueryRow(ctx, `SELECT owner_user_id,requester_user_id FROM prompt_requests WHERE id=$1`, r.ID).Scan(&owner, &requester); err != nil {
		return err
	}
	kind := "attention"
	if r.State == "completed" {
		kind = "finished"
	}
	for i, recipient := range []string{owner, requester} {
		if i == 1 && requester == owner {
			continue
		}
		if err := upsertNotification(ctx, tx, a, r.CampID, recipient, "request:"+r.ID, kind, r.SessionID, r.ID, "", 0); err != nil {
			return err
		}
	}
	return nil
}
func notifyPlan(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, id string, version int64, kind string) error {
	rows, err := tx.Query(ctx, `SELECT creator_user_id FROM planning_tickets WHERE id=$1 UNION SELECT owner_user_id FROM planning_participants WHERE plan_id=$1 AND version=$2`, id, version)
	if err != nil {
		return err
	}
	var recipients []string
	for rows.Next() {
		var recipient string
		if err = rows.Scan(&recipient); err != nil {
			rows.Close()
			return err
		}
		recipients = append(recipients, recipient)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, recipient := range recipients {
		if err = upsertNotification(ctx, tx, a, campID, recipient, fmt.Sprintf("plan:%s:%d", id, version), kind, "", "", id, version); err != nil {
			return err
		}
	}
	return nil
}
