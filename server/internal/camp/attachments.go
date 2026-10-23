package camp

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/attachments"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

var ErrAttachmentCapacity = errors.New("attachment storage limit reached")

// Camp uses snake_case wire keys; the local helper retains its camelCase wire.
type AttachmentReference struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	SourceDigest string `json:"source_digest"`
	State        string `json:"state"`
	MediaType    string `json:"media_type,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Size         int    `json:"size,omitempty"`
}

func (r AttachmentReference) valid() bool {
	return (attachments.Reference{ID: r.ID, Kind: r.Kind, Source: r.Source, SourceDigest: r.SourceDigest, State: r.State, MediaType: r.MediaType, SHA256: r.SHA256, Size: r.Size}).Valid()
}

type AttachmentUpload struct {
	ChildID string `json:"child_id,omitempty"`
	Data    []byte `json:"data"`
}
type AttachmentReceipt struct {
	SessionID string `json:"session_id"`
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
}
type AttachmentRead struct {
	ChildID      string `json:"child_id,omitempty"`
	Revision     int64  `json:"revision"`
	MessageID    string `json:"message_id"`
	AttachmentID string `json:"attachment_id"`
	SourceDigest string `json:"source_digest"`
}
type AttachmentBody struct {
	SessionID          string              `json:"session_id"`
	MessageID          string              `json:"message_id"`
	Revision           int64               `json:"revision"`
	PermissionRevision int64               `json:"permission_revision"`
	Attachment         AttachmentReference `json:"attachment"`
	Data               []byte              `json:"data"`
}

func attachmentBinding(campID, sessionID, hash string) string {
	return "transcript-attachment:v1:" + campID + ":" + sessionID + ":" + hash
}

func attachmentDevice(ctx context.Context, tx pgx.Tx, a auth.Actor) error {
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devices WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now())`, a.DeviceID, a.UserID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return ErrDenied
	}
	return nil
}

// PutAttachment is a content-addressed PUT scoped to one source conversation.
// A retry verifies current ownership before returning the existing receipt.
// Uploads are not readable until a committed message revision references them.
func (s *Store) PutAttachment(ctx context.Context, a auth.Actor, campID, parentID, hash string, in AttachmentUpload) (AttachmentReceipt, error) {
	var out AttachmentReceipt
	if !transferHash.MatchString(hash) || len(in.Data) < 1 || len(in.Data) > attachments.MaxBytes || transferDigest(in.Data) != hash || in.ChildID != "" && !validID(in.ChildID) {
		return out, ErrInput
	}
	if err := s.cloudEnabled(); err != nil {
		return out, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return out, err
	}
	if err = attachmentDevice(ctx, tx, a); err != nil {
		return out, err
	}
	parent, err := s.readSession(ctx, tx, a, campID, parentID)
	if err != nil {
		return out, err
	}
	if parent.OwnerUserID != a.UserID || parent.OwnerDeviceID != a.DeviceID && !parent.HistoryOnly {
		return out, ErrDenied
	}
	if parent.ExecutionLocation != "local" {
		return out, ErrTransition
	}
	id := parentID
	if in.ChildID != "" {
		child, e := readChildHistory(ctx, tx, parentID, in.ChildID, parent.TranscriptRevision)
		if e != nil {
			return out, e
		}
		if child.Removed {
			return out, ErrDenied
		}
		id = in.ChildID
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, a.UserID); err != nil {
		return out, err
	}
	var size int
	var saved []byte
	err = tx.QueryRow(ctx, `SELECT byte_count,encrypted_body FROM transcript_attachment_blobs WHERE camp_id=$1 AND parent_session_id=$2 AND session_id=$3 AND sha256=$4`, campID, parentID, id, hash).Scan(&size, &saved)
	out = AttachmentReceipt{SessionID: id, SHA256: hash, Bytes: len(in.Data)}
	if err == nil {
		if size != len(in.Data) {
			return out, ErrConflict
		}
		plain, e := s.openProvider(saved, attachmentBinding(campID, id, hash))
		if e != nil || len(plain) != size || transferDigest(plain) != hash {
			return out, ErrConflict
		}
		if _, err = tx.Exec(ctx, `UPDATE transcript_attachment_blobs SET last_uploaded_at=clock_timestamp() WHERE session_id=$1 AND sha256=$2`, id, hash); err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var parentBytes, userBytes int64
	var count int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(byte_count) FILTER(WHERE b.parent_session_id=$1),0),COALESCE(sum(byte_count),0),count(*) FILTER(WHERE b.parent_session_id=$1) FROM transcript_attachment_blobs b JOIN sessions p ON p.id=b.parent_session_id WHERE p.owner_user_id=$2`, parentID, a.UserID).Scan(&parentBytes, &userBytes, &count); err != nil {
		return out, err
	}
	if parentBytes+int64(len(in.Data)) > 128<<20 || userBytes+int64(len(in.Data)) > 512<<20 || count >= 1024 {
		return out, ErrAttachmentCapacity
	}
	encrypted, err := s.sealProvider(in.Data, attachmentBinding(campID, id, hash))
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO transcript_attachment_blobs(camp_id,parent_session_id,session_id,sha256,byte_count,encrypted_body) VALUES($1,$2,$3,$4,$5,$6)`, campID, parentID, id, hash, len(in.Data), encrypted); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ReadAttachment(ctx context.Context, a auth.Actor, campID, parentID string, in AttachmentRead) (AttachmentBody, error) {
	return retryRead(ctx, func() (AttachmentBody, error) {
		var out AttachmentBody
		if in.Revision < 1 || in.MessageID == "" || len(in.MessageID) > 256 || len(in.AttachmentID) > 100 || !transferHash.MatchString(in.SourceDigest) || in.ChildID != "" && !validID(in.ChildID) {
			return out, ErrInput
		}
		if err := s.cloudEnabled(); err != nil {
			return out, err
		}
		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			return out, err
		}
		defer tx.Rollback(ctx)
		if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
			return out, err
		}
		if err = attachmentDevice(ctx, tx, a); err != nil {
			return out, err
		}
		parent, err := s.readSession(ctx, tx, a, campID, parentID)
		if err != nil {
			return out, err
		}
		if in.Revision > parent.TranscriptRevision {
			return out, ErrRevision
		}
		id, revision := parentID, in.Revision
		if in.ChildID != "" {
			current, e := readChildHistory(ctx, tx, parentID, in.ChildID, parent.TranscriptRevision)
			if e != nil {
				return out, e
			}
			if current.Removed {
				return out, ErrDenied
			}
			child, e := readChildHistory(ctx, tx, parentID, in.ChildID, in.Revision)
			if e != nil {
				return out, e
			}
			if child.Removed || !child.Complete {
				return out, ErrTransition
			}
			id, revision = in.ChildID, child.TranscriptRevision
		}
		messages, err := readContextMessagesBudget(ctx, tx, SourceRef{SessionID: id, TranscriptRevision: revision, MessageIDs: []string{in.MessageID}}, 1<<20)
		if err != nil {
			return out, err
		}
		var ref AttachmentReference
		for _, v := range messages[0].Attachments {
			if v.ID == in.AttachmentID && v.SourceDigest == in.SourceDigest {
				ref = v
				break
			}
		}
		if ref.State != "embedded" || !ref.valid() {
			return out, ErrDenied
		}
		data, err := s.readAttachmentBlob(ctx, tx, campID, parentID, id, ref)
		if err != nil {
			return out, err
		}
		out = AttachmentBody{SessionID: id, MessageID: in.MessageID, Revision: in.Revision, PermissionRevision: parent.PermissionRevision, Attachment: ref, Data: data}
		return out, tx.Commit(ctx)
	})
}

// Caller has authorized the current parent and selected a committed reference
// at its pinned revision. The content hash alone never grants a read.
func (s *Store) readAttachmentBlob(ctx context.Context, tx pgx.Tx, campID, parentID, id string, ref AttachmentReference) ([]byte, error) {
	if !ref.valid() || ref.State != "embedded" {
		return nil, ErrDenied
	}
	var encrypted []byte
	var size int
	err := tx.QueryRow(ctx, `SELECT encrypted_body,byte_count FROM transcript_attachment_blobs WHERE camp_id=$1 AND parent_session_id=$2 AND session_id=$3 AND sha256=$4`, campID, parentID, id, ref.SHA256).Scan(&encrypted, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	data, err := s.openProvider(encrypted, attachmentBinding(campID, id, ref.SHA256))
	if err != nil {
		return nil, err
	}
	if len(data) != size || size != ref.Size || transferDigest(data) != ref.SHA256 {
		return nil, ErrConflict
	}
	return data, nil
}
