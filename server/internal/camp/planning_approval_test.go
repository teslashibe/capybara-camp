package camp

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func readyApprovalFixture(t *testing.T, remote bool) (*Store, []auth.Actor, Camp, PlanningTicket, Session, PlanningApprovalInput) {
	t.Helper()
	s, actors, c, in := planningFixture(t)
	in.Shared = true
	plan := createAndStartPlanning(t, s, actors[0], c, in)
	for range 5 {
		finishPlanningFixture(t, s, actors[0], c.ID, claimPlanning(t, s, actors[0], c, in))
	}
	plan, err := s.Planning(t.Context(), actors[0], c.ID, plan.ID, 0)
	if err != nil || plan.Version.State != "ready" {
		t.Fatal("plan not ready", err)
	}
	owner := actors[0]
	if remote {
		owner = actors[1]
	}
	profile, err := s.RegisterProfile(t.Context(), owner, uuid.NewString(), ProfileInput{Provider: "Codex", LocalID: strings.Repeat("c", 64), Label: "Independent executor"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.PublishSession(t.Context(), owner, c.ID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: plan.ProjectID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: "Executor fixture", State: "idle", Stage: "Ready", Shared: true, AllowRequests: true})
	if err != nil {
		t.Fatal(err)
	}
	assignment := planningExecution(target)
	return s, actors, c, plan, target, PlanningApprovalInput{ExpectedRevision: plan.Revision, ExpectedVersion: plan.ActiveVersion, Executor: PlannerInput{SessionID: target.ID, Model: "different-model", Effort: "high", Speed: "standard", Execution: &assignment}, ExpectedSessionRevision: target.Revision, ExpectedTranscriptRevision: target.TranscriptRevision, Permissions: localcli.PermissionSettings{"sandbox": "workspace-write", "approval": "on-request"}}
}
func TestPlanningApprovalBindsIndependentExecutorWithoutStartingWork(t *testing.T) {
	s, actors, c, plan, target, in := readyApprovalFixture(t, true)
	creator, owner := actors[0], actors[1]
	key := uuid.NewString()
	value, err := s.PreparePlanningApproval(t.Context(), creator, c.ID, plan.ID, key, in)
	if err != nil {
		t.Fatal(err)
	}
	if value.PlanID != plan.ID || value.Model != "different-model" || value.ProfileID != target.ProfileID || value.State != "pending" || !strings.Contains(value.Prompt, "Preserved fixture draft") {
		t.Fatal("wrong frozen selection")
	}
	retry, err := s.PreparePlanningApproval(t.Context(), creator, c.ID, plan.ID, key, in)
	if err != nil || retry.ID != value.ID {
		t.Fatal("prepare replay", err)
	}
	changed := in
	changed.Executor.Model = "changed-model"
	if _, err = s.PreparePlanningApproval(t.Context(), creator, c.ID, plan.ID, key, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed selection replay", err)
	}
	if _, err = s.PreparePlanningApproval(t.Context(), creator, c.ID, plan.ID, uuid.NewString(), in); !errors.Is(err, ErrConflict) {
		t.Fatal("parallel approval", err)
	}
	action := PlanningApprovalAction{ExpectedRevision: 1, ExecutionContext: strings.Repeat("a", 64)}
	if _, err = s.ApprovePlanningExecution(t.Context(), creator, c.ID, plan.ID, value.ID, uuid.NewString(), action); !errors.Is(err, ErrDenied) {
		t.Fatal("creator bypassed owner", err)
	}
	otherDevice := owner
	otherDevice.DeviceID = uuid.NewString()
	if _, err = s.ApprovePlanningExecution(t.Context(), otherDevice, c.ID, plan.ID, value.ID, uuid.NewString(), action); !errors.Is(err, ErrDenied) {
		t.Fatal("wrong local device approved", err)
	}
	approveKey := uuid.NewString()
	approved, err := s.ApprovePlanningExecution(t.Context(), owner, c.ID, plan.ID, value.ID, approveKey, action)
	if err != nil || approved.State != "approved" || approved.Revision != 2 || approved.ExecutionContext != action.ExecutionContext {
		t.Fatal("approval", err)
	}
	replay, err := s.ApprovePlanningExecution(t.Context(), owner, c.ID, plan.ID, value.ID, approveKey, action)
	if err != nil || replay.Revision != approved.Revision {
		t.Fatal("approval replay", err)
	}
	if _, err = s.PlanningApproval(t.Context(), actors[2], c.ID, plan.ID, value.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("third member read approval", err)
	}
	events, err := s.Events(t.Context(), actors[2], c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(string(event.Payload), value.ID) {
			t.Fatal("approval event exposed to another member")
		}
	}
	var count int
	if err = s.pool.QueryRow(t.Context(), `SELECT count(*) FROM prompt_requests WHERE session_id=$1`, target.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("approval dispatched a provider turn", err)
	}
	after, err := s.Planning(t.Context(), creator, c.ID, plan.ID, 0)
	hash, _ := planApprovalHash(after)
	if err != nil || after.Revision != plan.Revision || hash != value.PlanHash {
		t.Fatal("approval changed plan history", err)
	}
	if err = s.CancelPlanningApproval(t.Context(), creator, c.ID, plan.ID, value.ID, uuid.NewString(), 2); err != nil {
		t.Fatal(err)
	}
}
func TestPlanningApprovalRechecksRevokedSourcesAndAllowsWithdrawal(t *testing.T) {
	s, actors, c, plan, _, in := readyApprovalFixture(t, true)
	value, err := s.PreparePlanningApproval(t.Context(), actors[0], c.ID, plan.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.Session(t.Context(), actors[0], c.ID, plan.Version.Planners[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	update := SessionInput{ID: source.ID, ProjectID: source.ProjectID, ProfileID: source.ProfileID, LocalSessionID: source.LocalSessionID, Title: source.Title, State: "idle", Stage: "Ready", Shared: false, AllowRequests: true, ExpectedRevision: source.Revision}
	source, err = s.PublishSession(t.Context(), actors[0], c.ID, uuid.NewString(), update)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PlanningApproval(t.Context(), actors[1], c.ID, plan.ID, value.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked source exposed saved prompt", err)
	}
	statuses, err := s.PlanningApprovalStatuses(t.Context(), actors[1], c.ID, plan.ID)
	if err != nil || len(statuses) != 1 || !statuses[0].ReviewRequired {
		t.Fatal("withdrawal status unavailable", err)
	}
	// Sharing again must not revive approval tied to the previous grant.
	update.Shared = true
	update.ExpectedRevision = source.Revision
	if _, err = s.PublishSession(t.Context(), actors[0], c.ID, uuid.NewString(), update); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApprovePlanningExecution(t.Context(), actors[1], c.ID, plan.ID, value.ID, uuid.NewString(), PlanningApprovalAction{ExpectedRevision: 1, ExecutionContext: strings.Repeat("a", 64)}); !errors.Is(err, ErrDenied) {
		t.Fatal("reshare revived approval", err)
	}
	if err = s.CancelPlanningApproval(t.Context(), actors[1], c.ID, plan.ID, value.ID, uuid.NewString(), 1); err != nil {
		t.Fatal("withdraw after revocation", err)
	}
}
func TestPlanningApprovalConcurrentReplayAndChangedTarget(t *testing.T) {
	s, actors, c, plan, target, in := readyApprovalFixture(t, false)
	key := uuid.NewString()
	results := make(chan PlanningApproval, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.PreparePlanningApproval(t.Context(), actors[0], c.ID, plan.ID, key, in)
			if err != nil {
				t.Error(err)
			}
			results <- v
		}()
	}
	wg.Wait()
	close(results)
	var first PlanningApproval
	for v := range results {
		if first.ID == "" {
			first = v
		}
		if v.ID != first.ID {
			t.Fatal("concurrent prepare duplicated approval")
		}
	}
	if _, err := s.WriteTranscript(t.Context(), actors[0], c.ID, target.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "new", Ordinal: 0, Role: "assistant", Text: "Changed executor context"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePlanningExecution(t.Context(), actors[0], c.ID, plan.ID, first.ID, uuid.NewString(), PlanningApprovalAction{ExpectedRevision: 1, ExecutionContext: strings.Repeat("a", 64)}); !errors.Is(err, ErrRevision) {
		t.Fatal("changed target approved", err)
	}
}
func TestPlanningApprovalRejectsUnreadyPlanAndUnsupportedSelection(t *testing.T) {
	s, actors, c, plan, _, in := readyApprovalFixture(t, false)
	for _, change := range []func(*PlanningApprovalInput){func(v *PlanningApprovalInput) { v.ExpectedRevision++ }, func(v *PlanningApprovalInput) { v.ExpectedVersion++ }, func(v *PlanningApprovalInput) { v.ExpectedSessionRevision++ }, func(v *PlanningApprovalInput) {
		copy := *v.Executor.Execution
		copy.Epoch++
		v.Executor.Execution = &copy
	}, func(v *PlanningApprovalInput) {
		v.Permissions = localcli.PermissionSettings{"claudeMode": "bypassPermissions"}
	}} {
		bad := in
		change(&bad)
		if _, err := s.PreparePlanningApproval(t.Context(), actors[0], c.ID, plan.ID, uuid.NewString(), bad); err == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	if _, err := s.pool.Exec(t.Context(), `UPDATE planning_versions SET state='failed' WHERE plan_id=$1`, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreparePlanningApproval(t.Context(), actors[0], c.ID, plan.ID, uuid.NewString(), in); !errors.Is(err, ErrTransition) {
		t.Fatal("failed plan accepted", err)
	}
}

func TestPlanningApprovalCloudAssignmentAndPermissionBinding(t *testing.T) {
	g, w, _, owner, assignment := executionFixture(t, "implementation")
	s := g.store
	assignment.ExecutionApproved = true
	assignment.Permissions = localcli.PermissionSettings{"sandbox": "workspace-write", "approval": "on-request", "reviewer": "user"}
	if _, err := s.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), assignment); err != nil {
		t.Fatal(err)
	}
	target, err := s.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// Planning does not need to run in the executor workspace: a separately
	// completed local plan can select this prepared implementation destination.
	profile, err := s.RegisterProfile(t.Context(), owner, uuid.NewString(), ProfileInput{Provider: "Codex", LocalID: strings.Repeat("d", 64), Label: "Local planner"})
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := s.PublishSession(t.Context(), owner, w.CampID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: target.ProjectID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: "Planner", State: "idle", Stage: "Ready", Shared: true, AllowRequests: true})
	if err != nil {
		t.Fatal(err)
	}
	input := PlanningInput{Title: "Cloud implementation", ProjectID: target.ProjectID, Mode: "single", Brief: "Plan a bounded change", BudgetPolicy: "calls", Start: true, Planners: []PlannerInput{{SessionID: anchor.ID, Model: "fixture"}}}
	p, err := s.CreatePlanning(t.Context(), owner, w.CampID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ClaimPlanningRequest(t.Context(), owner, w.CampID, uuid.NewString(), []string{anchor.ID})
	if err != nil {
		t.Fatal(err)
	}
	finishPlanningFixture(t, s, owner, w.CampID, r)
	p, err = s.Planning(t.Context(), owner, w.CampID, p.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	execution := planningExecution(target)
	in := PlanningApprovalInput{ExpectedRevision: p.Revision, ExpectedVersion: p.ActiveVersion, ExpectedSessionRevision: target.Revision, ExpectedTranscriptRevision: target.TranscriptRevision, Executor: PlannerInput{SessionID: target.ID, Model: "new-executor-model", Speed: "standard", Execution: &execution}, Permissions: assignment.Permissions}
	bad := in
	bad.Permissions = localcli.PermissionSettings{"sandbox": "danger-full-access", "approval": "never"}
	if _, err = s.PreparePlanningApproval(t.Context(), owner, w.CampID, p.ID, uuid.NewString(), bad); !errors.Is(err, ErrRevision) {
		t.Fatal("widened workspace permissions", err)
	}
	approval, err := s.PreparePlanningApproval(t.Context(), owner, w.CampID, p.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := s.ApprovePlanningExecution(t.Context(), owner, w.CampID, p.ID, approval.ID, uuid.NewString(), PlanningApprovalAction{ExpectedRevision: 1})
	if err != nil || approved.State != "approved" || approved.ExecutionContext != "" {
		t.Fatal("cloud approval", err)
	}
	// Runtime login/workspace availability is rechecked even after consent.
	if _, err = s.pool.Exec(t.Context(), `UPDATE provider_installations SET epoch=epoch+1 WHERE id=$1`, w.ProviderInstallationID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PlanningApproval(t.Context(), owner, w.CampID, p.ID, approval.ID); !errors.Is(err, ErrTransition) {
		t.Fatal("stale installation accepted", err)
	}
}

func TestPlanningApprovalRestartInvalidatesPriorConsent(t *testing.T) {
	s, actors, c, p, _, in := readyApprovalFixture(t, false)
	approval, err := s.PreparePlanningApproval(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	action := PlanningApprovalAction{ExpectedRevision: 1, ExecutionContext: strings.Repeat("a", 64)}
	approval, err = s.ApprovePlanningExecution(t.Context(), actors[0], c.ID, p.ID, approval.ID, uuid.NewString(), action)
	if err != nil {
		t.Fatal(err)
	}
	planners := []PlannerInput{}
	for _, planner := range p.Version.Planners {
		planners = append(planners, planner.PlannerInput)
	}
	revised := PlanningInput{Title: p.Title, ProjectID: p.ProjectID, Mode: p.Version.Mode, Brief: "A different feature", BudgetPolicy: "calls", Shared: p.Shared, Planners: planners, Sources: p.Version.Sources, MergeSlot: p.Version.MergeSlot}
	if _, err = s.ActOnPlanning(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &revised}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PlanningApproval(t.Context(), actors[0], c.ID, p.ID, approval.ID); !errors.Is(err, ErrRevision) {
		t.Fatal("old approval survived plan restart", err)
	}
	statuses, err := s.PlanningApprovalStatuses(t.Context(), actors[0], c.ID, p.ID)
	if err != nil || len(statuses) != 1 || !statuses[0].ReviewRequired {
		t.Fatal("stale approval not surfaced", err)
	}
	if err = s.CancelPlanningApproval(t.Context(), actors[0], c.ID, p.ID, approval.ID, uuid.NewString(), 2); err != nil {
		t.Fatal(err)
	}
}
