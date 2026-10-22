package camp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func streamFixture(t *testing.T) (*GitStore, Workspace, RunnerGrant, auth.Actor, Request) {
	t.Helper()
	g, w, grant, owner, input := executionFixture(t, "planning")
	if _, err := g.store.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	r, err := g.store.CreateRequest(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), RequestInput{Prompt: "Original prompt"})
	if err != nil {
		t.Fatal(err)
	}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	r, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claim})
	if err != nil {
		t.Fatal(err)
	}
	r.LeaseToken = claim
	return g, w, grant, owner, r
}
func appendFixtureEvent(t *testing.T, g *GitStore, w Workspace, grant RunnerGrant, r Request, seq int64, value any) {
	t.Helper()
	raw, _ := json.Marshal(value)
	if n, err := g.AppendRunnerEvent(t.Context(), grant.Token, w.ID, r.ID, StreamEvent{Seq: seq, Event: raw}); err != nil || n != seq {
		t.Fatal(n, err)
	}
}
func TestRunnerStreamReplayControlsAndTranscript(t *testing.T) {
	g, w, grant, owner, r := streamFixture(t)
	s := g.store
	appendFixtureEvent(t, g, w, grant, r, 1, map[string]any{"type": "delta", "text": "First answer"})
	raw := json.RawMessage(`{"text":"First answer","type":"delta"}`)
	if _, err := g.AppendRunnerEvent(t.Context(), grant.Token, w.ID, r.ID, StreamEvent{Seq: 1, Event: raw}); err != nil {
		t.Fatal("canonical replay", err)
	}
	if _, err := g.AppendRunnerEvent(t.Context(), grant.Token, w.ID, r.ID, StreamEvent{Seq: 1, Event: json.RawMessage(`{"type":"delta","text":"different"}`)}); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay", err)
	}
	if _, err := g.AppendRunnerEvent(t.Context(), grant.Token, w.ID, r.ID, StreamEvent{Seq: 3, Event: raw}); !errors.Is(err, ErrTransition) {
		t.Fatal("gap accepted", err)
	}
	var member auth.Actor
	if err := s.pool.QueryRow(t.Context(), `SELECT user_id FROM memberships WHERE camp_id=$1 AND user_id<>$2 LIMIT 1`, w.CampID, owner.UserID).Scan(&member.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Request(t.Context(), member, w.CampID, r.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("private request leaked", err)
	}
	head, err := s.SessionStream(t.Context(), member, w.CampID, w.SessionID)
	if err != nil || head == nil || head.ID != r.ID || head.Seq != 1 {
		t.Fatal("shared reader cannot watch", head, err)
	}
	page, err := s.RequestStream(t.Context(), member, w.CampID, r.ID, 0)
	if err != nil || len(page.Events) != 1 || page.Next != 1 {
		t.Fatal(page, err)
	}
	steerID := uuid.NewString()
	input := ControlInput{ID: steerID, Control: map[string]any{"type": "steer", "id": steerID, "text": "Use another approach"}}
	if _, err := s.SendControl(t.Context(), member, w.CampID, r.ID, input); !errors.Is(err, ErrDenied) {
		t.Fatal("teammate steered owner process", err)
	}
	if _, err := s.SendControl(t.Context(), owner, w.CampID, r.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendControl(t.Context(), owner, w.CampID, r.ID, input); err != nil {
		t.Fatal("repeat control", err)
	}
	waiting, err := g.RunnerControls(t.Context(), grant.Token, w.ID, r.ID)
	if err != nil || len(waiting) != 1 {
		t.Fatal(waiting, err)
	}
	appendFixtureEvent(t, g, w, grant, r, 2, map[string]any{"type": "boundary"})
	appendFixtureEvent(t, g, w, grant, r, 3, map[string]any{"type": "steerAccepted", "requestID": steerID})
	appendFixtureEvent(t, g, w, grant, r, 4, map[string]any{"type": "delta", "text": "Second answer"})
	appendFixtureEvent(t, g, w, grant, r, 5, map[string]any{"type": "done"})
	var retained []byte
	if err := s.pool.QueryRow(t.Context(), `SELECT encrypted_input FROM request_controls WHERE id=$1`, steerID).Scan(&retained); err != nil || retained != nil {
		t.Fatal("accepted control retained secret", err)
	}
	finishKey := uuid.NewString()
	action := RequestAction{Action: "finish", LeaseToken: r.LeaseToken, Outcome: "completed", Evidence: "Observed fixture terminal result"}
	if _, err := g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, finishKey, action); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, finishKey, action); err != nil {
		t.Fatal("repeat finish", err)
	}
	rows, err := s.pool.Query(t.Context(), `SELECT v.text FROM messages m JOIN message_revisions v ON v.session_id=m.session_id AND v.local_id=m.local_id WHERE m.session_id=$1 ORDER BY m.ordinal`, w.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var text string
		if err = rows.Scan(&text); err != nil {
			t.Fatal(err)
		}
		values = append(values, text)
	}
	want := []string{"Original prompt", "First answer", "Use another approach", "Second answer"}
	if len(values) != len(want) {
		t.Fatal(values)
	}
	for i := range want {
		if values[i] != want[i] {
			t.Fatal(values)
		}
	}
	target, err := s.Execution(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil || !target.RequiresTransfer {
		t.Fatal("cloud history must be reviewed before moving", target, err)
	}
	session, err := s.Session(t.Context(), owner, w.CampID, w.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), ExecutionInput{Location: "local", ExpectedEpoch: target.Epoch, ExpectedSessionRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision})
	if !errors.Is(err, ErrTransition) {
		t.Fatal("cloud history was abandoned", err)
	}
	if _, err = s.RequestStream(context.Background(), auth.Actor{UserID: uuid.NewString()}, w.CampID, r.ID, 0); err == nil {
		t.Fatal("foreign stream access")
	}
}
func TestRunnerStreamRejectsMalformedEvents(t *testing.T) {
	for _, value := range []string{`null`, `{"type":"delta","text":3}`, `{"type":"delta","text":"ok","secret":"no"}`, `{"type":"done"} {}`, `{"type":"unknown"}`} {
		if _, _, err := validStreamEvent(json.RawMessage(value)); err == nil {
			t.Fatal("accepted", value)
		}
	}
}
