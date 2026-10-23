package camp

import (
	"context"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// ActivityEntry says who did what to a session and when. It carries no
// prompts, transcript text or other payload content.
type ActivityEntry struct {
	Cursor      int64     `json:"cursor"`
	Kind        string    `json:"kind"`
	ActorUserID string    `json:"actor_user_id"`
	State       string    `json:"state,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Automatic   bool      `json:"automatic,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// SessionActivity lists the latest attributed events for a session the caller
// can read, newest first. before pages backwards by event cursor.
func (s *Store) SessionActivity(ctx context.Context, a auth.Actor, campID, id string, before int64) ([]ActivityEntry, error) {
	if before < 0 {
		return nil, ErrInput
	}
	if before == 0 {
		before = 1 << 62
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.readSession(ctx, tx, a, campID, id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT e.cursor,e.kind,e.actor_id,COALESCE(e.payload->>'state',''),COALESCE(e.payload->>'reason',''),COALESCE((e.payload->>'automatic')::boolean,false),e.created_at
	 FROM events e WHERE e.camp_id=$1 AND e.session_id=$2 AND e.cursor<$3
	 AND (e.recipient_user_id IS NULL OR e.recipient_user_id=$4)
	 AND e.kind NOT LIKE 'memory.%' AND e.kind NOT IN ('transcript.updated','children.updated','notification.changed')
	 ORDER BY e.cursor DESC LIMIT 50`, campID, id, before, a.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ActivityEntry{}
	for rows.Next() {
		var v ActivityEntry
		if err = rows.Scan(&v.Cursor, &v.Kind, &v.ActorUserID, &v.State, &v.Reason, &v.Automatic, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
