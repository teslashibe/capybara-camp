package camp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

func sessionFixture(t *testing.T) (*Store, *pgxpool.Pool, []auth.Actor, Camp, Project, Profile) {
	t.Helper()
	pool := testdb.Open(t)
	ctx := context.Background()
	s := New(pool, "fixture-receipt-key")
	actors := make([]auth.Actor, 4)
	for i := range actors {
		a := auth.Actor{UserID: uuid.NewString(), DeviceID: uuid.NewString(), Name: fmt.Sprintf("Person %d", i), Email: fmt.Sprintf("member%d@example.com", i)}
		actors[i] = a
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, a.UserID, a.Email, a.Name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Fixture Mac',$3,now()+interval '1 day')`, a.DeviceID, a.UserID, auth.HashToken(a.DeviceID)); err != nil {
			t.Fatal(err)
		}
	}
	c, err := s.Create(ctx, actors[0], uuid.NewString(), "Camp")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 3; i++ {
		role := "editor"
		if i == 2 {
			role = "viewer"
		}
		inv, err := s.Invite(ctx, actors[0], c.ID, uuid.NewString(), actors[i].Email, role)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Accept(ctx, actors[i], uuid.NewString(), inv.Token); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.CreateProject(ctx, actors[0], c.ID, uuid.NewString(), "Shared project")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.RegisterProfile(ctx, actors[0], uuid.NewString(), ProfileInput{LocalID: strings.Repeat("a", 64), Provider: "Codex", Label: "Primary Codex"})
	if err != nil {
		t.Fatal(err)
	}
	return s, pool, actors, c, p, profile
}
func sessionInput(project Project, profile Profile) SessionInput {
	return SessionInput{ID: uuid.NewString(), ProjectID: project.ID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: "A shared plan", Model: "fixture-model", State: "idle", Stage: "Ready", Shared: true, AllowRequests: true}
}

func TestPrivateMetadataFencesDelayedFirstShare(t *testing.T) {
	s, _, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	input := sessionInput(p, profile)
	delayed := input
	input.Shared = false
	private, err := s.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), input)
	if err != nil || private.Shared {
		t.Fatal("private withdrawal fence", err)
	}
	if _, err = s.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), delayed); !errors.Is(err, ErrRevision) {
		t.Fatal("late first share bypassed withdrawal", err)
	}
	if _, err = s.Session(ctx, actors[1], c.ID, private.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("private fence visible to teammate", err)
	}
	delayed.ExpectedRevision = private.Revision
	shared, err := s.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), delayed)
	if err != nil || !shared.Shared {
		t.Fatal("explicit re-share failed", err)
	}
}

func TestSharedTranscriptVersionsAndRevocation(t *testing.T) {
	s, _, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner, member := actors[0], actors[1]
	input := sessionInput(p, profile)
	created, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []auth.Actor{actors[1], actors[2], actors[3]} {
		if _, err = s.PublishSession(ctx, foreign, c.ID, uuid.NewString(), input); err == nil {
			t.Fatal("another actor uploaded owner session")
		}
	}
	if _, err = s.Session(ctx, actors[3], c.ID, created.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign Camp read", err)
	}
	batch := TranscriptInput{Messages: []MessageInput{{ID: "u1", Ordinal: 0, Role: "user", Text: "Question"}, {ID: "a1", Ordinal: 1, Role: "assistant", Text: "First answer", Model: "first-model", Effort: "high", Speed: "fast"}}}
	if _, err = s.WriteTranscript(ctx, member, c.ID, created.ID, uuid.NewString(), batch); !errors.Is(err, ErrDenied) {
		t.Fatal("member wrote owner transcript", err)
	}
	key := uuid.NewString()
	revision, err := s.WriteTranscript(ctx, owner, c.ID, created.ID, key, batch)
	if err != nil || revision != 1 {
		t.Fatalf("first write: %d %v", revision, err)
	}
	if revision, err = s.WriteTranscript(ctx, owner, c.ID, created.ID, key, batch); err != nil || revision != 1 {
		t.Fatal("transcript replay duplicated", err)
	}
	batch.ExpectedRevision = 1
	batch.Messages = []MessageInput{{ID: "a1", Ordinal: 1, Role: "assistant", Text: "Updated answer", Model: "second-model", Effort: "low", Speed: "standard"}}
	if _, err = s.WriteTranscript(ctx, owner, c.ID, created.ID, uuid.NewString(), batch); err != nil {
		t.Fatal(err)
	}
	pinned, err := s.Transcript(ctx, member, c.ID, created.ID, 1, 0, 100)
	if err != nil || len(pinned.Messages) != 2 || pinned.Messages[1].Text != "First answer" || pinned.Messages[1].Model != "first-model" || pinned.Messages[1].Effort != "high" {
		t.Fatalf("pinned history lost: %+v %v", pinned, err)
	}
	latest, err := s.Transcript(ctx, member, c.ID, created.ID, 0, 0, 1)
	if err != nil || latest.Messages[0].Text != "Updated answer" || latest.Messages[0].Model != "second-model" || latest.Messages[0].Speed != "standard" || !latest.HasMore || latest.Before != 1 {
		t.Fatalf("latest page: %+v %v", latest, err)
	}
	older, err := s.Transcript(ctx, member, c.ID, created.ID, latest.Revision, latest.Before, 1)
	if err != nil || len(older.Messages) != 1 || older.Messages[0].ID != "u1" || older.HasMore {
		t.Fatalf("older page: %+v %v", older, err)
	}
	if _, err = s.WriteTranscript(ctx, owner, c.ID, created.ID, uuid.NewString(), batch); !errors.Is(err, ErrRevision) {
		t.Fatal("stale revision accepted", err)
	}
	input.ExpectedRevision = created.Revision
	input.Shared = false
	if _, err = s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Transcript(ctx, member, c.ID, created.ID, 1, 0, 100); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked historical read", err)
	}
	page, err := s.ListSessions(ctx, member, c.ID, "", "", 100)
	if err != nil || len(page.Sessions) != 0 {
		t.Fatal("revoked search result", err)
	}
	events, err := s.Events(ctx, member, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	tombstones := 0
	for _, e := range events {
		if strings.HasPrefix(e.Kind, "session.") || strings.HasPrefix(e.Kind, "transcript.") {
			if e.Kind != "session.revoked" {
				t.Fatalf("replayed revoked source: %s", e.Kind)
			}
			tombstones++
		}
	}
	if tombstones != 1 {
		t.Fatal("missing revocation event")
	}
	if _, err = s.Transcript(ctx, owner, c.ID, created.ID, 1, 0, 100); err != nil {
		t.Fatal("owner lost private history", err)
	}
}

func TestSessionPaginationAndAtomicConflicts(t *testing.T) {
	s, pool, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner := actors[0]
	ids := map[string]bool{}
	for i := 0; i < 5; i++ {
		input := sessionInput(p, profile)
		value, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input)
		if err != nil {
			t.Fatal(err)
		}
		ids[value.ID] = true
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET updated_at='2026-01-01T00:00:00Z' WHERE camp_id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	cursor := ""
	count := 0
	for {
		page, err := s.ListSessions(ctx, actors[1], c.ID, "Person 0", cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Sessions {
			if !ids[item.ID] {
				t.Fatal("duplicate or foreign item")
			}
			delete(ids, item.ID)
			count++
		}
		cursor = page.Next
		if cursor == "" {
			break
		}
	}
	if count != 5 || len(ids) != 0 {
		t.Fatalf("pagination skipped records: %d", count)
	}
	input := sessionInput(p, profile)
	key := uuid.NewString()
	value, err := s.PublishSession(ctx, owner, c.ID, key, input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.PublishSession(ctx, owner, c.ID, key, input)
	if err != nil || value.ID != replayed.ID {
		t.Fatal("duplicate publish", err)
	}
	input.Title = "Changed"
	if _, err = s.PublishSession(ctx, owner, c.ID, key, input); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay accepted", err)
	}
	bad := TranscriptInput{Messages: []MessageInput{{ID: "one", Ordinal: 0, Role: "user", Text: "one"}, {ID: "two", Ordinal: 0, Role: "assistant", Text: "two"}}}
	if _, err = s.WriteTranscript(ctx, owner, c.ID, value.ID, uuid.NewString(), bad); !errors.Is(err, ErrInput) {
		t.Fatal("duplicate ordinals accepted", err)
	}
	read, err := s.Transcript(ctx, owner, c.ID, value.ID, 0, 0, 10)
	if err != nil || len(read.Messages) != 0 || read.Revision != 0 {
		t.Fatal("failed write partially committed", err)
	}
	if _, err = s.RegisterProfile(ctx, owner, uuid.NewString(), ProfileInput{LocalID: "/Users/person/.codex", Provider: "Codex", Label: "Private path"}); !errors.Is(err, ErrInput) {
		t.Fatal("raw path accepted as profile ID")
	}
}

func TestTranscriptPageHasByteBudget(t *testing.T) {
	s, _, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner := actors[0]
	value, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, err = s.WriteTranscript(ctx, owner, c.ID, value.ID, uuid.NewString(), TranscriptInput{ExpectedRevision: int64(i), Messages: []MessageInput{{ID: fmt.Sprint(i), Ordinal: int64(i), Role: "assistant", Text: strings.Repeat("x", 600000)}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Transcript(ctx, actors[1], c.ID, value.ID, 0, 0, 100)
	if err != nil || len(page.Messages) != 1 || !page.HasMore {
		t.Fatalf("unbounded page: %d %v", len(page.Messages), err)
	}
	older, err := s.Transcript(ctx, actors[1], c.ID, value.ID, page.Revision, page.Before, 100)
	if err != nil || len(older.Messages) != 1 || older.Messages[0].ID == page.Messages[0].ID {
		t.Fatal("byte-budget pagination skipped content", err)
	}
}
