package camp

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAttachmentHandoffRequiresExactReviewAndPreservesContent(t *testing.T) {
	f, device, in := localDestinationFixture(t, "planning", true)
	s, ctx := f.git.store, t.Context()
	review := f.upload.AttachmentReview
	if review == nil || review.Files != 1 || review.Bytes != int64(len("preserved attachment")) || len(review.Unavailable) != 1 || review.Unavailable[0].State != "reference" || !transferHash.MatchString(review.SHA256) {
		t.Fatal("Missing review", review)
	}
	h, err := s.BeginHandoff(ctx, device, f.camp.ID, f.session.ID, f.upload.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	h, a, token, ack := acknowledgedHandoff(t, f, h, false)
	commit := HandoffCommitInput{ExpectedHandoffRevision: h.Revision, ContextContinuationApproved: true}
	for _, digest := range []string{"", strings.Repeat("0", 64)} {
		commit.AttachmentReviewSHA256 = digest
		if _, err = s.RequestHandoffCommit(ctx, device, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), commit); !errors.Is(err, ErrRevision) {
			t.Fatal("Unreviewed contents accepted", err)
		}
	}
	commit.AttachmentReviewSHA256 = review.SHA256
	key := uuid.NewString()
	requested, err := s.RequestHandoffCommit(ctx, device, f.camp.ID, f.session.ID, h.ID, key, commit)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.RequestHandoffCommit(ctx, device, f.camp.ID, f.session.ID, h.ID, key, commit)
	if err != nil || replay.CommitNonce != requested.CommitNonce {
		t.Fatal("Lost confirmation changed challenge", err)
	}
	if _, err = s.FinishHandoff(ctx, a, token, h.ID, HandoffFinishInput{HandoffAck: ack, CommitNonce: requested.CommitNonce}); err != nil {
		t.Fatal(err)
	}
	content, err := s.ReadAttachment(ctx, device, f.camp.ID, f.session.ID, AttachmentRead{Revision: f.session.TranscriptRevision, MessageID: "main", AttachmentID: "a:0", SourceDigest: strings.Repeat("a", 64)})
	if err != nil || !bytes.Equal(content.Data, []byte("preserved attachment")) {
		t.Fatal("Handoff lost content", err)
	}
}

func TestAttachmentReviewMissingBodyPreventsHandoffButAllowsCancellation(t *testing.T) {
	f, device, in := localDestinationFixture(t, "planning", true)
	s, ctx := f.git.store, t.Context()
	if _, err := s.pool.Exec(ctx, `DELETE FROM transcript_attachment_blobs WHERE parent_session_id=$1`, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginHandoff(ctx, device, f.camp.ID, f.session.ID, f.upload.ID, uuid.NewString(), in); !errors.Is(err, ErrConflict) {
		t.Fatal("Missing body accepted", err)
	}
	if _, err := s.CancelTransfer(ctx, device, f.camp.ID, f.session.ID, f.upload.ID); err != nil {
		t.Fatal("Cannot cancel broken transfer", err)
	}
}

func TestAttachmentReviewPinnedDescriptorsAndDeduplication(t *testing.T) {
	s, actors, c, _, parent, body, ref := attachmentFixture(t)
	ctx, owner := t.Context(), actors[0]
	if _, err := s.PutAttachment(ctx, owner, c.ID, parent.ID, ref.SHA256, AttachmentUpload{Data: body}); err != nil {
		t.Fatal(err)
	}
	duplicate := ref
	duplicate.ID = "a:1"
	input := TranscriptInput{Messages: []MessageInput{{ID: "main", Role: "user", Attachments: []AttachmentReference{ref, duplicate}}}}
	revision, err := s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	read := func(revision int64) *TransferAttachmentReview {
		t.Helper()
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		review, err := transferAttachmentReview(ctx, tx, parent.ID, revision, true)
		if err != nil {
			t.Fatal(err)
		}
		return review
	}
	first := read(revision)
	if first.Files != 1 || first.Bytes != int64(len(body)) {
		t.Fatal("Repeated content counted twice", first)
	}
	input.ExpectedRevision = revision
	input.Messages[0].Attachments[1].SourceDigest = strings.Repeat("c", 64)
	next, err := s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	if read(next).SHA256 == first.SHA256 {
		t.Fatal("Changed descriptor kept review identity")
	}
	if read(revision).SHA256 != first.SHA256 {
		t.Fatal("Pinned review changed")
	}
}
