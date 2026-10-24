package camp

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func TestControlStatusSeparatesSubmissionFromProviderAcceptance(t *testing.T) {
	g, w, grant, owner, r := streamFixture(t)
	id := uuid.NewString()
	in := ControlInput{ID: id, Control: map[string]any{"type": "steer", "id": id, "text": "Change direction"}}
	if _, err := g.store.SendControl(t.Context(), owner, w.CampID, r.ID, in); err != nil {
		t.Fatal(err)
	}
	check := func(state string) {
		t.Helper()
		out, err := g.store.ControlStatus(t.Context(), owner, w.CampID, r.ID, id)
		if err != nil || out.ID != id || out.RequestID != r.ID || out.Kind != "steer" || out.State != state {
			t.Fatal(out, err)
		}
	}
	check("pending")
	if _, err := g.store.ControlStatus(t.Context(), auth.Actor{UserID: uuid.NewString()}, w.CampID, r.ID, id); err == nil {
		t.Fatal("foreign user read control status")
	}
	if _, err := g.store.ControlStatus(t.Context(), owner, w.CampID, r.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
		t.Fatal("missing control status", err)
	}
	appendFixtureEvent(t, g, w, grant, r, 1, map[string]any{"type": "steerAccepted", "requestID": id})
	check("accepted")
	// An identical transport retry returns the same receipt after settlement.
	if _, err := g.store.SendControl(t.Context(), owner, w.CampID, r.ID, in); err != nil {
		t.Fatal(err)
	}
	check("accepted")
	id = uuid.NewString()
	in = ControlInput{ID: id, Control: map[string]any{"type": "steer", "id": id, "text": "Unacknowledged direction"}}
	if _, err := g.store.SendControl(t.Context(), owner, w.CampID, r.ID, in); err != nil {
		t.Fatal(err)
	}
	appendFixtureEvent(t, g, w, grant, r, 2, map[string]any{"type": "done"})
	if _, err := g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Fixture result"}); err != nil {
		t.Fatal(err)
	}
	check("unconfirmed")
	if _, err := g.store.SendControl(t.Context(), owner, w.CampID, r.ID, in); err != nil {
		t.Fatal("same ID could not recover terminal receipt", err)
	}
	check("unconfirmed")
}

func TestControlStatusUsesMatchingProviderResolution(t *testing.T) {
	for _, kind := range []string{"steer", "approval"} {
		t.Run(kind, func(t *testing.T) {
			g, w, grant, owner, r := streamFixture(t)
			id := uuid.NewString()
			providerID := id
			in := ControlInput{ID: id, Control: map[string]any{"type": "steer", "id": id, "text": "New direction"}}
			seq := int64(1)
			if kind == "approval" {
				providerID = uuid.NewString()
				appendFixtureEvent(t, g, w, grant, r, seq, map[string]any{"type": "approval", "id": providerID, "kind": "command", "title": "Run fixture", "detail": "echo fixture", "questions": []any{}, "canApprove": true, "acceptLabel": "Allow"})
				seq++
				approvalSeq := int64(1)
				in = ControlInput{ID: id, ApprovalSeq: &approvalSeq, Control: map[string]any{"type": "approval", "id": providerID, "allow": true}}
			}
			if _, err := g.store.SendControl(t.Context(), owner, w.CampID, r.ID, in); err != nil {
				t.Fatal(err)
			}
			appendFixtureEvent(t, g, w, grant, r, seq, map[string]any{"type": kind + "Rejected", "requestID": providerID, "message": "Fixture rejected"})
			out, err := g.store.ControlStatus(t.Context(), owner, w.CampID, r.ID, id)
			if err != nil || out.State != "rejected" || out.Kind != kind {
				t.Fatal(out, err)
			}
			if kind == "approval" {
				later := in
				later.ID = uuid.NewString()
				if _, err = g.store.SendControl(t.Context(), owner, w.CampID, r.ID, later); err != nil {
					t.Fatal(err)
				}
				appendFixtureEvent(t, g, w, grant, r, seq+1, map[string]any{"type": "approvalResolved", "requestID": providerID})
				old, err := g.store.ControlStatus(t.Context(), owner, w.CampID, r.ID, id)
				if err != nil || old.State != "rejected" {
					t.Fatal("later approval changed rejected receipt", old, err)
				}
				current, err := g.store.ControlStatus(t.Context(), owner, w.CampID, r.ID, later.ID)
				if err != nil || current.State != "accepted" {
					t.Fatal(current, err)
				}
			}
		})
	}
}
