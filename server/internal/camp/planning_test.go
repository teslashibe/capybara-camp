package camp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func planningFixture(t *testing.T) (*Store, []auth.Actor, Camp, PlanningInput) {
	t.Helper()
	s, _, actors, c, p, codex := sessionFixture(t)
	ctx := context.Background()
	claude, err := s.RegisterProfile(ctx, actors[0], uuid.NewString(), ProfileInput{LocalID: strings.Repeat("b", 64), Provider: "Claude", Label: "Claude fixture"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), sessionInput(p, codex))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), sessionInput(p, claude))
	if err != nil {
		t.Fatal(err)
	}
	input := PlanningInput{Title: "Plan a synthetic feature", ProjectID: p.ID, Brief: "Add a small synthetic feature and retain disagreements", BudgetPolicy: "calls", Planners: []PlannerInput{{SessionID: a.ID, Model: "codex-fixture", Effort: "low", Speed: "standard"}, {SessionID: b.ID, Model: "claude-fixture", Effort: "high", Speed: "fast"}}}
	return s, actors, c, input
}
func createAndStartPlanning(t *testing.T, s *Store, a auth.Actor, c Camp, input PlanningInput) PlanningTicket {
	t.Helper()
	ctx := context.Background()
	p, err := s.CreatePlanning(ctx, a, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "start", ExpectedRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func claimPlanning(t *testing.T, s *Store, a auth.Actor, c Camp, input PlanningInput) *Request {
	t.Helper()
	r, err := s.ClaimPlanningRequest(context.Background(), a, c.ID, uuid.NewString(), []string{input.Planners[0].SessionID, input.Planners[1].SessionID})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPlanningFiveCallsAndFrozenResults(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	a := actors[0]
	ctx := context.Background()
	p := createAndStartPlanning(t, s, a, c, input)
	if p.Version.State != "running" || len(p.Version.Steps) != 2 {
		t.Fatalf("initial drafts: %+v", p.Version)
	}
	if r, err := s.ClaimRequest(ctx, a, c.ID, uuid.NewString()); err != nil || r != nil {
		t.Fatal("chat worker claimed planning", r, err)
	}
	for count := 0; count < 5; count++ {
		r := claimPlanning(t, s, a, c, input)
		if r == nil {
			t.Fatalf("missing step %d", count)
		}
		if r.Kind != "planning" || r.PlanID != p.ID {
			t.Fatalf("missing planning fence: %+v", r)
		}
		if r.PlanStep < 2 && strings.Contains(r.Prompt, "Other provider's draft") {
			t.Fatal("drafts were not independent")
		}
		if r.PlanStep >= 2 && r.PlanStep < 4 && !strings.Contains(r.Prompt, "Other provider's draft") {
			t.Fatal("review omitted peer draft")
		}
		if r.PlanStep == 4 && (!strings.Contains(r.Prompt, "Review A:") || !strings.Contains(r.Prompt, "unresolved disagreements")) {
			t.Fatal("merge omitted evidence")
		}
		if _, err := s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
			t.Fatal(err)
		}
		tokens := int64(17)
		key := uuid.NewString()
		result := RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Synthetic worker completed", Output: fmt.Sprintf("Output of step %d, with an unresolved choice", r.PlanStep), Usage: &PlanningUsage{OutputTokens: &tokens}}
		if _, err := s.ActOnRequest(ctx, a, c.ID, r.ID, key, result); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ActOnRequest(ctx, a, c.ID, r.ID, key, result); err != nil {
			t.Fatal("finish replay failed", err)
		}
	}
	p, err := s.Planning(ctx, a, c.ID, p.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Version.State != "ready" || len(p.Version.Steps) != 5 {
		t.Fatalf("five-step plan did not finish: %+v", p.Version)
	}
	for _, step := range p.Version.Steps {
		if step.State != "completed" || step.Output == "" || step.Usage == nil || step.Usage.OutputTokens == nil || *step.Usage.OutputTokens != 17 {
			t.Fatalf("lost output: %+v", step)
		}
	}
	if p.Version.Planners[0].Model != "codex-fixture" || p.Version.Planners[1].Effort != "high" || p.Version.Planners[1].Speed != "fast" {
		t.Fatal("planner settings changed")
	}
	if r := claimPlanning(t, s, a, c, input); r != nil {
		t.Fatal("a sixth call was queued")
	}
	list, err := s.ListPlanning(ctx, a, c.ID)
	if err != nil || len(list) != 1 || list[0].State != "ready" || len(list[0].Planners) != 2 {
		t.Fatal("bad board summary", list, err)
	}
}

func TestPlanningPrivacyAndExplicitBudgetPolicy(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	ctx := context.Background()
	a := actors[0]
	key := uuid.NewString()
	input.BudgetPolicy = ""
	p, err := s.CreatePlanning(ctx, a, c.ID, key, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreatePlanning(ctx, a, c.ID, key, input)
	if err != nil || replay.ID != p.ID {
		t.Fatal("create replay duplicated", err)
	}
	if _, err = s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "start", ExpectedRevision: p.Revision}); !errors.Is(err, ErrPlanningBudget) {
		t.Fatal("unsupported strict budget silently relaxed", err)
	}
	for _, other := range actors[1:] {
		if _, err = s.Planning(ctx, other, c.ID, p.ID, 0); !errors.Is(err, ErrDenied) {
			t.Fatal("private plan disclosed", err)
		}
	}
	events, err := s.Events(ctx, actors[1], c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if strings.HasPrefix(e.Kind, "planning.") {
			t.Fatal("private plan event leaked")
		}
	}
	if _, err = s.CreatePlanning(ctx, actors[2], c.ID, uuid.NewString(), input); !errors.Is(err, ErrDenied) {
		t.Fatal("viewer created plan", err)
	}
	input.Title = "Changed"
	if _, err = s.CreatePlanning(ctx, a, c.ID, key, input); !errors.Is(err, ErrConflict) {
		t.Fatal("changed create reused receipt", err)
	}
}

func TestPlanningPauseCancelRestartAndLateCompletion(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	ctx := context.Background()
	a := actors[0]
	p := createAndStartPlanning(t, s, a, c, input)
	r := claimPlanning(t, s, a, c, input)
	p, err := s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "pause", ExpectedRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); !errors.Is(err, ErrTransition) {
		t.Fatal("paused plan started a call", err)
	}
	if _, err = s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "release", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	if claimPlanning(t, s, a, c, input) != nil {
		t.Fatal("paused plan was claimed")
	}
	p, err = s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "resume", ExpectedRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	r = claimPlanning(t, s, a, c, input)
	if _, err = s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	input.Brief = "Revised brief"
	old := p.ID
	p, err = s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &input})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != old || p.ActiveVersion != 2 || p.Version.State != "draft" || len(p.Version.Steps) != 0 {
		t.Fatalf("restart changed ticket or reused calls: %+v", p)
	}
	if _, err = s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Late first version completion", Output: "Old output"}); err != nil {
		t.Fatal(err)
	}
	current, err := s.Planning(ctx, a, c.ID, p.ID, 0)
	if err != nil || current.Version.State != "draft" || len(current.Version.Steps) != 0 {
		t.Fatal("late result advanced new version", err)
	}
	previous, err := s.Planning(ctx, a, c.ID, p.ID, 1)
	if err != nil || previous.Version.State != "cancelled" {
		t.Fatal("old history lost", err)
	}
	p, err = s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "start", ExpectedRevision: current.Revision})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "cancel", ExpectedRevision: p.Revision})
	if err != nil || p.Version.State != "cancelled" {
		t.Fatal("cancel failed", err)
	}
	if claimPlanning(t, s, a, c, input) != nil {
		t.Fatal("cancelled plan was claimed")
	}
}

func TestPlanningRevocationCancelsAllSteps(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	ctx := context.Background()
	a := actors[0]
	source, err := s.Session(ctx, a, c.ID, input.Planners[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.WriteTranscript(ctx, a, c.ID, source.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "m", Ordinal: 0, Role: "assistant", Text: "Synthetic source"}}}); err != nil {
		t.Fatal(err)
	}
	input.Sources = []SourceRef{{SessionID: source.ID, TranscriptRevision: 1, PermissionRevision: source.PermissionRevision, MessageIDs: []string{"m"}}}
	p := createAndStartPlanning(t, s, a, c, input)
	update := SessionInput{ID: source.ID, ProjectID: source.ProjectID, ProfileID: source.ProfileID, LocalSessionID: source.LocalSessionID, Title: source.Title, Model: source.Model, State: source.State, Stage: source.Stage, Shared: false, AllowRequests: false, ExpectedRevision: source.Revision + 1}
	// Writing the source transcript increments the session metadata revision.
	source, err = s.Session(ctx, a, c.ID, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	update.ExpectedRevision = source.Revision
	if _, err = s.PublishSession(ctx, a, c.ID, uuid.NewString(), update); err != nil {
		t.Fatal(err)
	}
	p, err = s.Planning(ctx, a, c.ID, p.ID, 0)
	if err != nil || p.Version.State != "failed" {
		t.Fatal("revocation did not fail plan", err)
	}
	for _, step := range p.Version.Steps {
		if step.State != "cancelled" {
			t.Fatal("remaining step not cancelled", step.State)
		}
	}
	if claimPlanning(t, s, a, c, input) != nil {
		t.Fatal("revoked plan was claimed")
	}
	input.Shared = true
	input.Sources[0].PermissionRevision = 0
	if _, err = s.CreatePlanning(ctx, a, c.ID, uuid.NewString(), input); !errors.Is(err, ErrDenied) {
		t.Fatal("shared plan accepted a private source", err)
	}
}

func TestPlanningUnknownOutcomeRequiresReconciliation(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	ctx := context.Background()
	a := actors[0]
	p := createAndStartPlanning(t, s, a, c, input)
	r := claimPlanning(t, s, a, c, input)
	if _, err := s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReapRequests(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.Planning(ctx, a, c.ID, p.ID, 0)
	if err != nil || p.Version.State != "needs_review" {
		t.Fatal("unknown outcome hidden", err)
	}
	if claimPlanning(t, s, a, c, input) != nil {
		t.Fatal("unknown outcome allowed further calls")
	}
	events, err := s.Events(ctx, actors[1], c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(string(event.Payload), p.ID) {
			t.Fatal("private planning event leaked after lease expiry")
		}
	}
	if _, err = s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "reconcile", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Reviewed recorded provider output", Output: "Reconciled original output"}); err != nil {
		t.Fatal(err)
	}
	if claimPlanning(t, s, a, c, input) == nil {
		t.Fatal("reconciled plan did not resume")
	}
}

func TestPlanningClaimsAtMostOneCallPerProvider(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	a := actors[0]
	ctx := context.Background()
	createAndStartPlanning(t, s, a, c, input)
	second := input
	second.Planners = append([]PlannerInput(nil), input.Planners...)
	for i, planner := range input.Planners {
		old, err := s.Session(ctx, a, c.ID, planner.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		session, err := s.PublishSession(ctx, a, c.ID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: old.ProjectID, ProfileID: old.ProfileID, LocalSessionID: uuid.NewString(), Title: "Another anchor", State: "idle", Stage: "Ready", Shared: true, AllowRequests: true})
		if err != nil {
			t.Fatal(err)
		}
		second.Planners[i].SessionID = session.ID
	}
	createAndStartPlanning(t, s, a, c, second)
	sessions := []string{input.Planners[0].SessionID, input.Planners[1].SessionID, second.Planners[0].SessionID, second.Planners[1].SessionID}
	first, err := s.ClaimPlanningRequest(ctx, a, c.ID, uuid.NewString(), sessions)
	if err != nil || first == nil {
		t.Fatal("first claim", err)
	}
	next, err := s.ClaimPlanningRequest(ctx, a, c.ID, uuid.NewString(), sessions)
	if err != nil || next == nil {
		t.Fatal("second provider claim", err)
	}
	firstSession, _ := s.Session(ctx, a, c.ID, first.SessionID)
	secondSession, _ := s.Session(ctx, a, c.ID, next.SessionID)
	if firstSession.Provider == secondSession.Provider {
		t.Fatal("concurrent calls on one provider")
	}
	if extra, err := s.ClaimPlanningRequest(ctx, a, c.ID, uuid.NewString(), sessions); err != nil || extra != nil {
		t.Fatal("third active planning call", extra, err)
	}
}

func TestPlanningRejectsChangedAssignmentsAndInvalidOutput(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	a := actors[0]
	ctx := context.Background()
	input.Planners[1].Speed = "ultrafast"
	if _, err := s.CreatePlanning(ctx, a, c.ID, uuid.NewString(), input); !errors.Is(err, ErrInput) {
		t.Fatal("unsupported Claude speed accepted", err)
	}
	input.Planners[1].Speed = "standard"
	p := createAndStartPlanning(t, s, a, c, input)
	r := claimPlanning(t, s, a, c, input)
	if _, err := s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "missing output"}); !errors.Is(err, ErrInput) {
		t.Fatal("empty successful planning output", err)
	}
	if _, err := s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "large output", Output: strings.Repeat("x", 8193)}); !errors.Is(err, ErrInput) {
		t.Fatal("unbounded planning output", err)
	}
	p, err := s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "cancel", ExpectedRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if p.Version.State != "cancelled" {
		t.Fatal("cancel state not retained")
	}
}

func TestOptInWorkerClaimsPlanningWithoutChangingLegacyClaims(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	ctx := context.Background()
	a := actors[0]
	plan := createAndStartPlanning(t, s, a, c, input)
	ids := []string{input.Planners[0].SessionID, input.Planners[1].SessionID}
	if r, err := s.ClaimAvailableRequest(ctx, a, c.ID, uuid.NewString(), ids); err != nil || r != nil {
		t.Fatal("legacy worker received planning", r, err)
	}
	if r, err := s.ClaimAvailableWork(ctx, a, c.ID, uuid.NewString(), []string{}); err != nil || r != nil {
		t.Fatal("empty eligibility claimed work", r, err)
	}
	for i := 0; i < 5; i++ {
		key := uuid.NewString()
		r, err := s.ClaimAvailableWork(ctx, a, c.ID, key, ids)
		if err != nil || r == nil || r.Kind != "planning" || r.PlanID != plan.ID {
			t.Fatal("mixed claim", r, err)
		}
		replay, err := s.ClaimAvailableWork(ctx, a, c.ID, key, ids)
		if err != nil || replay == nil || replay.ID != r.ID || replay.LeaseToken != r.LeaseToken {
			t.Fatal("claim replay", replay, err)
		}
		if _, err = s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Synthetic completion", Output: "Synthetic mixed-worker result"}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Planning(ctx, a, c.ID, plan.ID, 1)
	if err != nil || result.Version.State != "ready" || len(result.Version.Steps) != 5 {
		t.Fatal("plan completion", result, err)
	}
}

func TestCreateAndRestartPlanningAtomicallyStart(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	ctx := context.Background()
	a := actors[0]
	input.Start = true
	key := uuid.NewString()
	p, err := s.CreatePlanning(ctx, a, c.ID, key, input)
	if err != nil || p.Version.State != "running" || len(p.Version.Steps) != 2 {
		t.Fatal("atomic create/start", p, err)
	}
	replay, err := s.CreatePlanning(ctx, a, c.ID, key, input)
	if err != nil || replay.ID != p.ID || len(replay.Version.Steps) != 2 {
		t.Fatal("create replay duplicated work", replay, err)
	}
	bad := input
	bad.BudgetPolicy = "strict_tokens"
	if _, err = s.CreatePlanning(ctx, a, c.ID, uuid.NewString(), bad); !errors.Is(err, ErrPlanningBudget) {
		t.Fatal("strict budget started", err)
	}
	before, err := s.ListPlanning(ctx, a, c.ID)
	if err != nil || len(before) != 1 {
		t.Fatal("failed admission created a card", before, err)
	}
	p, err = s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &input})
	if err != nil || p.ActiveVersion != 2 || p.Version.State != "running" || len(p.Version.Steps) != 2 {
		t.Fatal("atomic restart/start", p, err)
	}
	old, err := s.Planning(ctx, a, c.ID, p.ID, 1)
	if err != nil || old.Version.State != "cancelled" {
		t.Fatal("restart lost old history", old, err)
	}
}

func TestCancelPlanningPinsVersionWhileStepsAdvance(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	ctx := context.Background()
	a := actors[0]
	p := createAndStartPlanning(t, s, a, c, input)
	initial := p.Revision
	r := claimPlanning(t, s, a, c, input)
	if _, err := s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActOnRequest(ctx, a, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Synthetic", Output: "A draft"}); err != nil {
		t.Fatal(err)
	}
	p, err := s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "cancel", ExpectedRevision: initial, ExpectedVersion: 1})
	if err != nil || p.Version.State != "cancelled" {
		t.Fatal("progress prevented cancellation", p, err)
	}
	p, err = s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &input})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnPlanning(ctx, a, c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "cancel", ExpectedRevision: p.Revision, ExpectedVersion: 1}); !errors.Is(err, ErrRevision) {
		t.Fatal("old cancellation affected new version", err)
	}
}

func TestPlanningReceiptsDoNotBypassRevokedSourceAccess(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	ctx := context.Background()
	creator, owner := actors[1], actors[0]
	input.Shared = true
	createKey, startKey := uuid.NewString(), uuid.NewString()
	p, err := s.CreatePlanning(ctx, creator, c.ID, createKey, input)
	if err != nil {
		t.Fatal(err)
	}
	start := PlanningAction{Action: "start", ExpectedRevision: p.Revision}
	if _, err = s.ActOnPlanning(ctx, creator, c.ID, p.ID, startKey, start); err != nil {
		t.Fatal(err)
	}
	source, err := s.Session(ctx, owner, c.ID, input.Planners[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	update := SessionInput{ID: source.ID, ProjectID: source.ProjectID, ProfileID: source.ProfileID, LocalSessionID: source.LocalSessionID, Title: source.Title, Model: source.Model, State: source.State, Stage: source.Stage, Shared: false, AllowRequests: false, ExpectedRevision: source.Revision}
	if _, err = s.PublishSession(ctx, owner, c.ID, uuid.NewString(), update); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreatePlanning(ctx, creator, c.ID, createKey, input); !errors.Is(err, ErrDenied) {
		t.Fatal("create receipt disclosed revoked source", err)
	}
	if _, err = s.ActOnPlanning(ctx, creator, c.ID, p.ID, startKey, start); !errors.Is(err, ErrDenied) {
		t.Fatal("action receipt disclosed revoked source", err)
	}
	if _, err = s.Planning(ctx, creator, c.ID, p.ID, 1); !errors.Is(err, ErrDenied) {
		t.Fatal("version disclosed revoked source", err)
	}
	list, err := s.ListPlanning(ctx, creator, c.ID)
	if err != nil || len(list) != 0 {
		t.Fatal("board disclosed revoked source", list, err)
	}
}
