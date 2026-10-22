package camp

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"testing"
)

func TestLocalPlanningStreamPrivacyReplayAndLease(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	owner, member := actors[0], actors[1]
	p := createAndStartPlanning(t, s, owner, c, input)
	r := claimPlanning(t, s, owner, c, input)
	if _, err := s.ActOnRequest(t.Context(), owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	event := LocalPlanningEvent{StreamEvent: StreamEvent{Seq: 1, Event: json.RawMessage(`{"type":"text","text":"Draft still streaming"}`)}, LeaseToken: r.LeaseToken}
	for range 2 {
		if seq, err := s.AppendLocalPlanningEvent(t.Context(), owner, c.ID, r.ID, event); err != nil || seq != 1 {
			t.Fatal(seq, err)
		}
	}
	if head, err := s.PlanningStream(t.Context(), owner, c.ID, p.ID, 1, r.PlanStep); err != nil || head == nil || head.Seq != 1 || head.State != "started" {
		t.Fatal(head, err)
	}
	page, err := s.RequestStream(t.Context(), owner, c.ID, r.ID, 0)
	if err != nil || len(page.Events) != 1 {
		t.Fatal(page, err)
	}
	if _, err = s.PlanningStream(t.Context(), member, c.ID, p.ID, 1, r.PlanStep); !errors.Is(err, ErrDenied) {
		t.Fatal("private plan stream leaked", err)
	}
	if _, err = s.RequestStream(t.Context(), member, c.ID, r.ID, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("private event contents leaked through anchor", err)
	}
	if head, err := s.SessionStream(t.Context(), member, c.ID, r.SessionID); err != nil || head != nil {
		t.Fatal("planning head leaked through chat", head, err)
	}
	wrongDevice := owner
	wrongDevice.DeviceID = uuid.NewString()
	if _, err = s.AppendLocalPlanningEvent(t.Context(), wrongDevice, c.ID, r.ID, event); !errors.Is(err, ErrDenied) {
		t.Fatal("wrong device replay", err)
	}
	changed := event
	changed.Event = json.RawMessage(`{"type":"text","text":"Changed"}`)
	if _, err = s.AppendLocalPlanningEvent(t.Context(), owner, c.ID, r.ID, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay", err)
	}
	changed.Seq = 3
	if _, err = s.AppendLocalPlanningEvent(t.Context(), owner, c.ID, r.ID, changed); !errors.Is(err, ErrTransition) {
		t.Fatal("sequence gap", err)
	}
	settings := event
	settings.Seq = 2
	settings.Event = json.RawMessage(`{"type":"settings","model":"reported","effort":"ultra","speed":"fast"}`)
	if _, err = s.AppendLocalPlanningEvent(t.Context(), owner, c.ID, r.ID, settings); err != nil {
		t.Fatal(err)
	}
	if head, err := s.PlanningStream(t.Context(), owner, c.ID, p.ID, 1, r.PlanStep); err != nil || head.Model != "reported" || head.Effort != "ultra" || head.Speed != "fast" {
		t.Fatal("reported settings lost", head, err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE planning_tickets SET shared=true WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestStream(t.Context(), member, c.ID, r.ID, 0); err != nil {
		t.Fatal("shared plan stream unavailable", err)
	}
	// Losing any planner source hides the complete derived output, including old pages.
	if _, err = s.pool.Exec(t.Context(), `UPDATE sessions SET shared=false WHERE id=$1`, input.Planners[1].SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestStream(t.Context(), member, c.ID, r.ID, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked planner source leaked", err)
	}
	if _, err = s.PlanningStream(t.Context(), member, c.ID, p.ID, 1, r.PlanStep); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked source head leaked", err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	changed.Seq = 3
	if _, err = s.AppendLocalPlanningEvent(t.Context(), owner, c.ID, r.ID, changed); !errors.Is(err, ErrTransition) {
		t.Fatal("expired lease wrote", err)
	}
}

func TestCloudPlanningStreamDoesNotLeakThroughSharedAnchor(t *testing.T) {
	g, w, grant, owner, assignment := executionFixture(t, "planning")
	s := g.store
	var before int
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM messages WHERE session_id=$1`, w.SessionID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), assignment); err != nil {
		t.Fatal(err)
	}
	plan, err := s.CreatePlanning(t.Context(), owner, w.CampID, uuid.NewString(), PlanningInput{Mode: "single", Title: "Private cloud plan", ProjectID: w.ProjectID, Brief: "Private planning brief", BudgetPolicy: "calls", Start: true, Planners: []PlannerInput{{SessionID: w.SessionID, Model: "fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, uuid.NewString())
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	appendFixtureEvent(t, g, w, grant, *r, 1, map[string]any{"type": "delta", "text": "Private draft"})
	var member auth.Actor
	if err = s.pool.QueryRow(t.Context(), `SELECT user_id FROM memberships WHERE camp_id=$1 AND user_id<>$2 LIMIT 1`, w.CampID, owner.UserID).Scan(&member.UserID); err != nil {
		t.Fatal(err)
	}
	if head, err := s.SessionStream(t.Context(), member, w.CampID, w.SessionID); err != nil || head != nil {
		t.Fatal("private planning request exposed in chat", head, err)
	}
	if _, err = s.RequestStream(t.Context(), member, w.CampID, r.ID, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("private planning text leaked", err)
	}
	if head, err := s.PlanningStream(t.Context(), owner, w.CampID, plan.ID, 1, 0); err != nil || head == nil || head.Seq != 1 {
		t.Fatal("owner cannot see live plan", head, err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE planning_tickets SET shared=true WHERE id=$1`, plan.ID); err != nil {
		t.Fatal(err)
	}
	if head, err := s.PlanningStream(t.Context(), member, w.CampID, plan.ID, 1, 0); err != nil || head == nil || head.Seq != 1 {
		t.Fatal("shared planning stream unavailable", head, err)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Output: "Saved plan", Evidence: "Synthetic planning completion"}); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM messages WHERE session_id=$1`, w.SessionID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("isolated plan was appended to the anchor transcript", before, after)
	}
}
