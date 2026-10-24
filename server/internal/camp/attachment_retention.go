package camp

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Published references in any revision retain the original body, including
// superseded messages and removed children. Only uploads without a retained reference expire.
// Publication marks the blob, so the partial expiry index holds only candidates;
// the reference check stays as a recheck on those few rows.
const expiredAttachmentUpload = `NOT b.published AND b.last_uploaded_at < clock_timestamp()-interval '7 days'
 AND NOT EXISTS(SELECT 1 FROM message_revisions v WHERE v.session_id=b.session_id
 AND v.attachments @> jsonb_build_array(jsonb_build_object('state','embedded','sha256',b.sha256)))`

func (s *Store) ReapAttachmentUploads(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT b.camp_id FROM transcript_attachment_blobs b WHERE `+expiredAttachmentUpload+` LIMIT 8`)
	if err != nil {
		return err
	}
	var camps []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		camps = append(camps, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range camps {
		if err = s.reapCampAttachmentUploads(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) reapCampAttachmentUploads(ctx context.Context, campID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Share publication's lock and recheck after acquiring it. Busy Camps can
	// wait for the next sweep without delaying another Camp's work.
	var locked string
	err = tx.QueryRow(ctx, `SELECT id FROM camps WHERE id=$1 FOR UPDATE SKIP LOCKED`, campID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `WITH expired AS (
 SELECT b.session_id,b.sha256 FROM transcript_attachment_blobs b
 WHERE b.camp_id=$1 AND `+expiredAttachmentUpload+`
 ORDER BY b.last_uploaded_at,b.session_id,b.sha256 LIMIT 16 FOR UPDATE OF b SKIP LOCKED
 ) DELETE FROM transcript_attachment_blobs b USING expired e
 WHERE b.camp_id=$1 AND b.session_id=e.session_id AND b.sha256=e.sha256`, campID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
