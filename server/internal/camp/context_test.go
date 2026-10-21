package camp

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestPinnedContextRangesAndRevocation(t *testing.T) {
	s, _, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner, member := actors[0], actors[1]
	input := sessionInput(p, profile)
	session, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	transcript := TranscriptInput{Messages: []MessageInput{
		{ID: "a", Ordinal: 0, Role: "user", Text: "Not selected"},
		{ID: "b", Ordinal: 1, Role: "assistant", Text: "Selected original"},
		{ID: "c", Ordinal: 2, Role: "assistant", Text: "Also not selected"},
	}}
	if _, err = s.WriteTranscript(ctx, owner, c.ID, session.ID, uuid.NewString(), transcript); err != nil {
		t.Fatal(err)
	}
	ref := SourceRef{SessionID: session.ID, TranscriptRevision: 1, PermissionRevision: session.PermissionRevision, MessageIDs: []string{"b"}}
	got, err := s.ReadContext(ctx, member, c.ID, session.ID, ref)
	if err != nil || len(got.Messages) != 1 || got.Messages[0].Text != "Selected original" || got.Revision != 1 {
		t.Fatalf("selected range: %+v %v", got, err)
	}
	transcript.ExpectedRevision = 1
	transcript.Messages = []MessageInput{{ID: "b", Ordinal: 1, Role: "assistant", Text: "New text"}, {ID: "d", Ordinal: 3, Role: "user", Text: "New message"}}
	if _, err = s.WriteTranscript(ctx, owner, c.ID, session.ID, uuid.NewString(), transcript); err != nil {
		t.Fatal(err)
	}
	got, err = s.ReadContext(ctx, member, c.ID, session.ID, ref)
	if err != nil || len(got.Messages) != 1 || got.Messages[0].Text != "Selected original" {
		t.Fatalf("snapshot mutated: %+v %v", got, err)
	}
	invalid := ref
	invalid.MessageIDs = []string{"d"}
	if _, err = s.ReadContext(ctx, member, c.ID, session.ID, invalid); !errors.Is(err, ErrRevision) {
		t.Fatal("future message read at old revision", err)
	}
	invalid.MessageIDs = []string{"b", "b"}
	if _, err = s.ReadContext(ctx, member, c.ID, session.ID, invalid); !errors.Is(err, ErrInput) {
		t.Fatal("duplicate range accepted", err)
	}
	invalid.MessageIDs = []string{}
	if _, err = s.ReadContext(ctx, member, c.ID, session.ID, invalid); !errors.Is(err, ErrInput) {
		t.Fatal("empty range read whole transcript", err)
	}
	target, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.CreateRequest(ctx, member, c.ID, target.ID, uuid.NewString(), RequestInput{Prompt: "Use selected source", Sources: []SourceRef{ref}})
	if err != nil || len(request.Sources) != 1 || len(request.Sources[0].MessageIDs) != 1 || request.Sources[0].MessageIDs[0] != "b" {
		t.Fatalf("range not saved: %+v %v", request, err)
	}
	read, err := s.Request(ctx, owner, c.ID, request.ID)
	if err != nil || len(read.Sources[0].MessageIDs) != 1 {
		t.Fatal("range not loaded", err)
	}
	input.ExpectedRevision = session.Revision
	input.Shared = false
	if _, err = s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadContext(ctx, member, c.ID, session.ID, ref); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked context read", err)
	}
	if _, err = s.ReadContext(ctx, owner, c.ID, session.ID, ref); !errors.Is(err, ErrDenied) {
		t.Fatal("stale permission accepted", err)
	}
	read, err = s.Request(ctx, owner, c.ID, request.ID)
	if err != nil || read.State != "cancelled" {
		t.Fatal("range request survived revocation", err)
	}
}
