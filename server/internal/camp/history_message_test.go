package camp

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func TestHistoryCitationReadsExactMessageOutsideSelectionBudget(t *testing.T) {
	s, _, actors, c, p, profile := sessionFixture(t)
	owner := actors[0]
	session, err := s.PublishSession(t.Context(), owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("😀", 75000)
	if _, err = s.WriteTranscript(t.Context(), owner, c.ID, session.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "cited", Ordinal: 250, Role: "assistant", Text: text}}}); err != nil {
		t.Fatal(err)
	}
	ref := SourceRef{SessionID: session.ID, TranscriptRevision: 1, PermissionRevision: session.PermissionRevision, MessageIDs: []string{"cited"}}
	if _, err = s.ReadContext(t.Context(), owner, c.ID, session.ID, ref); !errors.Is(err, ErrInput) {
		t.Fatal("Selection budget changed", err)
	}
	page, err := s.ReadMessage(t.Context(), owner, c.ID, session.ID, ref)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != text || page.Revision != 1 {
		t.Fatal("Cited message changed", err)
	}
	if _, err = s.ReadMessage(t.Context(), auth.Actor{UserID: uuid.NewString()}, c.ID, session.ID, ref); err == nil {
		t.Fatal("Foreign user read citation")
	}
	bad := ref
	bad.PermissionRevision++
	if _, err = s.ReadMessage(t.Context(), owner, c.ID, session.ID, bad); err == nil {
		t.Fatal("Stale permissions read citation")
	}
	bad = ref
	bad.MessageIDs = []string{"cited", "other"}
	if _, err = s.ReadMessage(t.Context(), owner, c.ID, session.ID, bad); !errors.Is(err, ErrInput) {
		t.Fatal("Message endpoint admitted multiple messages", err)
	}
	if _, err = s.WriteTranscript(t.Context(), owner, c.ID, session.ID, uuid.NewString(), TranscriptInput{ExpectedRevision: 1, Messages: []MessageInput{{ID: "cited", Ordinal: 250, Role: "assistant", Text: "Changed"}}}); err != nil {
		t.Fatal(err)
	}
	page, err = s.ReadMessage(t.Context(), owner, c.ID, session.ID, ref)
	if err != nil || page.Messages[0].Text != text {
		t.Fatal("Citation lost pinned text", err)
	}
}
