package camp

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// ReadContext returns only the explicitly selected messages at one immutable
// transcript revision. Access and permission revision are checked on every read.
func (s *Store) ReadContext(ctx context.Context, a auth.Actor, campID, id string, input SourceRef) (Transcript, error) {
	return retryRead(ctx, func() (Transcript, error) { return s.readContextOnce(ctx, a, campID, id, input) })
}
func (s *Store) readContextOnce(ctx context.Context, a auth.Actor, campID, id string, input SourceRef) (Transcript, error) {
	return s.readContextBudget(ctx, a, campID, id, input, 256<<10)
}

// ReadMessage opens one cited message in the native transcript inspector.
// Selected model context retains its smaller aggregate budget.
func (s *Store) ReadMessage(ctx context.Context, a auth.Actor, campID, id string, input SourceRef) (Transcript, error) {
	if len(input.MessageIDs) != 1 {
		return Transcript{}, ErrInput
	}
	return retryRead(ctx, func() (Transcript, error) { return s.readContextBudget(ctx, a, campID, id, input, 1<<20) })
}
func (s *Store) readContextBudget(ctx context.Context, a auth.Actor, campID, id string, input SourceRef, budget int) (Transcript, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Transcript{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return Transcript{}, err
	}
	session, err := s.readSession(ctx, tx, a, campID, id)
	if err != nil {
		return Transcript{}, err
	}
	if input.PermissionRevision != session.PermissionRevision {
		return Transcript{}, ErrDenied
	}
	if input.TranscriptRevision < 1 || input.TranscriptRevision > session.TranscriptRevision {
		return Transcript{}, ErrRevision
	}
	input.SessionID = id
	messages, err := readContextMessagesBudget(ctx, tx, input, budget)
	if err != nil {
		return Transcript{}, err
	}
	return Transcript{Revision: input.TranscriptRevision, Messages: messages}, tx.Commit(ctx)
}

func readContextMessages(ctx context.Context, tx pgx.Tx, ref SourceRef) ([]Message, error) {
	return readContextMessagesBudget(ctx, tx, ref, 256<<10)
}
func readContextMessagesBudget(ctx context.Context, tx pgx.Tx, ref SourceRef, budget int) ([]Message, error) {
	if len(ref.MessageIDs) < 1 || len(ref.MessageIDs) > 120 {
		return nil, ErrInput
	}
	seen := make(map[string]bool, len(ref.MessageIDs))
	for _, id := range ref.MessageIDs {
		if id == "" || len(id) > 256 || seen[id] {
			return nil, ErrInput
		}
		seen[id] = true
	}
	rows, err := tx.Query(ctx, `SELECT m.local_id,m.ordinal,v.role,v.text,v.removed,v.revision,v.model,v.effort,v.speed,v.attachments FROM messages m
      CROSS JOIN LATERAL(SELECT role,text,removed,revision,model,effort,speed,attachments FROM message_revisions
        WHERE session_id=m.session_id AND local_id=m.local_id AND revision<=$2 ORDER BY revision DESC LIMIT 1) v
      WHERE m.session_id=$1 AND m.local_id=ANY($3::text[]) AND NOT v.removed ORDER BY m.ordinal`, ref.SessionID, ref.TranscriptRevision, ref.MessageIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]Message, 0, len(ref.MessageIDs))
	size := 0
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.Ordinal, &m.Role, &m.Text, &m.Removed, &m.Revision, &m.Model, &m.Effort, &m.Speed, &m.Attachments); err != nil {
			return nil, err
		}
		size += len(m.Text)
		if len(m.Attachments) > 0 {
			raw, err := json.Marshal(m.Attachments)
			if err != nil {
				return nil, err
			}
			size += len(raw)
		}
		if size > budget {
			return nil, ErrInput
		}
		messages = append(messages, m)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(messages) != len(ref.MessageIDs) {
		return nil, ErrRevision
	}
	return messages, nil
}
