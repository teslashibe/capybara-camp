package camp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func planningImportFixture(t *testing.T) PlanningImportInput {
	t.Helper()
	return PlanningImportInput{SourceID: strings.Repeat("a", 64), Title: "Imported local plan", Versions: []ImportedPlanningVersion{{Version: 1, Mode: "comparison", State: "ready", Brief: "Preserve both proposals", BudgetPolicy: "calls", LocalSources: []ImportedPlanningSource{{ID: strings.Repeat("b", 64), Title: "Local context", Revision: strings.Repeat("c", 64), Messages: []ImportedPlanningMessage{{ID: "one", Role: "assistant", Text: "Quoted local context"}}}}, Steps: []ImportedPlanningStep{{Ordinal: 0, State: "completed", Output: "Draft A"}, {Ordinal: 1, State: "completed", Output: "Draft B"}, {Ordinal: 2, State: "completed", Output: "Review A"}, {Ordinal: 3, State: "completed", Output: "Review B"}, {Ordinal: 4, State: "completed", Output: "Combined plan with disagreement"}}}}}
}

func TestPlanningImportPreservesHistoryWithoutProviderWork(t *testing.T) {
	s, actors, c, definition := planningFixture(t)
	in := planningImportFixture(t)
	in.ProjectID = definition.ProjectID
	in.Versions[0].Planners = definition.Planners
	second := ImportedPlanningVersion{Version: 2, Mode: "single", State: "paused", Brief: "Later single draft", Planners: definition.Planners[:1], BudgetPolicy: "calls", Steps: []ImportedPlanningStep{{Ordinal: 0, State: "unknown", Error: "Outcome was not confirmed"}}}
	in.Versions = append(in.Versions, second)
	key := uuid.NewString()
	p, err := s.ImportPlanning(t.Context(), actors[0], c.ID, key, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Shared || p.ActiveVersion != 2 || p.Version.State != "needs_review" || p.Version.Imported == nil || p.Version.Planners[0].Execution != nil || p.Version.Steps[0].RequestID != "" || p.Version.Steps[0].State != "outcome_unknown" {
		t.Fatal("Imported history claimed execution", p)
	}
	var requests int
	if err = s.pool.QueryRow(t.Context(), `SELECT count(*) FROM prompt_requests WHERE plan_id=$1`, p.ID).Scan(&requests); err != nil || requests != 0 {
		t.Fatal("Import created provider work", requests, err)
	}
	first, err := s.Planning(t.Context(), actors[0], c.ID, p.ID, 1)
	if err != nil || first.Version.State != "ready" || first.Version.Steps[4].Output != "Combined plan with disagreement" || first.Version.Imported.LocalSources[0].Messages[0].Text != "Quoted local context" {
		t.Fatal(first, err)
	}
	for _, replayKey := range []string{key, uuid.NewString()} {
		replay, e := s.ImportPlanning(t.Context(), actors[0], c.ID, replayKey, in)
		if e != nil || replay.ID != p.ID {
			t.Fatal("Import duplicated", replay, e)
		}
	}
	changed := in
	changed.Title = "Changed import"
	if _, err = s.ImportPlanning(t.Context(), actors[0], c.ID, uuid.NewString(), changed); !errors.Is(err, ErrConflict) {
		t.Fatal("Changed source silently overwrote imported plan", err)
	}
	if _, err = s.Planning(t.Context(), actors[1], c.ID, p.ID, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("Private import was shared", err)
	}
	if _, err = s.ActOnPlanning(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "resume", ExpectedRevision: p.Revision}); !errors.Is(err, ErrTransition) {
		t.Fatal("Import replayed an uncertain turn", err)
	}
	restarted, err := s.ActOnPlanning(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &definition})
	if err != nil || restarted.ActiveVersion != 3 || restarted.Version.Imported != nil || restarted.Version.Planners[0].Execution == nil {
		t.Fatal("Restart did not use normal reviewed assignments", restarted, err)
	}
	list, err := s.ListPlanning(t.Context(), actors[0], c.ID)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
}

func TestPlanningImportValidatesSnapshotsAndEmptyDraft(t *testing.T) {
	s, actors, c, definition := planningFixture(t)
	in := planningImportFixture(t)
	in.ProjectID = definition.ProjectID
	in.Versions[0].Planners = definition.Planners
	for _, mutate := range []func(*PlanningImportInput){
		func(i *PlanningImportInput) { i.Versions[0].Steps[0].State = "running" },
		func(i *PlanningImportInput) { i.Versions[0].Steps[0].Output = "" },
		func(i *PlanningImportInput) {
			i.Versions[0].LocalSources[0].Messages[0].Text = strings.Repeat("x", 49<<10)
		},
		func(i *PlanningImportInput) { i.Versions[0].Version = 2 },
		func(i *PlanningImportInput) { i.Versions[0].Brief = strings.Repeat(" ", 8192) + "x" },
		func(i *PlanningImportInput) {
			i.Versions[0].Planners[0].Execution = &PlanningExecution{Location: "local", Epoch: 1}
		},
	} {
		raw, _ := json.Marshal(in)
		var bad PlanningImportInput
		_ = json.Unmarshal(raw, &bad)
		mutate(&bad)
		if _, err := s.ImportPlanning(t.Context(), actors[0], c.ID, uuid.NewString(), bad); !errors.Is(err, ErrInput) {
			t.Fatal("Invalid archive accepted", err)
		}
	}
	in.Versions = []ImportedPlanningVersion{{Version: 1, Mode: "single", State: "draft", Planners: definition.Planners[:1], BudgetPolicy: "strict_tokens"}}
	p, err := s.ImportPlanning(t.Context(), actors[0], c.ID, uuid.NewString(), in)
	if err != nil || p.Version.Brief != "" || p.Version.Imported.State != "draft" || p.Version.State != "needs_review" {
		t.Fatal("Empty draft was changed", p, err)
	}
	list, err := s.ListPlanning(t.Context(), actors[0], c.ID)
	if err != nil || len(list) != 1 || list[0].State != "needs_review" || list[0].Completed != 0 {
		t.Fatal("Empty imported draft was omitted from the board", list, err)
	}
}

func TestPlanningImportRechecksSourceAccessOnReplay(t *testing.T) {
	s, actors, c, definition := planningFixture(t)
	profile, err := s.RegisterProfile(t.Context(), actors[1], uuid.NewString(), ProfileInput{LocalID: strings.Repeat("9", 64), Provider: "Claude", Label: "Source owner"})
	if err != nil {
		t.Fatal(err)
	}
	sourceInput := SessionInput{ID: uuid.NewString(), ProfileID: profile.ID, ProjectID: definition.ProjectID, LocalSessionID: "source", Title: "Shared source", State: "idle", Stage: "Ready", Shared: true}
	source, err := s.PublishSession(t.Context(), actors[1], c.ID, uuid.NewString(), sourceInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.WriteTranscript(t.Context(), actors[1], c.ID, source.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "message", Role: "assistant", Text: "Context"}}}); err != nil {
		t.Fatal(err)
	}
	in := planningImportFixture(t)
	in.ProjectID = definition.ProjectID
	in.Versions[0].Planners = definition.Planners
	in.Versions[0].Sources = []SourceRef{{SessionID: source.ID, TranscriptRevision: 1, PermissionRevision: source.PermissionRevision, MessageIDs: []string{"message"}}}
	key := uuid.NewString()
	p, err := s.ImportPlanning(t.Context(), actors[0], c.ID, key, in)
	if err != nil {
		t.Fatal(err)
	}
	foreign := in
	foreign.SourceID = strings.Repeat("8", 64)
	foreign.Versions = append([]ImportedPlanningVersion{}, in.Versions...)
	foreign.Versions[0].Planners = []PlannerInput{{SessionID: definition.Planners[0].SessionID}, {SessionID: source.ID}}
	if _, err = s.ImportPlanning(t.Context(), actors[0], c.ID, uuid.NewString(), foreign); !errors.Is(err, ErrDenied) {
		t.Fatal("Imported another person's planner as owned history", err)
	}
	sourceInput.Shared = false
	sourceInput.ExpectedRevision = source.Revision
	if _, err = s.PublishSession(t.Context(), actors[1], c.ID, uuid.NewString(), sourceInput); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Planning(t.Context(), actors[0], c.ID, p.ID, 1); !errors.Is(err, ErrDenied) {
		t.Fatal("Imported snapshot bypassed source revocation", err)
	}
	for _, replayKey := range []string{key, uuid.NewString()} {
		if _, err = s.ImportPlanning(t.Context(), actors[0], c.ID, replayKey, in); !errors.Is(err, ErrDenied) {
			t.Fatal("Import receipt bypassed source revocation", err)
		}
	}
	list, err := s.ListPlanning(t.Context(), actors[0], c.ID)
	if err != nil || len(list) != 0 {
		t.Fatal("Revoked plan remained on the board", list, err)
	}
}
