package camp

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func TestRunnerReadsPinnedAttachmentBodies(t *testing.T) {
	g, w, grant, owner, assignment := executionFixture(t, "planning")
	ctx := t.Context()
	body := bytes.Repeat([]byte{255}, 16<<20)
	ref := AttachmentReference{ID: "a:0", Kind: "image", Source: "inline", SourceDigest: strings.Repeat("c", 64), State: "embedded", MediaType: "image/png", SHA256: transferDigest(body), Size: len(body)}
	if _, err := g.store.PutAttachment(ctx, owner, w.CampID, w.SessionID, ref.SHA256, AttachmentUpload{Data: body}); err != nil {
		t.Fatal(err)
	}
	revision, err := g.store.WriteTranscript(ctx, owner, w.CampID, w.SessionID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "main", Ordinal: 0, Role: "user", Attachments: []AttachmentReference{ref}}}})
	if err != nil {
		t.Fatal(err)
	}
	childID := uuid.NewString()
	input := childInput(revision)
	child, err := g.store.WriteChildHistory(ctx, owner, w.CampID, w.SessionID, childID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.store.PutAttachment(ctx, owner, w.CampID, w.SessionID, ref.SHA256, AttachmentUpload{ChildID: childID, Data: body}); err != nil {
		t.Fatal(err)
	}
	input.ExpectedParentRevision = child.Revision
	input.Complete = true
	input.Messages = []MessageInput{{ID: "child", Ordinal: 0, Role: "assistant", Attachments: []AttachmentReference{ref}}}
	child, err = g.store.WriteChildHistory(ctx, owner, w.CampID, w.SessionID, childID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	assignment.ExpectedTranscriptRevision = child.Revision
	if _, err := g.store.AssignExecution(ctx, owner, w.CampID, w.SessionID, uuid.NewString(), assignment); err != nil {
		t.Fatal(err)
	}
	request, err := g.store.CreateRequest(ctx, owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Read the attached images"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.ClaimRunnerRequest(ctx, grant.Token, w.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ session, message string }{{"camp:" + w.SessionID, "main"}, {"camp:" + w.SessionID + "/child:" + childID, "child"}} {
		read := localcli.HistoryRead{SessionID: item.session, Limit: 1}
		page, err := g.RunnerHistory(ctx, grant.Token, w.ID, request.ID, read)
		if err != nil || len(page.Messages) != 1 || len(page.Messages[0].Attachments) != 1 {
			t.Fatal("Attachment metadata missing", err)
		}
		read.Attachment = &localcli.HistoryAttachmentSelection{MessageID: item.message, ID: ref.ID, SourceDigest: ref.SourceDigest}
		page, err = g.RunnerHistory(ctx, grant.Token, w.ID, request.ID, read)
		if err != nil || page.Attachment == nil || !bytes.Equal(page.Attachment.Data, body) {
			t.Fatal("Attachment body changed", err)
		}
		read.Attachment.MessageID = "other"
		if _, err = g.RunnerHistory(ctx, grant.Token, w.ID, request.ID, read); err == nil {
			t.Fatal("Read unselected message")
		}
	}
	if _, err = g.store.pool.Exec(ctx, `UPDATE sessions SET permission_revision=permission_revision+1 WHERE id=$1`, w.SessionID); err != nil {
		t.Fatal(err)
	}
	read := localcli.HistoryRead{SessionID: "camp:" + w.SessionID, Limit: 1, Attachment: &localcli.HistoryAttachmentSelection{MessageID: "main", ID: ref.ID, SourceDigest: ref.SourceDigest}}
	if _, err = g.RunnerHistory(ctx, grant.Token, w.ID, request.ID, read); err == nil {
		t.Fatal("Changed permission retained attachment access")
	}
}
