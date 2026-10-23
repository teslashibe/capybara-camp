package camp

import (
	"testing"

	"github.com/google/uuid"
)

func TestTeammateRequestIsAttributedEndToEnd(t *testing.T) {
	g, f, actors, c, session, input := workspaceFixture(t)
	s := g.store
	owner, member := actors[0], actors[1]
	input.Purpose = "planning"
	w, err := g.CreateWorkspace(t.Context(), owner, c.ID, session.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.CreateRunnerGrant(t.Context(), owner, input.ProviderAccountID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	prepare := uuid.NewString()
	if _, err = g.ClaimRunnerWorkspace(t.Context(), grant.Token, w.ID, prepare); err != nil {
		t.Fatal(err)
	}
	if _, err = g.RunnerWorkspaceToken(t.Context(), grant.Token, w.ID, prepare); err != nil {
		t.Fatal(err)
	}
	if w, err = g.ReportRunnerWorkspace(t.Context(), grant.Token, w.ID, prepare, WorkspaceReport{State: "ready", BaseCommit: w.BaseCommit, Branch: w.Branch}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AssignExecution(t.Context(), owner, c.ID, session.ID, uuid.NewString(), ExecutionInput{Location: "cloud", WorkspaceID: w.ID, ExpectedEpoch: session.ExecutionEpoch, ExpectedSessionRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision, ExpectedWorkspaceRevision: w.Revision}); err != nil {
		t.Fatal(err)
	}
	// The teammate has their own GitHub login.
	f.mu.Lock()
	f.user = 456
	f.mu.Unlock()
	connectGit(t, g, member)

	r, err := s.CreateRequest(t.Context(), member, c.ID, session.ID, uuid.NewString(), RequestInput{Prompt: "Teammate prompt"})
	if err != nil || r.State != "pending_approval" {
		t.Fatal(r.State, err)
	}
	approved, err := s.ActOnRequest(t.Context(), owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "approve"})
	if err != nil || approved.ApprovedByUserID != owner.UserID || approved.RequesterUserID != member.UserID {
		t.Fatal("approval was not attributed", approved.ApprovedByUserID, err)
	}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerRequest(t.Context(), grant.Token, w.ID, claim); err != nil {
		t.Fatal(err)
	}
	turn, err := g.RunnerTurn(t.Context(), grant.Token, w.ID, r.ID)
	if err != nil || turn.GitIdentity == nil {
		t.Fatal("turn carried no Git identity", err)
	}
	if turn.GitIdentity.AuthorEmail != "456+fixture-user@users.noreply.github.com" || turn.GitIdentity.CommitterEmail != "123+fixture-user@users.noreply.github.com" {
		t.Fatal("commits are not attributed to the requester and owner", *turn.GitIdentity)
	}
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claim}); err != nil {
		t.Fatal(err)
	}
	appendFixtureEvent(t, g, w, grant, Request{ID: r.ID}, 1, map[string]any{"type": "delta", "text": "Answer"})
	if _, err = g.ActOnRunnerRequest(t.Context(), grant.Token, w.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: claim, Outcome: "completed", Evidence: "Provider terminal receipt"}); err != nil {
		t.Fatal(err)
	}
	current, err := s.Session(t.Context(), owner, c.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := s.Transcript(t.Context(), owner, c.ID, session.ID, current.TranscriptRevision, 0, 50)
	if err != nil || len(transcript.Messages) != 2 {
		t.Fatal(transcript.Messages, err)
	}
	if transcript.Messages[0].AuthorUserID != member.UserID || transcript.Messages[1].AuthorUserID != "" {
		t.Fatal("published prompt lost its author", transcript.Messages[0].AuthorUserID, transcript.Messages[1].AuthorUserID)
	}
	activity, err := s.SessionActivity(t.Context(), owner, c.ID, session.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, entry := range activity {
		if entry.Kind == "request.created" || entry.State == "queued" && entry.Kind == "request.updated" {
			seen[entry.Kind+":"+entry.State] = entry.ActorUserID
		}
	}
	if seen["request.created:pending_approval"] != member.UserID {
		t.Fatal("activity did not credit the requester", seen, activity)
	}
	approvedBy := ""
	for _, entry := range activity {
		if entry.State == "queued" && entry.ActorUserID == owner.UserID {
			approvedBy = entry.ActorUserID
		}
	}
	if approvedBy == "" {
		t.Fatal("activity did not credit the approver", activity)
	}
	if _, err = s.SessionActivity(t.Context(), actors[2], c.ID, uuid.NewString(), 0); err == nil {
		t.Fatal("activity for an unknown session was readable")
	}
}
