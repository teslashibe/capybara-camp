package camp

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func approvedImplementationFixture(t *testing.T, remote bool) (*Store, []auth.Actor, Camp, PlanningTicket, Session, PlanningApproval) {
	t.Helper()
	s, actors, c, p, target, input := readyApprovalFixture(t, remote)
	approval, err := s.PreparePlanningApproval(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	owner := actors[0]
	if remote {
		owner = actors[1]
	}
	approval, err = s.ApprovePlanningExecution(t.Context(), owner, c.ID, p.ID, approval.ID, uuid.NewString(), PlanningApprovalAction{ExpectedRevision: approval.Revision, ExecutionContext: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	return s, actors, c, p, target, approval
}
func dispatchImplementationFixture(t *testing.T, s *Store, a auth.Actor, c Camp, p PlanningTicket, approval PlanningApproval) Request {
	t.Helper()
	r, err := s.DispatchPlanningExecution(t.Context(), a, c.ID, p.ID, approval.ID, uuid.NewString(), approval.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func localImplementationEvent(t *testing.T, s *Store, a auth.Actor, r Request, seq int64, event map[string]any) error {
	t.Helper()
	raw, _ := json.Marshal(event)
	_, err := s.AppendLocalImplementationEvent(t.Context(), a, r.CampID, r.ID, LocalPlanningEvent{StreamEvent: StreamEvent{Seq: seq, Event: raw}, LeaseToken: r.LeaseToken})
	return err
}
func TestImplementationDispatchReplayAndWorkerCapability(t *testing.T) {
	s, actors, c, p, target, approval := approvedImplementationFixture(t, true)
	if _, err := s.DispatchPlanningExecution(t.Context(), actors[2], c.ID, p.ID, approval.ID, uuid.NewString(), approval.Revision); !errors.Is(err, ErrDenied) {
		t.Fatal("third member dispatch", err)
	}
	key := uuid.NewString()
	results := make(chan Request, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.DispatchPlanningExecution(t.Context(), actors[0], c.ID, p.ID, approval.ID, key, approval.Revision)
			if err != nil {
				t.Error(err)
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	var first Request
	for r := range results {
		if first.ID == "" {
			first = r
		}
		if r.ID != first.ID {
			t.Fatal("duplicate dispatch")
		}
	}
	if first.Kind != "implementation" || first.Mode != "implementation" || first.State != "queued" || first.PlanID != p.ID || first.ImplementationID == "" || first.ProviderSessionID != "" || first.Model != approval.Model {
		t.Fatal("incorrect dispatch binding", first.Kind, first.State)
	}
	if _, err := s.DispatchPlanningExecution(t.Context(), actors[1], c.ID, p.ID, approval.ID, uuid.NewString(), approval.Revision); err == nil {
		t.Fatal("consumed approval reused")
	}
	if r, err := s.ClaimRequest(t.Context(), actors[1], c.ID, uuid.NewString()); err != nil || r != nil {
		t.Fatal("legacy chat worker claimed implementation", err)
	}
	if r, err := s.ClaimPlanningRequest(t.Context(), actors[1], c.ID, uuid.NewString(), []string{target.ID}); err != nil || r != nil {
		t.Fatal("legacy planning worker claimed implementation", err)
	}
	r, err := s.ClaimImplementationWork(t.Context(), actors[1], c.ID, uuid.NewString(), []string{target.ID})
	if err != nil || r == nil || r.ID != first.ID {
		t.Fatal("capable worker claim", err)
	}
	for _, fingerprint := range []string{"", strings.Repeat("b", 64)} {
		if _, err = s.ActOnRequest(t.Context(), actors[1], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken, ExecutionContext: fingerprint}); !errors.Is(err, ErrInput) {
			t.Fatal("unreviewed runtime started", err)
		}
	}
	startKey := uuid.NewString()
	start := RequestAction{Action: "start", LeaseToken: r.LeaseToken, ExecutionContext: approval.ExecutionContext}
	for range 2 {
		if _, err = s.ActOnRequest(t.Context(), actors[1], c.ID, r.ID, startKey, start); err != nil {
			t.Fatal("start/replay", err)
		}
	}
	// The selected account source remains an ordinary unchanged chat.
	if err = localImplementationEvent(t, s, actors[1], *r, 1, map[string]any{"type": "session", "sessionID": target.LocalSessionID}); !errors.Is(err, ErrConflict) {
		t.Fatal("anchor adopted", err)
	}
	providerID := uuid.NewString()
	if err = localImplementationEvent(t, s, actors[1], *r, 1, map[string]any{"type": "session", "sessionID": providerID}); err != nil {
		t.Fatal(err)
	}
	if err = localImplementationEvent(t, s, actors[1], *r, 2, map[string]any{"type": "session", "sessionID": uuid.NewString()}); !errors.Is(err, ErrConflict) {
		t.Fatal("provider identity changed", err)
	}
	if err = localImplementationEvent(t, s, actors[1], *r, 2, map[string]any{"type": "delta", "text": "Implemented the approved change"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[1], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Synthetic implementation fixture"}); err != nil {
		t.Fatal(err)
	}
	impl, err := s.Implementation(t.Context(), actors[0], c.ID, p.ID, r.ImplementationID)
	if err != nil || impl.ProviderSessionID != providerID || len(impl.Requests) != 1 || impl.Requests[0].State != "completed" {
		t.Fatal("implementation history", err)
	}
	anchor, err := s.Session(t.Context(), actors[0], c.ID, target.ID)
	if err != nil || anchor.Revision != target.Revision || anchor.TranscriptRevision != target.TranscriptRevision {
		t.Fatal("anchor changed", err)
	}
	if head, err := s.SessionStream(t.Context(), actors[0], c.ID, target.ID); err != nil || head != nil {
		t.Fatal("implementation appeared as anchor chat", err)
	}
	if _, err = s.RequestStream(t.Context(), actors[2], c.ID, r.ID, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("third member read implementation", err)
	}
	if _, err = s.PublishSession(t.Context(), actors[1], c.ID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: target.ProjectID, ProfileID: target.ProfileID, LocalSessionID: providerID, Title: "Discovered implementation", State: "idle", Stage: "Ready"}); err != nil {
		t.Fatal("importer discovery", err)
	}
	next, err := s.ContinueImplementation(t.Context(), actors[0], c.ID, p.ID, r.ImplementationID, uuid.NewString(), RequestInput{Prompt: "Add a focused regression", Model: "follow-up-model"})
	if err != nil || next.State != "pending_approval" || next.ProviderSessionID != providerID {
		t.Fatal("remote follow-up bypassed approval or lost identity", err)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[1], c.ID, next.ID, uuid.NewString(), RequestAction{Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	nextClaim, err := s.ClaimImplementationWork(t.Context(), actors[1], c.ID, uuid.NewString(), []string{target.ID})
	if err != nil || nextClaim == nil || nextClaim.ID != next.ID || nextClaim.ProviderSessionID != providerID {
		t.Fatal("follow-up resume", err)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[1], c.ID, nextClaim.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: nextClaim.LeaseToken, ExecutionContext: approval.ExecutionContext}); err != nil {
		t.Fatal(err)
	}
	if err = localImplementationEvent(t, s, actors[1], *nextClaim, 1, map[string]any{"type": "session", "sessionID": providerID}); err != nil {
		t.Fatal("normal import invalidated existing implementation identity", err)
	}
}
func TestImplementationRevocationAndRestartRecovery(t *testing.T) {
	s, actors, c, p, target, approval := approvedImplementationFixture(t, true)
	initial := dispatchImplementationFixture(t, s, actors[0], c, p, approval)
	input := PlanningInput{Title: p.Title, ProjectID: p.ProjectID, Mode: p.Version.Mode, Brief: "Restart", BudgetPolicy: "calls", Shared: true, MergeSlot: p.Version.MergeSlot}
	for _, v := range p.Version.Planners {
		input.Planners = append(input.Planners, v.PlannerInput)
	}
	if _, err := s.ActOnPlanning(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &input}); !errors.Is(err, ErrTransition) {
		t.Fatal("restart discarded live implementation", err)
	}
	r, err := s.ClaimImplementationWork(t.Context(), actors[1], c.ID, uuid.NewString(), []string{target.ID})
	if err != nil || r == nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[1], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken, ExecutionContext: approval.ExecutionContext}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[1], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Missing conversation identity"}); !errors.Is(err, ErrTransition) {
		t.Fatal("finished without provider identity", err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE sessions SET shared=false,permission_revision=permission_revision+1 WHERE id=$1`, p.Version.Planners[0].SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Implementation(t.Context(), actors[1], c.ID, p.ID, initial.ImplementationID); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked history exposed", err)
	}
	if err = localImplementationEvent(t, s, actors[1], *r, 1, map[string]any{"type": "session", "sessionID": uuid.NewString()}); err == nil {
		t.Fatal("revoked context still running")
	}
	heartbeat, err := s.ActOnRequest(t.Context(), actors[1], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "heartbeat", LeaseToken: r.LeaseToken})
	if err != nil || !heartbeat.ContentRestricted || heartbeat.ExecutionContext != "" {
		t.Fatal("worker not informed", err)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[1], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "cancelled", Evidence: "Stopped after revocation"}); err != nil {
		t.Fatal("revocation prevented stop acknowledgement", err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE sessions SET shared=true WHERE id=$1`, p.Version.Planners[0].SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Implementation(t.Context(), actors[1], c.ID, p.ID, initial.ImplementationID); !errors.Is(err, ErrDenied) {
		t.Fatal("reshare revived old grant", err)
	}
}

func TestImplementationCloudConversationIsolation(t *testing.T) {
	g, w, grant, owner, assignment := executionFixture(t, "implementation")
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
	profile, err := s.RegisterProfile(t.Context(), owner, uuid.NewString(), ProfileInput{Provider: "Codex", LocalID: strings.Repeat("d", 64), Label: "Local planner"})
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := s.PublishSession(t.Context(), owner, w.CampID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: target.ProjectID, ProfileID: profile.ID, LocalSessionID: uuid.NewString(), Title: "Planner", State: "idle", Stage: "Ready", Shared: true, AllowRequests: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreatePlanning(t.Context(), owner, w.CampID, uuid.NewString(), PlanningInput{Title: "Implementation fixture", ProjectID: target.ProjectID, Mode: "single", Brief: "Make a small change", BudgetPolicy: "calls", Start: true, Planners: []PlannerInput{{SessionID: anchor.ID, Model: "fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ClaimPlanningRequest(t.Context(), owner, w.CampID, uuid.NewString(), []string{anchor.ID})
	if err != nil || r == nil {
		t.Fatal(err)
	}
	finishPlanningFixture(t, s, owner, w.CampID, r)
	p, err = s.Planning(t.Context(), owner, w.CampID, p.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	execution := planningExecution(target)
	approval, err := s.PreparePlanningApproval(t.Context(), owner, w.CampID, p.ID, uuid.NewString(), PlanningApprovalInput{ExpectedRevision: p.Revision, ExpectedVersion: p.ActiveVersion, ExpectedSessionRevision: target.Revision, ExpectedTranscriptRevision: target.TranscriptRevision, Executor: PlannerInput{SessionID: target.ID, Model: "fixture", Speed: "standard", Execution: &execution}, Permissions: assignment.Permissions})
	if err != nil {
		t.Fatal(err)
	}
	approval, err = s.ApprovePlanningExecution(t.Context(), owner, w.CampID, p.ID, approval.ID, uuid.NewString(), PlanningApprovalAction{ExpectedRevision: approval.Revision})
	if err != nil {
		t.Fatal(err)
	}
	dispatched := dispatchImplementationFixture(t, s, owner, Camp{ID: w.CampID}, p, approval)
	oldID := uuid.NewString()
	if _, err = s.pool.Exec(t.Context(), `INSERT INTO workspace_runtime(workspace_id,provider_session_id) VALUES($1,$2)`, w.ID, oldID); err != nil {
		t.Fatal(err)
	}
	if legacy, err := g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, uuid.NewString()); err != nil || legacy != nil {
		t.Fatal("old cloud worker claimed", err)
	}
	r, err = g.ClaimRunnerWork(t.Context(), grant.Token, w.ID, uuid.NewString(), true)
	if err != nil || r == nil || r.ID != dispatched.ID {
		t.Fatal("cloud claim", err)
	}
	turn, err := g.PagedRunnerTurn(t.Context(), grant.Token, w.ID, r.ID)
	if err != nil || turn.ProviderSessionID != "" || len(turn.History) != 0 || strings.Contains(string(turn.Context), "Original prompt") {
		t.Fatal("implementation adopted anchor context", err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"type": "session", "sessionID": oldID})
	if _, err = g.AppendRunnerEvent(t.Context(), grant.Token, w.ID, r.ID, StreamEvent{Seq: 1, Event: raw}); !errors.Is(err, ErrConflict) {
		t.Fatal("adopted workspace conversation", err)
	}
	newID := uuid.NewString()
	appendFixtureEvent(t, g, w, grant, *r, 1, map[string]any{"type": "session", "sessionID": newID})
	appendFixtureEvent(t, g, w, grant, *r, 2, map[string]any{"type": "text", "text": "Implemented fixture"})
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Fixture done"}); err != nil {
		t.Fatal(err)
	}
	var retained string
	if err = s.pool.QueryRow(t.Context(), `SELECT provider_session_id FROM workspace_runtime WHERE workspace_id=$1`, w.ID).Scan(&retained); err != nil || retained != oldID {
		t.Fatal("overwrote anchor runtime", err)
	}
	after, err := s.Session(t.Context(), owner, w.CampID, target.ID)
	if err != nil || after.TranscriptRevision != target.TranscriptRevision || after.Revision != target.Revision {
		t.Fatal("published implementation as anchor history", err)
	}
	next, err := s.ContinueImplementation(t.Context(), owner, w.CampID, p.ID, dispatched.ImplementationID, uuid.NewString(), RequestInput{Prompt: "Verify the change", Model: "fixture"})
	if err != nil || next.State != "queued" {
		t.Fatal("owner follow-up", err)
	}
	r, err = g.ClaimRunnerWork(t.Context(), grant.Token, w.ID, uuid.NewString(), true)
	if err != nil || r == nil {
		t.Fatal(err)
	}
	turn, err = g.RunnerTurn(t.Context(), grant.Token, w.ID, r.ID)
	if err != nil || turn.ProviderSessionID != newID {
		t.Fatal("wrong resumed provider conversation", err)
	}
}

func TestImplementationUnknownOutcomeRetainsHistoryAcrossRestart(t *testing.T) {
	s, actors, c, p, target, approval := approvedImplementationFixture(t, true)
	initial := dispatchImplementationFixture(t, s, actors[0], c, p, approval)
	r, err := s.ClaimImplementationWork(t.Context(), actors[1], c.ID, uuid.NewString(), []string{target.ID})
	if err != nil || r == nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[1], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken, ExecutionContext: approval.ExecutionContext}); err != nil {
		t.Fatal(err)
	}
	providerID := uuid.NewString()
	if err = localImplementationEvent(t, s, actors[1], *r, 1, map[string]any{"type": "session", "sessionID": providerID}); err != nil {
		t.Fatal(err)
	}
	if err = localImplementationEvent(t, s, actors[1], *r, 2, map[string]any{"type": "text", "text": "Partial result"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ReapRequests(t.Context()); err != nil {
		t.Fatal("lease recovery", err)
	}
	uncertain, err := s.Request(t.Context(), actors[0], c.ID, r.ID)
	if err != nil || uncertain.State != "outcome_unknown" || uncertain.ProviderSessionID != providerID {
		t.Fatal("lost uncertain conversation", err)
	}
	if retry, err := s.ClaimImplementationWork(t.Context(), actors[1], c.ID, uuid.NewString(), []string{target.ID}); err != nil || retry != nil {
		t.Fatal("uncertain work reran", err)
	}
	revised := PlanningInput{Title: p.Title, ProjectID: p.ProjectID, Mode: p.Version.Mode, Brief: "Second revision", BudgetPolicy: "calls", Shared: true, MergeSlot: p.Version.MergeSlot}
	for _, v := range p.Version.Planners {
		revised.Planners = append(revised.Planners, v.PlannerInput)
	}
	restart := PlanningAction{Action: "restart", ExpectedRevision: p.Revision, Input: &revised}
	if _, err = s.ActOnPlanning(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), restart); !errors.Is(err, ErrTransition) {
		t.Fatal("restart hid uncertain implementation", err)
	}
	if _, err = s.ActOnRequest(t.Context(), actors[1], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "reconcile", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Fixture outcome independently confirmed"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnPlanning(t.Context(), actors[0], c.ID, p.ID, uuid.NewString(), restart); err != nil {
		t.Fatal("restart after reconciliation", err)
	}
	impl, err := s.Implementation(t.Context(), actors[0], c.ID, p.ID, initial.ImplementationID)
	if err != nil || impl.ProviderSessionID != providerID || impl.PlanVersion != 1 || impl.Requests[0].State != "completed" {
		t.Fatal("restart lost old execution history", err)
	}
	if _, err = s.ContinueImplementation(t.Context(), actors[0], c.ID, p.ID, initial.ImplementationID, uuid.NewString(), RequestInput{Prompt: "Use old approval"}); !errors.Is(err, ErrRevision) {
		t.Fatal("old revision resumed without review", err)
	}
	stream, err := s.RequestStream(t.Context(), actors[0], c.ID, r.ID, 0)
	if err != nil || len(stream.Events) != 2 {
		t.Fatal("old version lost stream", err)
	}
}
