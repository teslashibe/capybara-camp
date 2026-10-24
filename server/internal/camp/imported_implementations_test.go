package camp

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestImportedImplementationKeepsPagedHistoryAndAttempts(t *testing.T) {
	s, actors, c, definition := planningFixture(t)
	anchor, err := s.Session(t.Context(), actors[0], c.ID, definition.Planners[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	sourceInput := SessionInput{ID: uuid.NewString(), ProfileID: anchor.ProfileID, ProjectID: anchor.ProjectID, LocalSessionID: uuid.NewString(), Title: "Implementation source", State: "idle", Stage: "Ready", Shared: true}
	source, err := s.PublishSession(t.Context(), actors[0], c.ID, uuid.NewString(), sourceInput)
	if err != nil {
		t.Fatal(err)
	}
	messages := make([]MessageInput, 230)
	large := strings.Repeat("🦫", 75000)
	for i := range messages {
		messages[i] = MessageInput{ID: fmt.Sprintf("m%d", i), Ordinal: int64(i), Role: "assistant", Text: fmt.Sprintf("Saved message %d", i)}
	}
	messages[0].Text = large
	if _, err = s.WriteTranscript(t.Context(), actors[0], c.ID, source.ID, uuid.NewString(), TranscriptInput{Messages: messages[:200]}); err != nil {
		t.Fatal(err)
	}
	revision, err := s.WriteTranscript(t.Context(), actors[0], c.ID, source.ID, uuid.NewString(), TranscriptInput{ExpectedRevision: 1, Messages: messages[200:]})
	if err != nil {
		t.Fatal(err)
	}
	implementation := ImportedImplementationInput{ImportedImplementationOrigin: ImportedImplementationOrigin{SourceID: strings.Repeat("d", 64), PlanVersion: 1, SessionID: source.ID, TranscriptRevision: revision, MessageCount: 230, Provider: "Codex", State: "complete"}, ImportedImplementationDetails: ImportedImplementationDetails{Model: "historical-model", Effort: "ultra", Speed: "fast", PlanDigest: strings.Repeat("e", 64), ContextFingerprint: strings.Repeat("f", 64), InitialPrompt: "Implement the reviewed plan", PermissionSummary: "Saved local permission summary"}}
	for i := 0; i < 11; i++ {
		implementation.Attempts = append(implementation.Attempts, ImportedImplementationAttempt{ID: uuid.NewString(), Prompt: fmt.Sprintf("Attempt %d", i), Model: "historical-model", State: "completed"})
	}
	implementation.Attempts[10].State = "uncertain"
	implementation.Attempts[10].Error = "Provider outcome was unconfirmed"
	in := planningImportFixture(t)
	in.ProjectID = definition.ProjectID
	in.Versions[0].Planners = definition.Planners
	second := implementation
	second.SourceID = strings.Repeat("4", 64)
	second.Attempts = nil
	in.Implementations = []ImportedImplementationInput{implementation, second}
	active := implementation
	active.Attempts = append([]ImportedImplementationAttempt{}, implementation.Attempts...)
	active.Attempts[0].State = "running"
	if validImportedImplementations([]ImportedImplementationInput{active}, 1) {
		t.Fatal("Running provider work accepted as historical")
	}
	foreignProfile, err := s.RegisterProfile(t.Context(), actors[1], uuid.NewString(), ProfileInput{Provider: "Codex", LocalID: strings.Repeat("7", 64), Label: "Other owner"})
	if err != nil {
		t.Fatal(err)
	}
	foreignSource, err := s.PublishSession(t.Context(), actors[1], c.ID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProfileID: foreignProfile.ID, ProjectID: definition.ProjectID, LocalSessionID: uuid.NewString(), Title: "Other source", State: "idle", Stage: "Ready", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	foreign := in
	foreign.SourceID = strings.Repeat("6", 64)
	foreign.Implementations = append([]ImportedImplementationInput{}, in.Implementations...)
	foreign.Implementations[0].SessionID = foreignSource.ID
	foreign.Implementations[0].TranscriptRevision = 0
	foreign.Implementations[0].MessageCount = 0
	if _, err = s.ImportPlanning(t.Context(), actors[0], c.ID, uuid.NewString(), foreign); !errors.Is(err, ErrDenied) {
		t.Fatal("A foreign implementation was claimed as owned history", err)
	}
	bad := in
	bad.SourceID = strings.Repeat("9", 64)
	bad.Implementations = append([]ImportedImplementationInput{}, in.Implementations...)
	bad.Implementations[0].MessageCount = 231
	if _, err = s.ImportPlanning(t.Context(), actors[0], c.ID, uuid.NewString(), bad); !errors.Is(err, ErrRevision) {
		t.Fatal("Partial transcript accepted", err)
	}
	key := uuid.NewString()
	plan, err := s.ImportPlanning(t.Context(), actors[0], c.ID, key, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ImportPlanning(t.Context(), actors[0], c.ID, uuid.NewString(), in)
	if err != nil || replay.ID != plan.ID {
		t.Fatal("Import duplicated", err)
	}
	var work int
	if err = s.pool.QueryRow(t.Context(), `SELECT count(*) FROM prompt_requests WHERE plan_id=$1`, plan.ID).Scan(&work); err != nil || work != 0 {
		t.Fatal("Historical import created provider work", err)
	}
	page, err := s.ImportedImplementations(t.Context(), actors[0], c.ID, plan.ID, 1, 0, 1)
	if err != nil || len(page.Implementations) != 1 || page.Next != 1 {
		t.Fatal(page, err)
	}
	item := page.Implementations[0]
	nextPage, err := s.ImportedImplementations(t.Context(), actors[0], c.ID, plan.ID, 1, page.Next, 1)
	if err != nil || len(nextPage.Implementations) != 1 || nextPage.Next != 0 || nextPage.Implementations[0].ID == item.ID {
		t.Fatal("Implementation cursor lost or duplicated history", nextPage, err)
	}
	detail, err := s.ImportedImplementation(t.Context(), actors[0], c.ID, plan.ID, item.ID)
	if err != nil || detail.InitialPrompt != implementation.InitialPrompt || detail.AttemptCount != 11 || detail.Model != "historical-model" {
		t.Fatal(detail, err)
	}
	attempts, err := s.ImportedImplementationAttempts(t.Context(), actors[0], c.ID, plan.ID, item.ID, 0, 10)
	if err != nil || len(attempts.Attempts) != 10 || attempts.Next != 10 {
		t.Fatal(attempts, err)
	}
	last, err := s.ImportedImplementationAttempts(t.Context(), actors[0], c.ID, plan.ID, item.ID, attempts.Next, 10)
	if err != nil || len(last.Attempts) != 1 || last.Next != 0 || last.Attempts[0].State != "uncertain" || last.Attempts[0].Error != implementation.Attempts[10].Error {
		t.Fatal(last, err)
	}
	if _, err = s.WriteTranscript(t.Context(), actors[0], c.ID, source.ID, uuid.NewString(), TranscriptInput{ExpectedRevision: revision, Messages: []MessageInput{{ID: "m0", Ordinal: 0, Role: "assistant", Text: "Later edit"}}}); err != nil {
		t.Fatal(err)
	}
	history, err := s.ImportedImplementationTranscript(t.Context(), actors[0], c.ID, plan.ID, item.ID, 0, 200)
	if err != nil || len(history.Messages) != 200 || !history.HasMore {
		t.Fatal(history, err)
	}
	older, err := s.ImportedImplementationTranscript(t.Context(), actors[0], c.ID, plan.ID, item.ID, history.Before, 200)
	if err != nil || len(older.Messages) != 30 || older.Messages[0].Text != large || older.HasMore {
		t.Fatal("Pinned history changed or truncated", err)
	}
	if _, err = s.ImportedImplementation(t.Context(), actors[1], c.ID, plan.ID, item.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("Private plan history leaked", err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE planning_tickets SET shared=true WHERE id=$1`, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportedImplementation(t.Context(), actors[1], c.ID, plan.ID, item.ID); err != nil {
		t.Fatal("Shared history unavailable", err)
	}
	sourceInput.Shared = false
	sourceInput.ExpectedRevision = source.Revision
	if _, err = s.PublishSession(t.Context(), actors[0], c.ID, uuid.NewString(), sourceInput); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportedImplementation(t.Context(), actors[1], c.ID, plan.ID, item.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("Revoked implementation source leaked", err)
	}
	if _, err = s.ImportedImplementationAttempts(t.Context(), actors[1], c.ID, plan.ID, item.ID, 0, 10); !errors.Is(err, ErrDenied) {
		t.Fatal("Revoked attempts leaked", err)
	}
	hidden, err := s.ImportedImplementations(t.Context(), actors[1], c.ID, plan.ID, 1, 0, 20)
	if err != nil || len(hidden.Implementations) != 0 {
		t.Fatal("Revoked implementation listed", err)
	}
	if _, err = s.ImportedImplementationTranscript(t.Context(), actors[3], c.ID, plan.ID, item.ID, 0, 200); !errors.Is(err, ErrDenied) {
		t.Fatal("Foreign Camp read", err)
	}
	currentRevision := revision + 1
	for i := 0; i < 8; i++ {
		currentRevision, err = s.WriteTranscript(t.Context(), actors[0], c.ID, source.ID, uuid.NewString(), TranscriptInput{ExpectedRevision: currentRevision, Messages: []MessageInput{{ID: fmt.Sprintf("large-%d", i), Ordinal: int64(230 + i), Role: "assistant", Text: strings.Repeat("x", 1<<20)}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	tooLarge := in
	tooLarge.SourceID = strings.Repeat("8", 64)
	tooLarge.Implementations = append([]ImportedImplementationInput{}, in.Implementations...)
	tooLarge.Implementations[0].MessageCount = 238
	tooLarge.Implementations[0].TranscriptRevision = currentRevision
	if _, err = s.ImportPlanning(t.Context(), actors[0], c.ID, uuid.NewString(), tooLarge); !errors.Is(err, ErrInput) {
		t.Fatal("Oversized implementation history accepted", err)
	}
}
