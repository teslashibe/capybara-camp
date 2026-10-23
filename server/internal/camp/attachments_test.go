package camp

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func attachmentFixture(t *testing.T) (*Store, []auth.Actor, Camp, SessionInput, Session, []byte, AttachmentReference) {
	t.Helper()
	base, pool, actors, c, p, profile := sessionFixture(t)
	s, err := NewWithProviderKey(pool, "fixture-receipt-key", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	base.providerCipher = s.providerCipher
	in := sessionInput(p, profile)
	session, err := s.PublishSession(t.Context(), actors[0], c.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("private image bytes from the owner's transcript")
	ref := AttachmentReference{ID: "a:0", Kind: "image", Source: "inline", SourceDigest: strings.Repeat("a", 64), State: "embedded", MediaType: "image/png", SHA256: transferDigest(body), Size: len(body)}
	return s, actors, c, in, session, body, ref
}

func TestAttachmentPublicationEncryptionIsolationAndRecovery(t *testing.T) {
	s, actors, c, sessionInput, parent, body, ref := attachmentFixture(t)
	ctx := t.Context()
	owner, member := actors[0], actors[1]
	input := TranscriptInput{Messages: []MessageInput{{ID: "image", Ordinal: 0, Role: "user", Attachments: []AttachmentReference{ref}}}}
	if _, err := s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), input); !errors.Is(err, ErrRevision) {
		t.Fatal("Published missing bytes", err)
	}
	for _, actor := range actors[1:] {
		if _, err := s.PutAttachment(ctx, actor, c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: body}); !errors.Is(err, ErrDenied) {
			t.Fatal("Non-owner upload", err)
		}
	}
	var group sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			receipt, err := s.PutAttachment(ctx, owner, c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: body})
			if err == nil && (receipt.SessionID != parent.ID || receipt.SHA256 != ref.SHA256 || receipt.Bytes != len(body)) {
				err = ErrConflict
			}
			results <- err
		}()
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var encrypted []byte
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM transcript_attachment_blobs`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT encrypted_body FROM transcript_attachment_blobs WHERE session_id=$1`, parent.ID).Scan(&encrypted); err != nil || bytes.Contains(encrypted, body) {
		t.Fatal("Unencrypted bytes", err)
	}
	if _, err := s.openProvider(encrypted, attachmentBinding(c.ID, uuid.NewString(), ref.SHA256)); err == nil {
		t.Fatal("Ciphertext crossed conversation binding")
	}
	read := AttachmentRead{Revision: 1, MessageID: "image", AttachmentID: ref.ID, SourceDigest: ref.SourceDigest}
	if _, err := s.ReadAttachment(ctx, owner, c.ID, parent.ID, read); err == nil {
		t.Fatal("Read an unpublished blob")
	}
	key := uuid.NewString()
	revision, err := s.WriteTranscript(ctx, owner, c.ID, parent.ID, key, input)
	if err != nil || revision != 1 {
		t.Fatal(revision, err)
	}
	if revision, err = s.WriteTranscript(ctx, owner, c.ID, parent.ID, key, input); err != nil || revision != 1 {
		t.Fatal("Lost response changed the revision", err)
	}
	for _, actor := range actors[:3] {
		value, err := s.ReadAttachment(ctx, actor, c.ID, parent.ID, read)
		if err != nil || !bytes.Equal(value.Data, body) || value.Attachment != ref || value.Revision != 1 || value.PermissionRevision != parent.PermissionRevision {
			t.Fatal(value, err)
		}
	}
	if _, err = s.ReadAttachment(ctx, actors[3], c.ID, parent.ID, read); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	transcript, err := s.Transcript(ctx, member, c.ID, parent.ID, 1, 0, 100)
	if err != nil || len(transcript.Messages) != 1 || len(transcript.Messages[0].Attachments) != 1 || transcript.Messages[0].Attachments[0] != ref {
		t.Fatal(transcript, err)
	}
	wrong := read
	wrong.SourceDigest = strings.Repeat("b", 64)
	if _, err = s.ReadAttachment(ctx, member, c.ID, parent.ID, wrong); !errors.Is(err, ErrDenied) {
		t.Fatal("Changed source read", err)
	}
	wrong = read
	wrong.MessageID = "other"
	if _, err = s.ReadAttachment(ctx, member, c.ID, parent.ID, wrong); err == nil {
		t.Fatal("Hash alone read bytes")
	}
	second := sessionInput
	second.ID = uuid.NewString()
	second.LocalSessionID = uuid.NewString()
	second.ExpectedRevision = 0
	other, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.WriteTranscript(ctx, owner, c.ID, other.ID, uuid.NewString(), input); !errors.Is(err, ErrRevision) {
		t.Fatal("Reused another session's blob", err)
	}
	input.ExpectedRevision = 1
	input.Messages[0].Attachments = nil
	input.Messages[0].Removed = true
	if _, err = s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	wrong = read
	wrong.Revision = 2
	if _, err = s.ReadAttachment(ctx, member, c.ID, parent.ID, wrong); err == nil {
		t.Fatal("Tombstone exposed bytes")
	}
	if _, err = s.ReadAttachment(ctx, member, c.ID, parent.ID, read); err != nil {
		t.Fatal("Pinned history lost bytes", err)
	}
	sessionInput.Shared = false
	sessionInput.ExpectedRevision = parent.Revision
	if _, err = s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadAttachment(ctx, member, c.ID, parent.ID, read); !errors.Is(err, ErrDenied) {
		t.Fatal("Revoked share retained byte access", err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE devices SET revoked_at=now() WHERE id=$1`, owner.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadAttachment(ctx, owner, c.ID, parent.ID, read); !errors.Is(err, ErrDenied) {
		t.Fatal("Revoked device read", err)
	}
	if _, err = s.PutAttachment(ctx, owner, c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: body}); !errors.Is(err, ErrDenied) {
		t.Fatal("Replay bypassed device revocation", err)
	}
}

func TestChildAttachmentParentAuthorityAndTransferReview(t *testing.T) {
	s, actors, c, _, parent, body, ref := attachmentFixture(t)
	ctx := t.Context()
	owner, member := actors[0], actors[1]
	childID := uuid.NewString()
	child := childInput(0)
	started, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, childID, uuid.NewString(), child)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutAttachment(ctx, owner, c.ID, parent.ID, ref.SHA256, AttachmentUpload{ChildID: childID, Data: body}); err != nil {
		t.Fatal(err)
	}
	read := AttachmentRead{ChildID: childID, Revision: started.Revision, MessageID: "image", AttachmentID: ref.ID, SourceDigest: ref.SourceDigest}
	if _, err = s.ReadAttachment(ctx, member, c.ID, parent.ID, read); !errors.Is(err, ErrTransition) {
		t.Fatal("Incomplete child readable", err)
	}
	child.ExpectedParentRevision = started.Revision
	child.Complete = true
	child.Messages = []MessageInput{{ID: "image", Ordinal: 0, Role: "user", Attachments: []AttachmentReference{ref}}}
	complete, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, childID, uuid.NewString(), child)
	if err != nil {
		t.Fatal(err)
	}
	read.Revision = complete.Revision
	result, err := s.ReadAttachment(ctx, member, c.ID, parent.ID, read)
	if err != nil || result.SessionID != childID || !bytes.Equal(result.Data, body) {
		t.Fatal(result, err)
	}
	wrong := read
	wrong.ChildID = ""
	if _, err = s.ReadAttachment(ctx, member, c.ID, parent.ID, wrong); err == nil {
		t.Fatal("Child escaped parent transcript")
	}
	if _, err = s.ReadAttachment(ctx, member, c.ID, childID, read); err == nil {
		t.Fatal("Child escaped parent route")
	}
	parent, err = s.Session(ctx, owner, c.ID, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	review, err := transferAttachmentReview(ctx, tx, parent.ID, parent.TranscriptRevision, true)
	tx.Rollback(ctx)
	if err != nil || review == nil || review.Files != 1 || review.Bytes != int64(len(body)) {
		t.Fatal("Child contents missing from transfer review", review, err)
	}
	child.ExpectedParentRevision = complete.Revision
	child.ExpectedTranscriptRevision = complete.TranscriptRevision
	child.Removed = true
	child.Messages = nil
	if _, err = s.WriteChildHistory(ctx, owner, c.ID, parent.ID, childID, uuid.NewString(), child); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadAttachment(ctx, member, c.ID, parent.ID, read); !errors.Is(err, ErrDenied) {
		t.Fatal("Removed child remained readable", err)
	}
}

func TestAttachmentValidationAndAdmission(t *testing.T) {
	s, actors, c, _, parent, body, ref := attachmentFixture(t)
	ctx := t.Context()
	owner := actors[0]
	if _, err := s.PutAttachment(ctx, owner, c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: []byte("changed")}); !errors.Is(err, ErrInput) {
		t.Fatal(err)
	}
	wrong := owner
	wrong.DeviceID = actors[1].DeviceID
	if _, err := s.PutAttachment(ctx, wrong, c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: body}); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	for _, mutate := range []func(*AttachmentReference){func(r *AttachmentReference) { r.ID = "https://example.test" }, func(r *AttachmentReference) { r.Size = -1 }, func(r *AttachmentReference) { r.State = "ready" }, func(r *AttachmentReference) { r.MediaType = "text/html" }, func(r *AttachmentReference) { r.Source = "remote" }} {
		bad := ref
		mutate(&bad)
		if err := validateTranscriptMessages([]MessageInput{{ID: "x", Role: "user", Attachments: []AttachmentReference{bad}}}, false); !errors.Is(err, ErrInput) {
			t.Fatal(bad, err)
		}
	}
	// Fill the count limit with small encrypted orphan bodies, not large test data.
	if _, err := s.pool.Exec(ctx, `INSERT INTO transcript_attachment_blobs(camp_id,parent_session_id,session_id,sha256,byte_count,encrypted_body) SELECT $1,$2,$2,lpad(to_hex(i),64,'0'),1,decode(repeat('00',29),'hex') FROM generate_series(1,1024) i`, c.ID, parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAttachment(ctx, owner, c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: body}); !errors.Is(err, ErrAttachmentCapacity) {
		t.Fatal("Unbounded blob count", err)
	}
}

func TestAttachmentCorruptionAndCrossCampRead(t *testing.T) {
	s, actors, c, _, parent, body, ref := attachmentFixture(t)
	ctx := t.Context()
	owner := actors[0]
	if _, err := s.PutAttachment(ctx, owner, c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: body}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "image", Role: "user", Attachments: []AttachmentReference{ref}}}}); err != nil {
		t.Fatal(err)
	}
	read := AttachmentRead{Revision: 1, MessageID: "image", AttachmentID: ref.ID, SourceDigest: ref.SourceDigest}
	other, err := s.Create(ctx, owner, uuid.NewString(), "Other Camp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadAttachment(ctx, owner, other.ID, parent.ID, read); !errors.Is(err, ErrDenied) {
		t.Fatal("Cross-Camp bytes", err)
	}
	corrupt, err := s.sealProvider(bytes.Repeat([]byte{0}, len(body)), attachmentBinding(c.ID, parent.ID, ref.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE transcript_attachment_blobs SET encrypted_body=$2 WHERE session_id=$1`, parent.ID, corrupt); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ReadAttachment(ctx, owner, c.ID, parent.ID, read); !errors.Is(err, ErrConflict) || len(result.Data) > 0 {
		t.Fatal("Read corrupt content", err)
	}
	if _, err = s.PutAttachment(ctx, owner, c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: body}); !errors.Is(err, ErrConflict) {
		t.Fatal("Replay acknowledged corrupt stored bytes", err)
	}
	corrupt[0] ^= 0xff
	if _, err = s.pool.Exec(ctx, `UPDATE transcript_attachment_blobs SET encrypted_body=$2 WHERE session_id=$1`, parent.ID, corrupt); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ReadAttachment(ctx, owner, c.ID, parent.ID, read); err == nil || len(result.Data) > 0 {
		t.Fatal("Read corrupt ciphertext", err)
	}
}
