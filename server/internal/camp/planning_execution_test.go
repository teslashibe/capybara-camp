package camp

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func finishPlanningFixture(t *testing.T, s *Store, owner auth.Actor, campID string, r *Request) {
	t.Helper()
	if r == nil {
		t.Fatal("missing planning request")
	}
	if _, err := s.ActOnRequest(t.Context(), owner, campID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActOnRequest(t.Context(), owner, campID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Synthetic planning completion", Output: "Preserved fixture draft"}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanningExecutionReviewAndRestart(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	owner := actors[0]
	input.Planners[0].Execution = &PlanningExecution{Location: "local", Epoch: 8}
	if _, err := s.CreatePlanning(t.Context(), owner, c.ID, uuid.NewString(), input); !errors.Is(err, ErrRevision) {
		t.Fatal("accepted stale client location", err)
	}
	input.Planners[0].Execution = nil
	p, err := s.CreatePlanning(t.Context(), owner, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, planner := range p.Version.Planners {
		if planner.Execution == nil || *planner.Execution != (PlanningExecution{Location: "local", Epoch: 1}) {
			t.Fatal("missing initial binding", planner.Execution)
		}
	}
	session, err := s.Session(t.Context(), owner, c.ID, input.Planners[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AssignExecution(t.Context(), owner, c.ID, session.ID, uuid.NewString(), ExecutionInput{Location: "local", ExpectedEpoch: 1, ExpectedSessionRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnPlanning(t.Context(), owner, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "start", ExpectedRevision: p.Revision}); !errors.Is(err, ErrRevision) {
		t.Fatal("started on a changed assignment", err)
	}
	input.Start = true
	p, err = s.ActOnPlanning(t.Context(), owner, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &input})
	if err != nil || p.Version.Version != 2 || p.Version.Planners[0].Execution.Epoch != 2 {
		t.Fatal("reviewed restart did not bind new assignment", err)
	}
	old, err := s.Planning(t.Context(), owner, c.ID, p.ID, 1)
	if err != nil || old.Version.Planners[0].Execution.Epoch != 1 {
		t.Fatal("history was rewritten", err)
	}
	listed, err := s.ListPlanning(t.Context(), owner, c.ID)
	if err != nil || len(listed) != 1 || listed[0].Planners[0].Execution.Epoch != 2 {
		t.Fatal("summary lost binding", err)
	}
}

func TestPlanningDoesNotChangeLocationBetweenPhases(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	owner := actors[0]
	p := createAndStartPlanning(t, s, owner, c, input)
	r := claimPlanning(t, s, owner, c, input)
	finishPlanningFixture(t, s, owner, c.ID, r)
	session, err := s.Session(t.Context(), owner, c.ID, r.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AssignExecution(t.Context(), owner, c.ID, session.ID, uuid.NewString(), ExecutionInput{Location: "local", ExpectedEpoch: session.ExecutionEpoch, ExpectedSessionRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision}); err != nil {
		t.Fatal(err)
	}
	finishPlanningFixture(t, s, owner, c.ID, claimPlanning(t, s, owner, c, input))
	p, err = s.Planning(t.Context(), owner, c.ID, p.ID, 0)
	if err != nil || p.Version.State != "failed" || len(p.Version.Steps) != 2 {
		t.Fatal("phase changed assignment", p.Version.State, err)
	}
	for _, step := range p.Version.Steps {
		if step.Output != "Preserved fixture draft" || step.State != "completed" {
			t.Fatal("completion rolled back", step)
		}
	}
	if r := claimPlanning(t, s, owner, c, input); r != nil {
		t.Fatal("queued another provider turn", r.ID)
	}
}

func TestPlanningCloudLossKeepsCompletedDrafts(t *testing.T) {
	g, w, grant, owner, assignment := executionFixture(t, "planning")
	s := g.store
	if _, err := s.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), assignment); err != nil {
		t.Fatal(err)
	}
	profile, err := s.RegisterProfile(t.Context(), owner, uuid.NewString(), ProfileInput{LocalID: strings.Repeat("d", 64), Provider: "Claude", Label: "Local comparison"})
	if err != nil {
		t.Fatal(err)
	}
	local, err := s.PublishSession(t.Context(), owner, w.CampID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: w.ProjectID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: "Local planner", State: "idle", Stage: "Ready"})
	if err != nil {
		t.Fatal(err)
	}
	input := PlanningInput{Title: "Mixed locations", ProjectID: w.ProjectID, Brief: "A bounded fixture", BudgetPolicy: "calls", Planners: []PlannerInput{{SessionID: w.SessionID}, {SessionID: local.ID}}}
	p := createAndStartPlanning(t, s, owner, Camp{ID: w.CampID}, input)
	if p.Version.Planners[0].Execution.Location != "cloud" || p.Version.Planners[0].Execution.WorkspaceID != w.ID || p.Version.Planners[1].Execution.Location != "local" {
		t.Fatal("mixed locations lost")
	}
	claim := uuid.NewString()
	r, err := g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, claim)
	if err != nil || r == nil {
		t.Fatal("missing cloud draft", err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claim}); err != nil {
		t.Fatal(err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: claim, Outcome: "completed", Evidence: "Synthetic cloud result", Output: "Preserved cloud draft"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE provider_installations SET credential_expires_at=now()-interval '1 second' WHERE id=$1`, w.ProviderInstallationID); err != nil {
		t.Fatal(err)
	}
	finishPlanningFixture(t, s, owner, w.CampID, claimPlanning(t, s, owner, Camp{ID: w.CampID}, input))
	p, err = s.Planning(t.Context(), owner, w.CampID, p.ID, 0)
	if err != nil || p.Version.State != "failed" || len(p.Version.Steps) != 2 {
		t.Fatal("cloud loss did not stop comparison", p.Version.State, err)
	}
	for _, step := range p.Version.Steps {
		if step.State != "completed" || step.Output == "" {
			t.Fatal("accepted output was lost", step)
		}
	}
}
