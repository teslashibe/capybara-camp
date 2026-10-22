package camp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func TestHistorySourcesRetainContextWithoutBecomingExecutors(t *testing.T) {
	s, pool, actors, c, project, profile := sessionFixture(t)
	owner, member := actors[0], actors[1]
	in := sessionInput(project, profile)
	in.HistoryOnly, in.Shared, in.AllowRequests = true, false, false
	in.LocalSessionID = "history:" + strings.Repeat("a", 64)
	key := uuid.NewString()
	history, err := s.PublishSession(t.Context(), owner, c.ID, key, in)
	if err != nil || !history.HistoryOnly {
		t.Fatal("History source was not retained", history, err)
	}
	if repeated, err := s.PublishSession(t.Context(), owner, c.ID, key, in); err != nil || repeated.ID != history.ID || !repeated.HistoryOnly {
		t.Fatal("History publication did not replay", repeated, err)
	}
	revision, err := s.WriteTranscript(t.Context(), owner, c.ID, history.ID, uuid.NewString(), TranscriptInput{
		Messages: []MessageInput{{ID: "one", Ordinal: 0, Role: "assistant", Text: "Saved implementation", Model: "historical", Effort: "ultra", Speed: "fast"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.Transcript(t.Context(), owner, c.ID, history.ID, revision, 0, 200)
	if err != nil || len(read.Messages) != 1 || read.Messages[0].Model != "historical" {
		t.Fatal("History content was not readable", read, err)
	}
	if _, err = s.Session(t.Context(), member, c.ID, history.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("Private history leaked to a member", err)
	}
	if _, err = s.CreateRequest(t.Context(), owner, c.ID, history.ID, uuid.NewString(), RequestInput{Prompt: "Run this"}); !errors.Is(err, ErrTransition) {
		t.Fatal("A history source accepted provider work", err)
	}
	if _, err = s.Execution(t.Context(), owner, c.ID, history.ID); !errors.Is(err, ErrTransition) {
		t.Fatal("A history source advertised an executor", err)
	}
	if _, err = s.AssignExecution(t.Context(), owner, c.ID, history.ID, uuid.NewString(), ExecutionInput{
		Location: "local", ExpectedEpoch: 1, ExpectedSessionRevision: history.Revision, ExpectedTranscriptRevision: revision,
	}); !errors.Is(err, ErrTransition) {
		t.Fatal("A history source changed execution location", err)
	}
	if _, err = s.AdoptSession(t.Context(), owner, c.ID, history.ID, uuid.NewString(), OwnershipInput{ProfileID: profile.ID, ExpectedRevision: history.Revision}); !errors.Is(err, ErrTransition) {
		t.Fatal("A history source accepted execution ownership", err)
	}
	if _, err = s.MoveSession(t.Context(), owner, c.ID, history.ID, uuid.NewString(), "In progress", history.Revision); !errors.Is(err, ErrTransition) {
		t.Fatal("A history source was moved as a board task", err)
	}
	definition := PlanningInput{Mode: "single", ProjectID: project.ID, Title: "Plan", Brief: "Use saved context", BudgetPolicy: "calls", Planners: []PlannerInput{{SessionID: history.ID}}}
	if _, err = s.CreatePlanning(t.Context(), owner, c.ID, uuid.NewString(), definition); !errors.Is(err, ErrTransition) {
		t.Fatal("An archived planner accepted new work", err)
	}
	for _, mutate := range []func(*SessionInput){
		func(x *SessionInput) { x.AllowRequests = true },
		func(x *SessionInput) { x.ExternalOwner = true },
		func(x *SessionInput) { x.State = "running" },
		func(x *SessionInput) { x.State = "waiting" },
		func(x *SessionInput) { x.Stage = "In progress" },
	} {
		bad := in
		bad.ID, bad.LocalSessionID = uuid.NewString(), uuid.NewString()
		mutate(&bad)
		if _, err = s.PublishSession(t.Context(), owner, c.ID, uuid.NewString(), bad); !errors.Is(err, ErrInput) {
			t.Fatal("Invalid history source accepted", err)
		}
	}
	changed := in
	changed.ExpectedRevision, changed.HistoryOnly = history.Revision, false
	if _, err = s.PublishSession(t.Context(), owner, c.ID, uuid.NewString(), changed); !errors.Is(err, ErrTransition) {
		t.Fatal("History was converted to an executor", err)
	}
	for _, sql := range []string{
		`UPDATE sessions SET allow_requests=true WHERE id=$1`,
		`UPDATE sessions SET external_owner=true WHERE id=$1`,
		`UPDATE sessions SET state='running' WHERE id=$1`,
		`UPDATE sessions SET stage='In progress' WHERE id=$1`,
		`UPDATE sessions SET execution_epoch=2 WHERE id=$1`,
	} {
		if _, err = pool.Exec(t.Context(), sql, history.ID); err == nil {
			t.Fatal("Database accepted executable history", sql)
		}
	}
	imported := PlanningImportInput{SourceID: strings.Repeat("b", 64), Title: "Imported history", ProjectID: project.ID,
		Versions: []ImportedPlanningVersion{{Version: 1, Mode: "single", State: "ready", Brief: "Historical plan", BudgetPolicy: "calls",
			Planners: []PlannerInput{{SessionID: history.ID, Model: "old-model", Effort: "old-effort", Speed: "old-speed"}},
			Steps:    []ImportedPlanningStep{{Ordinal: 0, State: "completed", Output: "Saved plan"}}}},
		Implementations: []ImportedImplementationInput{{
			ImportedImplementationOrigin:  ImportedImplementationOrigin{SourceID: strings.Repeat("c", 64), PlanVersion: 1, SessionID: history.ID, TranscriptRevision: revision, MessageCount: 1, Provider: "Codex", State: "complete"},
			ImportedImplementationDetails: ImportedImplementationDetails{PlanDigest: strings.Repeat("d", 64), ContextFingerprint: strings.Repeat("e", 64)},
		}},
	}
	plan, err := s.ImportPlanning(t.Context(), owner, c.ID, uuid.NewString(), imported)
	if err != nil || plan.Version.Planners[0].Execution != nil || plan.Version.Planners[0].Effort != "old-effort" {
		t.Fatal("Historical planner was lost or became executable", plan, err)
	}
	items, err := s.ImportedImplementations(t.Context(), owner, c.ID, plan.ID, 1, 0, 20)
	if err != nil || len(items.Implementations) != 1 {
		t.Fatal(items, err)
	}
	saved, err := s.ImportedImplementationTranscript(t.Context(), owner, c.ID, plan.ID, items.Implementations[0].ID, 0, 200)
	if err != nil || len(saved.Messages) != 1 || saved.Messages[0].Text != "Saved implementation" {
		t.Fatal(saved, err)
	}
	// An ordinary executor can still read a deliberately attached history source.
	liveInput := sessionInput(project, profile)
	live, err := s.PublishSession(t.Context(), owner, c.ID, uuid.NewString(), liveInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateRequest(t.Context(), owner, c.ID, live.ID, uuid.NewString(), RequestInput{
		Prompt: "Keep private context private", Sources: []SourceRef{{SessionID: history.ID, TranscriptRevision: revision, MessageIDs: []string{"one"}}},
	}); !errors.Is(err, ErrDenied) {
		t.Fatal("Private history flowed into a shared session", err)
	}
	liveInput.Shared, liveInput.ExpectedRevision = false, live.Revision
	live, err = s.PublishSession(t.Context(), owner, c.ID, uuid.NewString(), liveInput)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateRequest(t.Context(), owner, c.ID, live.ID, uuid.NewString(), RequestInput{
		Prompt: "Read the saved context", Sources: []SourceRef{{SessionID: history.ID, TranscriptRevision: revision, MessageIDs: []string{"one"}}},
	})
	if err != nil {
		t.Fatal("History could not be used as read context", err)
	}
	changed = in
	changed.ExpectedRevision, changed.Shared = history.Revision, true
	history, err = s.PublishSession(t.Context(), owner, c.ID, uuid.NewString(), changed)
	if err != nil || !history.HistoryOnly {
		t.Fatal("Explicit history sharing failed", err)
	}
	if _, err = s.Transcript(t.Context(), member, c.ID, history.ID, revision, 0, 200); err != nil {
		t.Fatal("Authorized member could not read shared history", err)
	}
	if _, err = s.CreateRequest(t.Context(), member, c.ID, history.ID, uuid.NewString(), RequestInput{Prompt: "Run"}); !errors.Is(err, ErrTransition) {
		t.Fatal("Member started archived history", err)
	}
	ordinaryJSON, _ := json.Marshal(liveInput)
	if strings.Contains(string(ordinaryJSON), "history_only") {
		t.Fatal("Ordinary publication changed its idempotency payload")
	}
}

func TestOwnerCanFinishHistoryCopyFromAnotherDevice(t *testing.T) {
	s, pool, actors, c, project, profile := sessionFixture(t)
	owner := actors[0]
	other := auth.Actor{UserID: owner.UserID, DeviceID: uuid.NewString(), Email: owner.Email, Name: owner.Name}
	if _, err := pool.Exec(t.Context(), `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Recovery Mac',$3,now()+interval '1 day')`, other.DeviceID, other.UserID, auth.HashToken(other.DeviceID)); err != nil {
		t.Fatal(err)
	}
	input := sessionInput(project, profile)
	input.HistoryOnly, input.AllowRequests = true, false
	history, err := s.PublishSession(t.Context(), owner, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	first := TranscriptInput{Messages: []MessageInput{{ID: "first", Ordinal: 0, Role: "user", Text: "First captured prompt"}}}
	revision, err := s.WriteTranscript(t.Context(), owner, c.ID, history.ID, uuid.NewString(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := TranscriptInput{ExpectedRevision: revision, Messages: []MessageInput{{ID: "second", Ordinal: 1, Role: "assistant", Text: "Captured answer"}}}
	if _, err = s.WriteTranscript(t.Context(), actors[1], c.ID, history.ID, uuid.NewString(), second); !errors.Is(err, ErrDenied) {
		t.Fatal("A teammate wrote the owner's history", err)
	}
	key := uuid.NewString()
	completed, err := s.WriteTranscript(t.Context(), other, c.ID, history.ID, key, second)
	if err != nil || completed != revision+1 {
		t.Fatal("Owner could not finish the saved copy", completed, err)
	}
	if repeated, err := s.WriteTranscript(t.Context(), other, c.ID, history.ID, key, second); err != nil || repeated != completed {
		t.Fatal("History recovery repeated a page", repeated, err)
	}
	pinned, err := s.Transcript(t.Context(), owner, c.ID, history.ID, revision, 0, 200)
	if err != nil || len(pinned.Messages) != 1 || pinned.Messages[0].Text != first.Messages[0].Text {
		t.Fatal("Recovery changed a pinned earlier revision", err)
	}
	current, err := s.Session(t.Context(), other, c.ID, history.ID)
	if err != nil || current.OwnerDeviceID != owner.DeviceID || !current.HistoryOnly || current.ExecutionEpoch != 1 || current.AllowRequests {
		t.Fatal("Copying history transferred execution ownership", current, err)
	}
	input.ID, input.LocalSessionID, input.HistoryOnly = uuid.NewString(), uuid.NewString(), false
	live, err := s.PublishSession(t.Context(), owner, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.WriteTranscript(t.Context(), other, c.ID, live.ID, uuid.NewString(), first); !errors.Is(err, ErrDenied) {
		t.Fatal("History recovery widened live-session writing", err)
	}
}

func TestHistorySourcesCannotPrepareCloudWorkOrTransfers(t *testing.T) {
	g, _, actors, c, original, input := workspaceFixture(t)
	s := g.store
	project := Project{ID: original.ProjectID}
	profile, err := s.RegisterProfile(t.Context(), actors[0], uuid.NewString(), ProfileInput{LocalID: strings.Repeat("f", 64), Provider: "Codex", Label: "History source"})
	if err != nil {
		t.Fatal(err)
	}
	historyInput := sessionInput(project, profile)
	historyInput.HistoryOnly, historyInput.AllowRequests, historyInput.Shared = true, false, false
	history, err := s.PublishSession(t.Context(), actors[0], c.ID, uuid.NewString(), historyInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.CreateWorkspace(t.Context(), actors[0], c.ID, history.ID, uuid.NewString(), input); !errors.Is(err, ErrTransition) {
		t.Fatal("A history source created a cloud workspace", err)
	}
	tx, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = s.transferOwner(t.Context(), tx, actors[0], c.ID, history.ID, true); !errors.Is(err, ErrTransition) {
		t.Fatal("A history source began a transfer", err)
	}
}
