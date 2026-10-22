package camp

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	memdb "github.com/teslashibe/capybara-camp/server/internal/memory"
)

func TestMemoryJobFreezeReplayCancelAndAccess(t *testing.T) {
	s, actors, c, project, source, input := memoryFixture(t)
	observation, err := s.SaveMemoryObservation(t.Context(), actors[0], c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	in := MemoryJobInput{ProjectID: project.ID, Visibility: "private", Target: MemoryTarget{Location: "local", ProfileID: source.ProfileID}, Model: "fixture-model", Effort: "high", Speed: "standard", ObservationIDs: []string{observation.ID}}
	key := uuid.NewString()
	first, err := s.CreateMemoryJob(t.Context(), actors[0], c.ID, key, in)
	if err != nil || first.State != "queued" || first.Target.DeviceID != actors[0].DeviceID || first.Provider != "Codex" {
		t.Fatal(first, err)
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replay, err := s.CreateMemoryJob(t.Context(), actors[0], c.ID, key, in)
			if err != nil || replay.ID != first.ID {
				t.Error("idempotent job changed", err)
			}
		}()
	}
	wg.Wait()
	if _, err = s.CreateMemoryJob(t.Context(), actors[0], c.ID, uuid.NewString(), in); !errors.Is(err, ErrTransition) {
		t.Fatal("parallel job entered namespace", err)
	}
	changed := in
	changed.Model = "changed"
	if _, err = s.CreateMemoryJob(t.Context(), actors[0], c.ID, key, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed approval replayed", err)
	}
	for _, actor := range actors[1:] {
		if _, err = s.MemoryJob(t.Context(), actor, c.ID, first.ID); !errors.Is(err, ErrDenied) {
			t.Fatal("private job leaked", err)
		}
	}
	if _, err = s.CancelMemoryJob(t.Context(), actors[1], c.ID, first.ID, uuid.NewString(), 1); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign cancellation", err)
	}
	if _, err = s.CancelMemoryJob(t.Context(), actors[0], c.ID, first.ID, uuid.NewString(), 3); !errors.Is(err, ErrRevision) {
		t.Fatal("stale cancellation", err)
	}
	cancelKey := uuid.NewString()
	cancelled, err := s.CancelMemoryJob(t.Context(), actors[0], c.ID, first.ID, cancelKey, 1)
	if err != nil || cancelled.State != "cancelled" || len(cancelled.ObservationIDs) != 0 {
		t.Fatal(cancelled, err)
	}
	replay, err := s.CancelMemoryJob(t.Context(), actors[0], c.ID, first.ID, cancelKey, 1)
	if err != nil || replay.Revision != cancelled.Revision {
		t.Fatal("cancellation replay", err)
	}
	if _, err = s.CreateMemoryJob(t.Context(), actors[0], c.ID, uuid.NewString(), in); err != nil {
		t.Fatal("cancelled job retained namespace", err)
	}
	page, err := s.MemoryJobs(t.Context(), actors[0], c.ID, project.ID, "private", "", 1)
	if err != nil || len(page.Jobs) != 1 || page.Next == "" {
		t.Fatal(page, err)
	}
	next, err := s.MemoryJobs(t.Context(), actors[0], c.ID, project.ID, "private", page.Next, 1)
	if err != nil || len(next.Jobs) != 1 || next.Next != "" {
		t.Fatal(next, err)
	}
}

func TestMemoryDerivedSourcesFenceReadsAndJobs(t *testing.T) {
	s, actors, c, project, oldSource, input := memoryFixture(t)
	input.Visibility = "project"
	old, err := s.SaveMemoryObservation(t.Context(), actors[0], c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	var parent string
	if err = s.pool.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	memoryID := "mem_" + uuid.NewString()
	_, err = s.pool.Exec(t.Context(), `INSERT INTO `+pgx.Identifier{memdb.SchemaName(parent), "memories"}.Sanitize()+`(id,user_id,kind,summary,confidence,reinforced_count,born_at,last_seen_at,sources) VALUES($1,$2,'fact','A retained project decision',0.8,1,now(),now(),$3)`, memoryID, old.SpaceID, `["`+old.ID+`"]`)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.Memory(t.Context(), actors[1], c.ID, project.ID, "project", memoryID)
	if err != nil || view.Digest == "" || len(view.ObservationIDs) != 1 {
		t.Fatal(view, err)
	}
	newInput := SessionInput{ID: uuid.NewString(), ProjectID: project.ID, ProfileID: oldSource.ProfileID, LocalSessionID: uuid.NewString(), Title: "New source", Model: "fixture", State: "idle", Stage: "Ready", Shared: true}
	newSource, err := s.PublishSession(t.Context(), actors[0], c.ID, uuid.NewString(), newInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.WriteTranscript(t.Context(), actors[0], c.ID, newSource.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "new", Ordinal: 0, Role: "assistant", Text: "New evidence"}}}); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.SaveMemoryObservation(t.Context(), actors[0], c.ID, uuid.NewString(), MemoryObservationInput{ProjectID: project.ID, Visibility: "project", Source: SourceRef{SessionID: newSource.ID, PermissionRevision: 1, TranscriptRevision: 1, MessageIDs: []string{"new"}}})
	if err != nil {
		t.Fatal(err)
	}
	in := MemoryJobInput{ProjectID: project.ID, Visibility: "project", Target: MemoryTarget{Location: "local", ProfileID: oldSource.ProfileID}, Model: "fixture", ObservationIDs: []string{fresh.ID}, Memories: []MemoryRef{{ID: memoryID, Digest: strings.Repeat("0", 64)}}}
	if _, err = s.CreateMemoryJob(t.Context(), actors[0], c.ID, uuid.NewString(), in); !errors.Is(err, ErrRevision) {
		t.Fatal("changed memory accepted", err)
	}
	in.Memories[0].Digest = view.Digest
	job, err := s.CreateMemoryJob(t.Context(), actors[0], c.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := s.MemoryJob(t.Context(), actors[1], c.ID, job.ID)
	if err != nil || shared.Target.ProfileID != "" || shared.Target.DeviceID != "" {
		t.Fatal("shared job leaked account target", shared, err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE sessions SET shared=false,permission_revision=permission_revision+1 WHERE id=$1`, oldSource.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MemoryObservation(t.Context(), actors[1], c.ID, fresh.ID); err != nil {
		t.Fatal("new source unexpectedly revoked", err)
	}
	if _, err = s.Memory(t.Context(), actors[1], c.ID, project.ID, "project", memoryID); !errors.Is(err, ErrDenied) {
		t.Fatal("inherited source leaked", err)
	}
	page, err := s.Memories(t.Context(), actors[1], c.ID, project.ID, "project", "", 50)
	if err != nil || len(page.Memories) != 0 {
		t.Fatal("revoked derived memory listed", page, err)
	}
	if _, err = s.MemoryJob(t.Context(), actors[0], c.ID, job.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("job retained inherited source access", err)
	}
	events, err := s.Events(t.Context(), actors[1], c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "memory.job" {
			t.Fatal("revoked job event leaked")
		}
	}
	if _, err = s.CancelMemoryJob(t.Context(), actors[0], c.ID, job.ID, uuid.NewString(), job.Revision); err != nil {
		t.Fatal("owner cannot stop revoked job", err)
	}
	if _, err = s.CreateMemoryJob(t.Context(), actors[0], c.ID, uuid.NewString(), in); !errors.Is(err, ErrDenied) {
		t.Fatal("new job laundered inherited source", err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE sessions SET shared=true,permission_revision=permission_revision+1 WHERE id=$1`, oldSource.ID); err != nil {
		t.Fatal(err)
	}
	for _, actor := range actors[:2] {
		if _, err = s.MemoryJob(t.Context(), actor, c.ID, job.ID); !errors.Is(err, ErrDenied) {
			t.Fatal("old job grant resurrected", err)
		}
		page, err := s.MemoryJobs(t.Context(), actor, c.ID, project.ID, "project", "", 50)
		if err != nil || len(page.Jobs) != 0 {
			t.Fatal("old job listed after re-share", page, err)
		}
		events, err := s.Events(t.Context(), actor, c.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Kind == "memory.job" {
				t.Fatal("old job event resurrected")
			}
		}
	}
}

func TestMemoryJobTargetAndInputFences(t *testing.T) {
	s, actors, c, project, source, input := memoryFixture(t)
	obs, err := s.SaveMemoryObservation(t.Context(), actors[0], c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	in := MemoryJobInput{ProjectID: project.ID, Visibility: "private", Target: MemoryTarget{Location: "local", ProfileID: source.ProfileID}, Model: "fixture", ObservationIDs: []string{obs.ID}}
	for _, actor := range actors[1:] {
		if _, err = s.CreateMemoryJob(t.Context(), actor, c.ID, uuid.NewString(), in); err == nil {
			t.Fatal("foreign input or target accepted")
		}
	}
	for _, edit := range []func(*MemoryJobInput){func(v *MemoryJobInput) { v.Target.DeviceID = actors[0].DeviceID }, func(v *MemoryJobInput) { v.Target.ProfileID = uuid.NewString() }, func(v *MemoryJobInput) { v.ObservationIDs = nil }, func(v *MemoryJobInput) { v.ObservationIDs = []string{obs.ID, obs.ID} }, func(v *MemoryJobInput) { v.Model = "--unsafe" }, func(v *MemoryJobInput) { v.Model = "bad\x00model" }} {
		bad := in
		edit(&bad)
		if _, err = s.CreateMemoryJob(t.Context(), actors[0], c.ID, uuid.NewString(), bad); err == nil {
			t.Fatal("invalid job accepted")
		}
	}
	cloud, err := NewWithProviderKey(s.pool, "fixture", bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	cloud.SetMemoryStore(s.memory)
	account, grant := addCloud(t, cloud, actors[0], "Codex")
	in.Target = MemoryTarget{Location: "cloud", AccountID: account.ID}
	if _, err = cloud.CreateMemoryJob(t.Context(), actors[0], c.ID, uuid.NewString(), in); !errors.Is(err, ErrTransition) {
		t.Fatal("unconnected target accepted", err)
	}
	login, claim := startCloudLogin(t, cloud, actors[0], account, grant)
	if _, err = cloud.ReportRunnerLogin(t.Context(), grant.Token, login.ID, claim, loginProof("fixture")); err != nil {
		t.Fatal(err)
	}
	job, err := cloud.CreateMemoryJob(t.Context(), actors[0], c.ID, uuid.NewString(), in)
	if err != nil || job.Target.InstallationID != account.InstallationID || job.Target.Epoch != account.Epoch {
		t.Fatal(job, err)
	}
}

func TestMemoryReadBoundsLargeProvenancePages(t *testing.T) {
	s, actors, c, project, _, input := memoryFixture(t)
	obs, err := s.SaveMemoryObservation(t.Context(), actors[0], c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	var parent string
	if err = s.pool.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(t.Context(), `INSERT INTO `+pgx.Identifier{memdb.SchemaName(parent), "memories"}.Sanitize()+`(id,user_id,kind,summary,confidence,reinforced_count,born_at,last_seen_at,sources) SELECT 'mem_'||n::text,$1,'fact','Large lineage fixture',0.8,1,now(),now(),(SELECT jsonb_agg($2::text) FROM generate_series(1,10000)) FROM generate_series(1,25) n`, obs.SpaceID, obs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Memories(t.Context(), actors[0], c.ID, project.ID, "private", "", 100); !errors.Is(err, ErrInput) {
		t.Fatal("large lineage page exceeded its budget", err)
	}
	if page, err := s.Memories(t.Context(), actors[0], c.ID, project.ID, "private", "", 1); err != nil || len(page.Memories) != 1 || page.Next == "" {
		t.Fatal("smaller lineage page unavailable", len(page.Memories), err)
	}
}
