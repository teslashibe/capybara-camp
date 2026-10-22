package camp

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestSinglePlanOneCallAndComparisonRestart(t *testing.T) {
	s, _, actors, c, project, profile := sessionFixture(t)
	ctx, owner := context.Background(), actors[0]
	source, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(project, profile))
	if err != nil {
		t.Fatal(err)
	}
	input := PlanningInput{Mode: "single", Title: "One model", ProjectID: project.ID, Brief: "Plan one bounded change", BudgetPolicy: "calls", Start: true, Planners: []PlannerInput{{SessionID: source.ID, Model: "chosen", Effort: "high", Speed: "fast"}}}
	key := uuid.NewString()
	plan, err := s.CreatePlanning(ctx, owner, c.ID, key, input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version.Mode != "single" || plan.Version.CallLimit != 1 || len(plan.Version.Planners) != 1 || len(plan.Version.Steps) != 1 {
		t.Fatalf("single plan contract: %+v", plan.Version)
	}
	replay, err := s.CreatePlanning(ctx, owner, c.ID, key, input)
	if err != nil || replay.ID != plan.ID {
		t.Fatal("creation replay", err)
	}
	r := claimPlanning(t, s, owner, c, input)
	if r == nil || r.PlanStep != 0 || r.Model != "chosen" || strings.Contains(r.Prompt, "other proposal") {
		t.Fatal("wrong single request", r)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	finishKey := uuid.NewString()
	finish := RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Synthetic completed plan", Output: "Final single plan"}
	for i := 0; i < 2; i++ {
		if _, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, finishKey, finish); err != nil {
			t.Fatal(err)
		}
	}
	plan, err = s.Planning(ctx, owner, c.ID, plan.ID, 0)
	if err != nil || plan.Version.State != "ready" || len(plan.Version.Steps) != 1 || plan.Version.Steps[0].Output != "Final single plan" {
		t.Fatal("single completion", plan.Version, err)
	}
	if claimPlanning(t, s, owner, c, input) != nil {
		t.Fatal("single plan queued a hidden second call")
	}
	summaries, err := s.ListPlanning(ctx, owner, c.ID)
	if err != nil || len(summaries) != 1 || summaries[0].Mode != "single" || summaries[0].Completed != 1 {
		t.Fatal("single summary", err)
	}
	claude, err := s.RegisterProfile(ctx, owner, uuid.NewString(), ProfileInput{LocalID: strings.Repeat("c", 64), Provider: "Claude", Label: "Second planner"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(project, claude))
	if err != nil {
		t.Fatal(err)
	}
	comparison := input
	comparison.Mode = "comparison"
	comparison.Planners = append(append([]PlannerInput{}, input.Planners...), PlannerInput{SessionID: second.ID, Model: "peer"})
	next, err := s.ActOnPlanning(ctx, owner, c.ID, plan.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: plan.Revision, Input: &comparison})
	if err != nil || next.ID != plan.ID || next.Version.Version != 2 || next.Version.Mode != "comparison" || next.Version.CallLimit != 5 || len(next.Version.Steps) != 2 {
		t.Fatal("comparison restart", next.Version, err)
	}
	old, err := s.Planning(ctx, owner, c.ID, plan.ID, 1)
	if err != nil || old.Version.Mode != "single" || old.Version.State != "ready" || old.Version.Steps[0].Output != "Final single plan" {
		t.Fatal("old single history changed", err)
	}
	invalid := input
	invalid.Mode = "comparison"
	if _, err = s.CreatePlanning(ctx, owner, c.ID, uuid.NewString(), invalid); !errors.Is(err, ErrInput) {
		t.Fatal("comparison accepted one participant", err)
	}
	invalid = input
	invalid.MergeSlot = 1
	if _, err = s.CreatePlanning(ctx, owner, c.ID, uuid.NewString(), invalid); !errors.Is(err, ErrInput) {
		t.Fatal("single accepted another merge slot", err)
	}
	invalid = comparison
	invalid.Mode = "single"
	if _, err = s.CreatePlanning(ctx, owner, c.ID, uuid.NewString(), invalid); !errors.Is(err, ErrInput) {
		t.Fatal("single accepted two participants", err)
	}
}
