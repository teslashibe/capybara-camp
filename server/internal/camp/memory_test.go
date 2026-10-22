package camp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	memorydb "github.com/teslashibe/capybara-camp/server/internal/memory"
)

func memoryFixture(t *testing.T) (*Store, []auth.Actor, Camp, Project, Session, MemoryObservationInput) {
	t.Helper()
	s, pool, actors, c, project, profile := sessionFixture(t)
	var parent string
	if err := pool.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{memorydb.SchemaName(parent)}.Sanitize()+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	if err := memorydb.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	memories, err := memorydb.Open(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(memories.Close)
	s.SetMemoryStore(memories)
	source, err := s.PublishSession(t.Context(), actors[0], c.ID, uuid.NewString(), sessionInput(project, profile))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteTranscript(t.Context(), actors[0], c.ID, source.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "one", Ordinal: 0, Role: "assistant", Text: "A useful project decision"}, {ID: "two", Ordinal: 1, Role: "assistant", Text: "Another project decision"}, {ID: "three", Ordinal: 2, Role: "assistant", Text: "A third project decision"}}})
	if err != nil {
		t.Fatal(err)
	}
	source, err = s.Session(t.Context(), actors[0], c.ID, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := MemoryObservationInput{ProjectID: project.ID, Visibility: "private", Source: SourceRef{SessionID: source.ID, TranscriptRevision: 1, PermissionRevision: source.PermissionRevision, MessageIDs: []string{"one"}}}
	return s, actors, c, project, source, input
}

func TestMemoryObservationScopesRevocationAndReplay(t *testing.T) {
	s, actors, c, project, source, input := memoryFixture(t)
	owner, member := actors[0], actors[1]
	key := uuid.NewString()
	private, err := s.SaveMemoryObservation(t.Context(), owner, c.ID, key, input)
	if err != nil || !private.Stored || private.Visibility != "private" || !strings.Contains(private.Content, "useful project decision") {
		t.Fatal(private, err)
	}
	for range 2 {
		value, err := s.SaveMemoryObservation(t.Context(), owner, c.ID, key, input)
		if err != nil || value.ID != private.ID || !value.Stored {
			t.Fatal("replayed save", value, err)
		}
	}
	changed := input
	changed.Title = "Changed payload"
	if _, err = s.SaveMemoryObservation(t.Context(), owner, c.ID, key, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed key accepted", err)
	}
	if _, err = s.MemoryObservation(t.Context(), member, c.ID, private.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("private observation leaked", err)
	}
	page, err := s.MemoryObservations(t.Context(), member, c.ID, project.ID, "private", "", 20)
	if err != nil || len(page.Observations) != 0 {
		t.Fatal(page, err)
	}
	publicInput := input
	publicInput.Visibility = "project"
	shared, err := s.SaveMemoryObservation(t.Context(), owner, c.ID, uuid.NewString(), publicInput)
	if err != nil || shared.SpaceID == private.SpaceID || !shared.Stored {
		t.Fatal("scope separation", shared, err)
	}
	if _, err = s.MemoryObservation(t.Context(), member, c.ID, shared.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MemoryObservation(t.Context(), actors[3], c.ID, shared.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign Camp read", err)
	}
	if _, err = s.SaveMemoryObservation(t.Context(), actors[2], c.ID, uuid.NewString(), publicInput); !errors.Is(err, ErrDenied) {
		t.Fatal("viewer write", err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE sessions SET shared=false,permission_revision=permission_revision+1 WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MemoryObservation(t.Context(), member, c.ID, shared.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked memory read", err)
	}
	page, err = s.MemoryObservations(t.Context(), member, c.ID, project.ID, "project", "", 20)
	if err != nil || len(page.Observations) != 0 {
		t.Fatal("revoked memory list", page, err)
	}
	if _, err = s.MemoryObservation(t.Context(), owner, c.ID, private.ID); err != nil {
		t.Fatal("owner lost private memory", err)
	}
	events, err := s.Events(t.Context(), member, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "memory.changed" {
			t.Fatal("revoked memory event leaked")
		}
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE sessions SET shared=true,permission_revision=permission_revision+1 WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MemoryObservation(t.Context(), member, c.ID, shared.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("old grant resurrected memory", err)
	}
	events, err = s.Events(t.Context(), member, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "memory.changed" {
			t.Fatal("old memory event resurrected")
		}
	}
}

func TestMemoryCopyDoesNotHoldCampPolicyLock(t *testing.T) {
	s, actors, c, project, source, input := memoryFixture(t)
	input.Visibility = "project"
	space := uuid.NewString()
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO memory_spaces(id,camp_id,project_id) VALUES($1,$2,$3)`, space, c.ID, project.ID); err != nil {
		t.Fatal(err)
	}
	barrier, err := s.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Release()
	if _, err = barrier.Exec(t.Context(), `SELECT pg_advisory_lock(214015,hashtext($1))`, space); err != nil {
		t.Fatal(err)
	}
	locked := true
	unlock := func() {
		if locked {
			locked = false
			if _, err := barrier.Exec(context.Background(), `SELECT pg_advisory_unlock(214015,hashtext($1))`, space); err != nil {
				t.Error(err)
			}
		}
	}
	defer unlock()
	result := make(chan error, 1)
	go func() {
		_, err := s.SaveMemoryObservation(t.Context(), actors[0], c.ID, uuid.NewString(), input)
		result <- err
	}()
	received := false
	defer func() {
		unlock()
		if !received {
			<-result
		}
	}()
	ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	for {
		var waiting bool
		err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=214015 AND objid=hashtext($1)::oid AND NOT granted)`, space).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("copy did not reach the lifecycle barrier")
		}
	}
	// This is the same Camp policy lock used by sharing and membership writes.
	policy, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer policy.Rollback(context.Background())
	if _, err = s.lock(ctx, policy, actors[0], c.ID, true); err != nil {
		t.Fatal("copy held the Camp policy lock", err)
	}
	if _, err = policy.Exec(ctx, `UPDATE sessions SET shared=false,permission_revision=permission_revision+1 WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err = policy.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	unlock()
	err = <-result
	received = true
	if !errors.Is(err, ErrDenied) {
		t.Fatal("revoked copy published completion", err)
	}
	var stored bool
	if err = s.pool.QueryRow(ctx, `SELECT engine_saved_at IS NOT NULL FROM memory_observations WHERE space_id=$1`, space).Scan(&stored); err != nil || stored {
		t.Fatal("revoked copy was marked available", stored, err)
	}
}

func TestMemoryObservationPaginationAndConcurrentDuplicates(t *testing.T) {
	s, actors, c, project, _, input := memoryFixture(t)
	owner := actors[0]
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.SaveMemoryObservation(t.Context(), owner, c.ID, uuid.NewString(), input)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- v.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("duplicate observation created")
		}
	}
	for _, id := range []string{"two", "three"} {
		next := input
		next.Source.MessageIDs = []string{id}
		if _, err := s.SaveMemoryObservation(t.Context(), owner, c.ID, uuid.NewString(), next); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.MemoryObservations(t.Context(), owner, c.ID, project.ID, "private", "", 2)
	if err != nil || len(page.Observations) != 2 || page.Next == "" {
		t.Fatal(page, err)
	}
	next, err := s.MemoryObservations(t.Context(), owner, c.ID, project.ID, "private", page.Next, 2)
	if err != nil || len(next.Observations) != 1 || next.Next != "" {
		t.Fatal(next, err)
	}
	var parent string
	if err = s.pool.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.pool.QueryRow(t.Context(), "SELECT count(*) FROM "+pgx.Identifier{memorydb.SchemaName(parent), "observations"}.Sanitize()).Scan(&count); err != nil || count != 3 {
		t.Fatal("Cortex duplicates", count, err)
	}
}

func TestMemoryObservationCopyFailureAndBounds(t *testing.T) {
	s, actors, c, project, source, input := memoryFixture(t)
	owner := actors[0]
	s.memory.Close()
	key := uuid.NewString()
	saved, err := s.SaveMemoryObservation(t.Context(), owner, c.ID, key, input)
	if err != nil || saved.Stored || saved.ID == "" {
		t.Fatal("copy failure lost durable observation", saved, err)
	}
	reopened, err := memorydb.Open(t.Context(), s.pool)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s.SetMemoryStore(reopened)
	retry, err := s.SaveMemoryObservation(t.Context(), owner, c.ID, key, input)
	if err != nil || retry.ID != saved.ID || !retry.Stored {
		t.Fatal("copy did not recover", retry, err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE memory_observations SET engine_saved_at=NULL WHERE id=$1`, saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveMemoryObservation(t.Context(), owner, c.ID, key, input); err != nil {
		t.Fatal("copied but unacknowledged retry", err)
	}
	bad := input
	bad.Source.MessageIDs = []string{"missing"}
	if _, err = s.SaveMemoryObservation(t.Context(), owner, c.ID, uuid.NewString(), bad); err == nil {
		t.Fatal("missing source accepted")
	}
	bad = input
	bad.Source.PermissionRevision++
	if _, err = s.SaveMemoryObservation(t.Context(), owner, c.ID, uuid.NewString(), bad); !errors.Is(err, ErrRevision) {
		t.Fatal("stale permission accepted", err)
	}
	bad = input
	bad.Visibility = "everything"
	if _, err = s.SaveMemoryObservation(t.Context(), owner, c.ID, uuid.NewString(), bad); !errors.Is(err, ErrInput) {
		t.Fatal("invalid visibility", err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE projects SET archived=true WHERE id=$1`, project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveMemoryObservation(t.Context(), owner, c.ID, uuid.NewString(), input); !errors.Is(err, ErrTransition) {
		t.Fatal("archived project write", err)
	}
	if _, err = s.MemoryObservation(t.Context(), owner, c.ID, saved.ID); err != nil {
		t.Fatal("archived project lost history", err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE projects SET archived=false WHERE id=$1`, project.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteTranscript(t.Context(), owner, c.ID, source.ID, uuid.NewString(), TranscriptInput{ExpectedRevision: 1, Messages: []MessageInput{{ID: "large", Ordinal: 3, Role: "assistant", Text: strings.Repeat("x", (48<<10)+1)}}})
	if err != nil {
		t.Fatal(err)
	}
	bad = input
	bad.Source.TranscriptRevision = 2
	bad.Source.MessageIDs = []string{"large"}
	if _, err = s.SaveMemoryObservation(t.Context(), owner, c.ID, uuid.NewString(), bad); !errors.Is(err, ErrInput) {
		t.Fatal("oversized message accepted", err)
	}
}
