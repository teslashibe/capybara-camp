package camp

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func childInput(revision int64) ChildHistoryInput {
	return ChildHistoryInput{SourceKey: strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 64), Name: "Review helper", Phase: "review", Workflow: "Feature plan", State: "done", Model: "fixture-model", ExpectedParentRevision: revision}
}

func TestChildHistoryRevisionPrivacyAndReadOnlyBoundary(t *testing.T) {
	s, _, actors, c, p, profile := sessionFixture(t)
	ctx := t.Context()
	owner, member := actors[0], actors[1]
	input := sessionInput(p, profile)
	parent, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	parent.TranscriptRevision, err = s.WriteTranscript(ctx, owner, c.ID, parent.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "main", Ordinal: 0, Role: "user", Text: "Parent only"}}})
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	in := childInput(parent.TranscriptRevision)
	started, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, id, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	if started.Complete || started.TranscriptRevision != 0 {
		t.Fatal("Incomplete upload was labelled complete", started)
	}
	if _, err = s.ChildTranscript(ctx, member, c.ID, parent.ID, id, 0, 0, 100); !errors.Is(err, ErrTransition) {
		t.Fatal("Incomplete upload was readable", err)
	}
	in.ExpectedParentRevision = started.Revision
	in.Complete = true
	in.Messages = []MessageInput{{ID: "child-message", Ordinal: 0, Role: "assistant", Text: "Original child", Model: "recorded", Effort: "high", Speed: "fast"}}
	key := uuid.NewString()
	first, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, id, key, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, id, key, in)
	if err != nil || replay != first {
		t.Fatal("Lost response replay changed history", replay, err)
	}
	got, err := s.ChildTranscript(ctx, member, c.ID, parent.ID, id, first.Revision, 0, 100)
	if err != nil || len(got.Transcript.Messages) != 1 || got.Transcript.Messages[0].Text != "Original child" || got.Transcript.Messages[0].Effort != "high" {
		t.Fatal(got, err)
	}
	message, err := s.ChildMessage(ctx, member, c.ID, parent.ID, id, first.Revision, "child-message")
	if err != nil || len(message.Transcript.Messages) != 1 || message.Transcript.Messages[0].Text != "Original child" {
		t.Fatal("Cited child message unavailable", message, err)
	}
	if _, err = s.ChildMessage(ctx, member, c.ID, parent.ID, id, first.Revision, "main"); err == nil {
		t.Fatal("Child message link read its parent")
	}
	in.ExpectedParentRevision = first.Revision
	in.ExpectedTranscriptRevision = first.TranscriptRevision
	in.Messages[0].Text = "Updated child"
	in.SourceRevision = strings.Repeat("c", 64)
	second, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, id, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.ChildTranscript(ctx, member, c.ID, parent.ID, id, first.Revision, 0, 100)
	if err != nil || got.Transcript.Messages[0].Text != "Original child" {
		t.Fatal("Pinned child changed", got, err)
	}
	got, err = s.ChildTranscript(ctx, actors[2], c.ID, parent.ID, id, 0, 0, 100)
	if err != nil || got.Transcript.Messages[0].Text != "Updated child" {
		t.Fatal("Viewer lost parent-authorized read", got, err)
	}
	main, err := s.Transcript(ctx, member, c.ID, parent.ID, 0, 0, 100)
	if err != nil || len(main.Messages) != 1 || main.Messages[0].Text != "Parent only" {
		t.Fatal("Child text mixed into main chat", main, err)
	}
	listing, err := s.ListSessions(ctx, member, c.ID, "", "", 100)
	if err != nil || len(listing.Sessions) != 1 || listing.Sessions[0].ID != parent.ID {
		t.Fatal("Child became a board ticket", listing, err)
	}
	if _, err = s.Session(ctx, owner, c.ID, id); !errors.Is(err, ErrDenied) {
		t.Fatal("Child escaped its parent endpoint", err)
	}
	if _, err = s.CreateRequest(ctx, owner, c.ID, id, uuid.NewString(), RequestInput{Prompt: "Run this child"}); !errors.Is(err, ErrDenied) {
		t.Fatal("Child became an independent executor", err)
	}
	if _, err = s.ChildTranscript(ctx, actors[3], c.ID, parent.ID, id, 0, 0, 100); !errors.Is(err, ErrDenied) {
		t.Fatal("Outsider read child", err)
	}
	other := owner
	other.DeviceID = uuid.NewString()
	in.ExpectedParentRevision = second.Revision
	if _, err = s.WriteChildHistory(ctx, other, c.ID, parent.ID, id, uuid.NewString(), in); !errors.Is(err, ErrDenied) {
		t.Fatal("Another device wrote source history", err)
	}
	if _, err = s.WriteChildHistory(ctx, member, c.ID, parent.ID, id, uuid.NewString(), in); !errors.Is(err, ErrDenied) {
		t.Fatal("Teammate wrote source history", err)
	}
	input.ExpectedRevision = parent.Revision
	input.Shared = false
	if _, err = s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChildHistories(ctx, member, c.ID, parent.ID, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("Revoked parent retained child listing", err)
	}
	if _, err = s.ChildTranscript(ctx, member, c.ID, parent.ID, id, first.Revision, 0, 100); !errors.Is(err, ErrDenied) {
		t.Fatal("Revoked parent retained old child snapshot", err)
	}
	in.ExpectedTranscriptRevision = second.TranscriptRevision
	in.Messages = nil
	in.Removed = true
	removed, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, id, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChildTranscript(ctx, owner, c.ID, parent.ID, id, first.Revision, 0, 100); !errors.Is(err, ErrDenied) {
		t.Fatal("Removed child still readable", err)
	}
	children, err := s.ChildHistories(ctx, owner, c.ID, parent.ID, removed.Revision)
	if err != nil || len(children.Children) != 0 {
		t.Fatal(children, err)
	}
	publication, err := s.ChildPublication(ctx, owner, c.ID, parent.ID)
	if err != nil || len(publication.Children) != 1 || !publication.Children[0].Removed {
		t.Fatal("Source lost its tombstone", publication, err)
	}
	if _, err = s.ChildPublication(ctx, member, c.ID, parent.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("Reader accessed the source journal", err)
	}
}

func TestChildHistoryConcurrentWritesAndIdentityConflicts(t *testing.T) {
	s, pool, actors, c, p, profile := sessionFixture(t)
	ctx := t.Context()
	owner := actors[0]
	parent, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, uuid.NewString(), uuid.NewString(), childInput(0))
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, ErrRevision) && !errors.Is(e, ErrConflict) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("Concurrent source revisions both committed", success)
	}
	children, err := s.ChildHistories(ctx, owner, c.ID, parent.ID, 0)
	if err != nil || len(children.Children) != 1 {
		t.Fatal(children, err)
	}
	if _, err = s.WriteChildHistory(ctx, owner, c.ID, parent.ID, uuid.NewString(), uuid.NewString(), childInput(children.ParentRevision)); !errors.Is(err, ErrConflict) {
		t.Fatal("Source identity acquired two children", err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE camp_id=$1`, c.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("Failed write left an orphan", count, err)
	}
	if _, err = s.ChildHistories(ctx, owner, c.ID, parent.ID, children.ParentRevision+1); !errors.Is(err, ErrRevision) {
		t.Fatal("Future child revision accepted", err)
	}
	other, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChildTranscript(ctx, owner, c.ID, other.ID, children.Children[0].ID, 0, 0, 100); !errors.Is(err, ErrDenied) {
		t.Fatal("Another parent read the child", err)
	}
}

func TestChildHistoryMultipartUploadAndPagedRead(t *testing.T) {
	s, _, actors, c, p, profile := sessionFixture(t)
	ctx := t.Context()
	owner := actors[0]
	parent, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	in := childInput(0)
	for i := 0; i < 200; i++ {
		in.Messages = append(in.Messages, MessageInput{ID: strconv.Itoa(i), Ordinal: int64(i), Role: "assistant", Text: "Stored child message"})
	}
	first, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, id, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedParentRevision = first.Revision
	in.ExpectedTranscriptRevision = first.TranscriptRevision
	in.Complete = true
	in.Messages = nil
	for i := 200; i < 230; i++ {
		in.Messages = append(in.Messages, MessageInput{ID: strconv.Itoa(i), Ordinal: int64(i), Role: "assistant", Text: "Stored child message"})
	}
	complete, err := s.WriteChildHistory(ctx, owner, c.ID, parent.ID, id, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ChildTranscript(ctx, actors[1], c.ID, parent.ID, id, complete.Revision, 0, 120)
	if err != nil || len(page.Transcript.Messages) != 120 || !page.Transcript.HasMore {
		t.Fatal(page, err)
	}
	older, err := s.ChildTranscript(ctx, actors[1], c.ID, parent.ID, id, complete.Revision, page.Transcript.Before, 120)
	if err != nil || len(older.Transcript.Messages) != 110 || older.Transcript.HasMore {
		t.Fatal(older, err)
	}
	all := append(older.Transcript.Messages, page.Transcript.Messages...)
	for i, m := range all {
		if m.ID != strconv.Itoa(i) || m.Ordinal != int64(i) {
			t.Fatal("Child paging lost its order", i, m)
		}
	}
}
