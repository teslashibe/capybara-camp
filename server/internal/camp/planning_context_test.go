package camp

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestPlanningRestartCarriesQuotedContextAndRequiresWorkerAcknowledgement(t *testing.T) {
	s, actors, c, definition := planningFixture(t)
	in := planningImportFixture(t)
	in.ProjectID = definition.ProjectID
	in.Versions = in.Versions[:1]
	in.Versions[0].Mode = "single"
	in.Versions[0].Planners = definition.Planners[:1]
	in.Versions[0].Steps = in.Versions[0].Steps[:1]
	p, err := s.ImportPlanning(t.Context(), actors[0], c.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	definition.Mode = "single"
	definition.Planners = definition.Planners[:1]
	definition.Start = true
	if _, err = s.ActOnPlanning(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &definition}); !errors.Is(err, ErrInput) {
		t.Fatal("Missing quote review was accepted", err)
	}
	definition.QuotedSourceVersion = 1
	definition.QuotedSourceIDs = []string{strings.Repeat("f", 64)}
	if _, err = s.ActOnPlanning(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &definition}); !errors.Is(err, ErrInput) {
		t.Fatal("An unrelated quote identity was accepted", err)
	}
	definition.QuotedSourceIDs = []string{in.Versions[0].LocalSources[0].ID}
	p, err = s.ActOnPlanning(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &definition})
	if err != nil || p.Version.Imported != nil || len(p.Version.QuotedSources) != 1 {
		t.Fatal(p, err)
	}
	r := claimPlanning(t, s, actors[0], c, definition)
	if r == nil || len(r.QuotedSources) != 1 || r.QuotedSources[0].Messages[0].Text != "Quoted local context" || len(r.QuotedContextDigest) != 64 {
		t.Fatal("Worker lost saved context", r)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[0], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); !errors.Is(err, ErrInput) {
		t.Fatal("Unaware worker started without context", err)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[1], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start"}); !errors.Is(err, ErrDenied) {
		t.Fatal("Context acknowledgement disclosed a private request", err)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[0], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken, QuotedContextDigest: r.QuotedContextDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[0], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Output: "New plan", Evidence: "Fixture completed with quoted context"}); err != nil {
		t.Fatal(err)
	}
	p, err = s.Planning(t.Context(), actors[0], c.ID, p.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	definition.QuotedSourceIDs = []string{}
	definition.Start = false
	next, err := s.ActOnPlanning(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &definition})
	if err != nil || len(next.Version.QuotedSources) != 0 {
		t.Fatal("Explicit removal failed", next, err)
	}
	old, err := s.Planning(t.Context(), actors[0], c.ID, p.ID, 2)
	if err != nil || len(old.Version.QuotedSources) != 1 {
		t.Fatal("Old quoted history changed", err)
	}
	definition.QuotedSourceVersion = 1
	definition.QuotedSourceIDs = []string{in.Versions[0].LocalSources[0].ID}
	restored, err := s.ActOnPlanning(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: next.Revision, Input: &definition})
	if err != nil || len(restored.Version.QuotedSources) != 1 {
		t.Fatal("An older version's quote could not be restored", err)
	}
}

func TestRunnerReceivesQuotedPlanningContext(t *testing.T) {
	g, w, grant, owner, assignment := executionFixture(t, "planning")
	if _, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), assignment); err != nil {
		t.Fatal(err)
	}
	in := planningImportFixture(t)
	in.ProjectID = w.ProjectID
	in.Versions[0].Mode = "single"
	in.Versions[0].Planners = []PlannerInput{{SessionID: w.SessionID, Model: "fixture"}}
	in.Versions[0].Steps = in.Versions[0].Steps[:1]
	p, err := g.store.ImportPlanning(t.Context(), owner, w.CampID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	definition := PlanningInput{Mode: "single", Brief: "Use the retained quote", Planners: in.Versions[0].Planners, BudgetPolicy: "calls", Start: true, QuotedSourceVersion: 1, QuotedSourceIDs: []string{in.Versions[0].LocalSources[0].ID}}
	if _, err = g.store.ActOnPlanning(t.Context(), owner, w.CampID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &definition}); err != nil {
		t.Fatal(err)
	}
	r, err := g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, uuid.NewString())
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	work, err := g.RunnerTurn(t.Context(), grant.Token, w.ID, r.ID)
	if err != nil || !json.Valid(work.Context) || !strings.Contains(string(work.Context), "Quoted local context") || !strings.Contains(string(work.Context), "plan-source:") {
		t.Fatal("Runner lost quoted context", err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); !errors.Is(err, ErrInput) {
		t.Fatal("Unaware cloud worker started", err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken, QuotedContextDigest: r.QuotedContextDigest}); err != nil {
		t.Fatal(err)
	}
}
