package camp

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func validateTranscriptMessages(messages []MessageInput, allowEmpty bool) error {
	if len(messages) > 200 || !allowEmpty && len(messages) == 0 {
		return ErrInput
	}
	size := 0
	seen := map[string]bool{}
	positions := map[int64]bool{}
	for _, m := range messages {
		size += len(m.Text)
		if len(m.ID) == 0 || len(m.ID) > 256 || len(m.Model) > 200 || len(m.Effort) > 32 || len(m.Speed) > 32 || m.Ordinal < 0 || seen[m.ID] || positions[m.Ordinal] || !oneOf(m.Role, "user", "assistant", "system", "tool") {
			return ErrInput
		}
		seen[m.ID] = true
		positions[m.Ordinal] = true
	}
	if size > 1<<20 {
		return ErrInput
	}
	return nil
}

func appendTranscriptMessages(ctx context.Context, tx pgx.Tx, id string, revision int64, messages []MessageInput) error {
	var err error
	for _, m := range messages {
		var ordinal int64
		err = tx.QueryRow(ctx, `INSERT INTO messages(session_id,local_id,ordinal) VALUES($1,$2,$3) ON CONFLICT(session_id,local_id) DO UPDATE SET local_id=EXCLUDED.local_id RETURNING ordinal`, id, m.ID, m.Ordinal).Scan(&ordinal)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrConflict
			}
			return err
		}
		if ordinal != m.Ordinal {
			return ErrConflict
		}
		if _, err = tx.Exec(ctx, `INSERT INTO message_revisions(session_id,local_id,revision,role,text,removed,model,effort,speed) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, m.ID, revision, m.Role, m.Text, m.Removed, m.Model, m.Effort, m.Speed); err != nil {
			return err
		}
	}
	return nil
}

func readTranscriptAt(ctx context.Context, tx pgx.Tx, id string, revision, before int64, limit int) (Transcript, error) {
	if before == 0 {
		before = 1 << 62
	}
	rows, err := tx.Query(ctx, `SELECT m.local_id,m.ordinal,v.role,v.text,v.removed,v.revision,v.model,v.effort,v.speed FROM messages m
 CROSS JOIN LATERAL(SELECT role,text,removed,revision,model,effort,speed FROM message_revisions WHERE session_id=m.session_id AND local_id=m.local_id AND revision<=$2 ORDER BY revision DESC LIMIT 1) v
 WHERE m.session_id=$1 AND m.ordinal<$3 AND NOT v.removed ORDER BY m.ordinal DESC LIMIT $4`, id, revision, before, limit+1)
	if err != nil {
		return Transcript{}, err
	}
	out := Transcript{Revision: revision, Messages: []Message{}}
	textBytes := 0
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.Ordinal, &m.Role, &m.Text, &m.Removed, &m.Revision, &m.Model, &m.Effort, &m.Speed); err != nil {
			rows.Close()
			return out, err
		}
		if len(out.Messages) == limit || (textBytes+len(m.Text) > 1<<20 && len(out.Messages) > 0) {
			out.HasMore = true
			break
		}
		textBytes += len(m.Text)
		out.Messages = append(out.Messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Messages) > 0 {
		out.Before = out.Messages[len(out.Messages)-1].Ordinal
	}
	for i, j := 0, len(out.Messages)-1; i < j; i, j = i+1, j-1 {
		out.Messages[i], out.Messages[j] = out.Messages[j], out.Messages[i]
	}
	return out, nil
}
