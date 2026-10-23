package camp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAttachmentUploadReapingRetainsHistoryAndRenewedUploads(t *testing.T) {
	s, actors, c, _, parent, body, ref := attachmentFixture(t)
	ctx, owner := t.Context(), actors[0]
	put := func(data []byte, child string) {
		t.Helper()
		if _, err := s.PutAttachment(ctx, owner, c.ID, parent.ID, transferDigest(data), AttachmentUpload{Data: data, ChildID: child}); err != nil {
			t.Fatal(err)
		}
	}
	put(body, "")
	revision, err := s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "saved", Role: "user", Attachments: []AttachmentReference{ref}}}})
	if err != nil {
		t.Fatal(err)
	}
	childID := uuid.NewString()
	child, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, childID, uuid.NewString(), childInput(revision))
	if err != nil {
		t.Fatal(err)
	}
	// The same hash in an unpublished child must not borrow the parent's reference.
	put(body, childID)
	_, err = s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), TranscriptInput{ExpectedRevision: child.Revision, Messages: []MessageInput{{ID: "saved", Role: "user", Removed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	renewed, expired, fresh := []byte("renewed upload"), []byte("expired upload"), []byte("fresh upload")
	put(renewed, "")
	put(expired, "")
	if _, err = s.pool.Exec(ctx, `UPDATE transcript_attachment_blobs SET last_uploaded_at=now()-interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	put(renewed, "")
	put(fresh, "")
	if err = s.ReapAttachmentUploads(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM transcript_attachment_blobs`).Scan(&count); err != nil || count != 3 {
		t.Fatal("Wrong retained uploads", count, err)
	}
	for _, data := range [][]byte{body, renewed, fresh} {
		var exists bool
		if err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM transcript_attachment_blobs WHERE session_id=$1 AND sha256=$2)`, parent.ID, transferDigest(data)).Scan(&exists); err != nil || !exists {
			t.Fatal("Lost retained contents", err)
		}
	}
	saved, err := s.ReadAttachment(ctx, owner, c.ID, parent.ID, AttachmentRead{Revision: revision, MessageID: "saved", AttachmentID: ref.ID, SourceDigest: ref.SourceDigest})
	if err != nil || !bytes.Equal(saved.Data, body) {
		t.Fatal("Historical bytes lost", err)
	}
	// A queued transcript whose uncommitted upload expired must resend its body.
	current, err := s.Session(ctx, owner, c.ID, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumedRef := ref
	resumedRef.SHA256, resumedRef.Size = transferDigest(expired), len(expired)
	pending := TranscriptInput{ExpectedRevision: current.TranscriptRevision, Messages: []MessageInput{{ID: "resumed", Ordinal: 1, Role: "user", Attachments: []AttachmentReference{resumedRef}}}}
	if _, err = s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), pending); !errors.Is(err, ErrRevision) {
		t.Fatal("Expired upload became readable", err)
	}
	put(expired, "")
	if _, err = s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), pending); err != nil {
		t.Fatal("Resent upload did not recover", err)
	}
	if err = s.ReapAttachmentUploads(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM transcript_attachment_blobs`).Scan(&count); err != nil || count != 4 {
		t.Fatal("Retry was not retained", count, err)
	}
}

func TestAttachmentReapingSkipsPublicationLockAndIsBounded(t *testing.T) {
	s, actors, c, _, parent, body, ref := attachmentFixture(t)
	ctx, owner := t.Context(), actors[0]
	for i := range 20 {
		data := []byte(fmt.Sprintf("unpublished-%d", i))
		if i == 0 {
			data = body
		}
		if _, err := s.PutAttachment(ctx, owner, c.ID, parent.ID, transferDigest(data), AttachmentUpload{Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE transcript_attachment_blobs SET last_uploaded_at=now()-interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	lock, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, `SELECT id FROM camps WHERE id=$1 FOR UPDATE`, c.ID); err != nil {
		t.Fatal(err)
	}
	published := make(chan error, 1)
	go func() {
		_, err := s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "saved", Role: "user", Attachments: []AttachmentReference{ref}}}})
		published <- err
	}()
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err = s.ReapAttachmentUploads(bounded); err != nil {
		t.Fatal("Cleanup waited on publisher", err)
	}
	var count int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM transcript_attachment_blobs`).Scan(&count); err != nil || count != 20 {
		t.Fatal("Cleanup entered locked Camp", count, err)
	}
	if err = lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-published; err != nil {
		t.Fatal(err)
	}
	if err = s.ReapAttachmentUploads(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM transcript_attachment_blobs`).Scan(&count); err != nil || count != 4 {
		t.Fatal("Unbounded batch", count, err)
	}
	if err = s.ReapAttachmentUploads(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM transcript_attachment_blobs`).Scan(&count); err != nil || count != 1 {
		t.Fatal("Cleanup lost newly published body", count, err)
	}
}

func TestAttachmentRetentionPreservesRemovedChildHistory(t *testing.T) {
	s, actors, c, _, parent, body, ref := attachmentFixture(t)
	ctx, owner := t.Context(), actors[0]
	id := uuid.NewString()
	input := childInput(0)
	started, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, id, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutAttachment(ctx, owner, c.ID, parent.ID, ref.SHA256, AttachmentUpload{ChildID: id, Data: body}); err != nil {
		t.Fatal(err)
	}
	input.ExpectedParentRevision = started.Revision
	input.Complete = true
	input.Messages = []MessageInput{{ID: "saved", Role: "user", Attachments: []AttachmentReference{ref}}}
	saved, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, id, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedParentRevision = saved.Revision
	input.ExpectedTranscriptRevision = saved.TranscriptRevision
	input.Removed = true
	input.Messages = nil
	if _, err = s.WriteChildHistory(ctx, owner, c.ID, parent.ID, id, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE transcript_attachment_blobs SET last_uploaded_at=now()-interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if err = s.ReapAttachmentUploads(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM transcript_attachment_blobs WHERE session_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatal("Removed child lost retained historical bytes", count, err)
	}
}

func TestAttachmentRetentionRuntimeColumnGrant(t *testing.T) {
	s, actors, c, _, parent, body, ref := attachmentFixture(t)
	ctx := t.Context()
	role := "camp_attachment_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := uuid.NewString()
	if _, err := s.pool.Exec(ctx, `CREATE ROLE `+role+` LOGIN PASSWORD '`+password+`'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DROP OWNED BY `+role)
		_, _ = s.pool.Exec(context.Background(), `DROP ROLE `+role)
	})
	var schema string
	if err := s.pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`GRANT USAGE ON SCHEMA ` + schema + ` TO ` + role, `GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA ` + schema + ` TO ` + role, `REVOKE UPDATE ON transcript_attachment_blobs FROM ` + role} {
		if _, err := s.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"00040_attachment_upload_retention.sql", "00041_attachment_publication.sql"} {
		raw, err := os.ReadFile("../database/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		grant := strings.Split(strings.Split(string(raw), "-- +goose StatementBegin")[1], "-- +goose StatementEnd")[0]
		if _, err = s.pool.Exec(ctx, strings.ReplaceAll(grant, "camp_runtime", role)); err != nil {
			t.Fatal(err)
		}
	}
	cfg := s.pool.Config().Copy()
	cfg.ConnConfig.User = role
	cfg.ConnConfig.Password = password
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	limited, err := NewWithProviderKey(pool, "fixture-receipt-key", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = limited.PutAttachment(ctx, actors[0], c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: body}); err != nil {
			t.Fatal("Runtime upload/renewal failed", err)
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE transcript_attachment_blobs SET encrypted_body=encrypted_body`); err == nil {
		t.Fatal("Renewal grant permits rewriting content")
	}
	extra := []byte("runtime published")
	if _, err = limited.PutAttachment(ctx, actors[0], c.ID, parent.ID, transferDigest(extra), AttachmentUpload{Data: extra}); err != nil {
		t.Fatal(err)
	}
	publishedRef := ref
	publishedRef.SHA256, publishedRef.Size = transferDigest(extra), len(extra)
	if _, err = limited.WriteTranscript(ctx, actors[0], c.ID, parent.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "saved", Role: "user", Attachments: []AttachmentReference{publishedRef}}}}); err != nil {
		t.Fatal("Runtime publication could not mark its upload", err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE transcript_attachment_blobs SET last_uploaded_at=now()-interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if err = limited.ReapAttachmentUploads(ctx); err != nil {
		t.Fatal("Runtime cleanup failed", err)
	}
	var count int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM transcript_attachment_blobs`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestAttachmentPublicationMarksOnlyItsSession(t *testing.T) {
	s, actors, c, _, parent, body, ref := attachmentFixture(t)
	ctx, owner := t.Context(), actors[0]
	if _, err := s.PutAttachment(ctx, owner, c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: body}); err != nil {
		t.Fatal(err)
	}
	revision, err := s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "saved", Role: "user", Attachments: []AttachmentReference{ref}}}})
	if err != nil {
		t.Fatal(err)
	}
	childID := uuid.NewString()
	if _, err = s.WriteChildHistory(ctx, owner, c.ID, parent.ID, childID, uuid.NewString(), childInput(revision)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutAttachment(ctx, owner, c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: body, ChildID: childID}); err != nil {
		t.Fatal(err)
	}
	marks := map[string]bool{}
	rows, err := s.pool.Query(ctx, `SELECT session_id::text,published FROM transcript_attachment_blobs WHERE sha256=$1`, ref.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var published bool
		if err = rows.Scan(&id, &published); err != nil {
			t.Fatal(err)
		}
		marks[id] = published
	}
	rows.Close()
	if len(marks) != 2 || !marks[parent.ID] || marks[childID] {
		t.Fatal("Publication marked the wrong uploads", marks)
	}
	// Aged published bodies are not cleanup candidates at all.
	if _, err = s.pool.Exec(ctx, `UPDATE transcript_attachment_blobs SET last_uploaded_at=now()-interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	var candidates int
	if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM transcript_attachment_blobs b WHERE NOT b.published AND b.last_uploaded_at < clock_timestamp()-interval '7 days'`).Scan(&candidates); err != nil || candidates != 1 {
		t.Fatal("Published body stayed a cleanup candidate", candidates, err)
	}
}
