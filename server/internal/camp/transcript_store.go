package camp

import (
	"context"
	"encoding/json"
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
		if len(m.Attachments) > 64 || m.Removed && len(m.Attachments) > 0 {
			return ErrInput
		}
		ids := map[string]bool{}
		for _, ref := range m.Attachments {
			if !ref.valid() || ids[ref.ID] {
				return ErrInput
			}
			ids[ref.ID] = true
		}
		metadata, err := json.Marshal(m.Attachments)
		if err != nil {
			return ErrInput
		}
		if len(m.Attachments) > 0 {
			size += len(metadata)
		}

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

func appendTranscriptMessages(ctx context.Context, tx pgx.Tx, id string, revision int64, messages []MessageInput, author string) error {
	if err := verifyAttachmentBlobs(ctx, tx, id, messages); err != nil {
		return err
	}
	for _, m := range messages {
		refs := m.Attachments
		if refs == nil {
			refs = []AttachmentReference{}
		}
		encoded, err := json.Marshal(refs)
		if err != nil {
			return err
		}
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
		// The publishing owner wrote every user message in their own transcript.
		var writer any
		if m.Role == "user" && validID(author) {
			writer = author
		}
		if _, err = tx.Exec(ctx, `INSERT INTO message_revisions(session_id,local_id,revision,role,text,removed,model,effort,speed,attachments,author_user_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, m.ID, revision, m.Role, m.Text, m.Removed, m.Model, m.Effort, m.Speed, encoded, writer); err != nil {
			return err
		}
	}
	return nil
}

func verifyAttachmentBlobs(ctx context.Context, tx pgx.Tx, id string, messages []MessageInput) error {
	needed := map[string]int{}
	for _, m := range messages {
		for _, ref := range m.Attachments {
			if ref.State == "embedded" {
				if size, ok := needed[ref.SHA256]; ok && size != ref.Size {
					return ErrInput
				}
				needed[ref.SHA256] = ref.Size
			}
		}
	}
	if len(needed) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(needed))
	for hash := range needed {
		hashes = append(hashes, hash)
	}
	rows, err := tx.Query(ctx, `SELECT sha256,byte_count FROM transcript_attachment_blobs WHERE session_id=$1 AND sha256=ANY($2::text[])`, id, hashes)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		var size int
		if err = rows.Scan(&hash, &size); err != nil {
			return err
		}
		if needed[hash] != size {
			return ErrInput
		}
		delete(needed, hash)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if len(needed) != 0 {
		return ErrRevision
	}
	// The caller holds the Camp lock that cleanup shares and inserts the
	// referencing revision in this transaction.
	_, err = tx.Exec(ctx, `UPDATE transcript_attachment_blobs SET published=true WHERE session_id=$1 AND sha256=ANY($2::text[]) AND NOT published`, id, hashes)
	return err
}

func readTranscriptAt(ctx context.Context, tx pgx.Tx, id string, revision, before int64, limit int) (Transcript, error) {
	if before == 0 {
		before = 1 << 62
	}
	rows, err := tx.Query(ctx, `SELECT m.local_id,m.ordinal,v.role,v.text,v.removed,v.revision,v.model,v.effort,v.speed,v.attachments,COALESCE(v.author_user_id::text,'') FROM messages m
 CROSS JOIN LATERAL(SELECT role,text,removed,revision,model,effort,speed,attachments,author_user_id FROM message_revisions WHERE session_id=m.session_id AND local_id=m.local_id AND revision<=$2 ORDER BY revision DESC LIMIT 1) v
 WHERE m.session_id=$1 AND m.ordinal<$3 AND NOT v.removed ORDER BY m.ordinal DESC LIMIT $4`, id, revision, before, limit+1)
	if err != nil {
		return Transcript{}, err
	}
	out := Transcript{Revision: revision, Messages: []Message{}}
	textBytes := 0
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.Ordinal, &m.Role, &m.Text, &m.Removed, &m.Revision, &m.Model, &m.Effort, &m.Speed, &m.Attachments, &m.AuthorUserID); err != nil {
			rows.Close()
			return out, err
		}
		metadata, e := json.Marshal(m.Attachments)
		if e != nil {
			rows.Close()
			return out, e
		}
		messageBytes := len(m.Text)
		if len(m.Attachments) > 0 {
			messageBytes += len(metadata)
		}
		if len(out.Messages) == limit || (textBytes+messageBytes > 1<<20 && len(out.Messages) > 0) {
			out.HasMore = true
			break
		}
		textBytes += messageBytes
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
