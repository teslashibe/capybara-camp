package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

// Uses the real store and HTTP server with synthetic provider output. The
// fixture never starts a CLI or loads a personal profile. The two native
// clients sign in independently and exercise the normal follow-up API.
func seedNativeImplementation(t *testing.T, store *camp.Store, actors []auth.Actor, campID, projectID string) {
	t.Helper()
	ctx := t.Context()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	sources := make([]camp.Session, 2)
	for i, provider := range []string{"Claude", "Codex"} {
		profile, err := store.RegisterProfile(ctx, actors[i], uuid.NewString(), camp.ProfileInput{Provider: provider, LocalID: strings.Repeat(string(rune('d'+i)), 64), Label: "Synthetic " + provider})
		check(err)
		sources[i], err = store.PublishSession(ctx, actors[i], campID, uuid.NewString(), camp.SessionInput{ID: uuid.NewString(), ProjectID: projectID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: provider + " account source", Model: "fixture-model", State: "idle", Stage: "Ready", Shared: true, AllowRequests: true})
		check(err)
	}
	plan, err := store.CreatePlanning(ctx, actors[0], campID, uuid.NewString(), camp.PlanningInput{Mode: "single", Title: "Polish the project overview", ProjectID: projectID, Brief: "Plan a concise project overview and implement the approved change", Shared: true, BudgetPolicy: "calls", Start: true, Planners: []camp.PlannerInput{{SessionID: sources[0].ID, Model: "claude-sonnet-4-6", Effort: "high", Speed: "standard"}}})
	check(err)
	r, err := store.ClaimPlanningRequest(ctx, actors[0], campID, uuid.NewString(), []string{sources[0].ID})
	check(err)
	if r == nil {
		t.Fatal("planning fixture was not claimed")
	}
	_, err = store.ActOnRequest(ctx, actors[0], campID, r.ID, uuid.NewString(), camp.RequestAction{Action: "start", LeaseToken: r.LeaseToken})
	check(err)
	_, err = store.ActOnRequest(ctx, actors[0], campID, r.ID, uuid.NewString(), camp.RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Synthetic native audit", Output: "## Project overview\nKeep the project name, current tickets and repository links in one view. Reuse the existing card styles and verify keyboard access."})
	check(err)
	plan, err = store.Planning(ctx, actors[0], campID, plan.ID, 0)
	check(err)
	target := sources[1]
	execution := camp.PlanningExecution{Location: target.ExecutionLocation, Epoch: target.ExecutionEpoch}
	approval, err := store.PreparePlanningApproval(ctx, actors[0], campID, plan.ID, uuid.NewString(), camp.PlanningApprovalInput{ExpectedRevision: plan.Revision, ExpectedVersion: plan.ActiveVersion, Executor: camp.PlannerInput{SessionID: target.ID, Model: "gpt-5.4", Effort: "high", Speed: "standard", Execution: &execution}, ExpectedSessionRevision: target.Revision, ExpectedTranscriptRevision: target.TranscriptRevision, Permissions: localcli.PermissionSettings{"sandbox": "workspace-write", "approval": "on-request"}})
	check(err)
	approval, err = store.ApprovePlanningExecution(ctx, actors[1], campID, plan.ID, approval.ID, uuid.NewString(), camp.PlanningApprovalAction{ExpectedRevision: approval.Revision, ExecutionContext: strings.Repeat("a", 64)})
	check(err)
	_, err = store.DispatchPlanningExecution(ctx, actors[0], campID, plan.ID, approval.ID, uuid.NewString(), approval.Revision)
	check(err)
	r, err = store.ClaimImplementationWork(ctx, actors[1], campID, uuid.NewString(), []string{target.ID})
	check(err)
	if r == nil {
		t.Fatal("implementation fixture was not claimed")
	}
	_, err = store.ActOnRequest(ctx, actors[1], campID, r.ID, uuid.NewString(), camp.RequestAction{Action: "start", LeaseToken: r.LeaseToken, ExecutionContext: approval.ExecutionContext})
	check(err)
	for i, event := range []map[string]any{
		{"type": "session", "sessionID": uuid.NewString()},
		{"type": "settings", "model": "gpt-5.4", "effort": "high", "speed": "fast"},
		{"type": "delta", "text": "The project overview now groups tickets and repository links under the project name.\n\nKeyboard focus follows the visual order, and the cards keep their existing spacing.\n\nThis is synthetic audit output; no repository or provider account was changed."},
	} {
		raw, err := json.Marshal(event)
		check(err)
		_, err = store.AppendLocalImplementationEvent(ctx, actors[1], campID, r.ID, camp.LocalPlanningEvent{StreamEvent: camp.StreamEvent{Seq: int64(i + 1), Event: raw}, LeaseToken: r.LeaseToken})
		check(err)
	}
	_, err = store.ActOnRequest(ctx, actors[1], campID, r.ID, uuid.NewString(), camp.RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Synthetic native audit"})
	check(err)
}
