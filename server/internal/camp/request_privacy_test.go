package camp

import (
	"crypto/sha256"
	"errors"
	"github.com/google/uuid"
	"testing"
)

func TestRequestContentRevocationAcrossReadAndReceipts(t *testing.T) {
	s, _, actors, c, project, profile := sessionFixture(t)
	ctx := t.Context()
	sourceInput := sessionInput(project, profile)
	source, err := s.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), sourceInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.WriteTranscript(ctx, actors[0], c.ID, source.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "source", Ordinal: 0, Role: "assistant", Text: "Private source content"}}}); err != nil {
		t.Fatal(err)
	}
	target, err := s.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), sessionInput(project, profile))
	if err != nil {
		t.Fatal(err)
	}
	input := RequestInput{Prompt: "Prompt containing selected source content", Sources: []SourceRef{{SessionID: source.ID, TranscriptRevision: 1, PermissionRevision: source.PermissionRevision, MessageIDs: []string{"source"}}}}
	key := uuid.NewString()
	request, err := s.CreateRequest(ctx, actors[1], c.ID, target.ID, key, input)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"type":"text","text":"Saved source output"}`)
	hash := sha256.Sum256(payload)
	if _, err = s.pool.Exec(ctx, `INSERT INTO request_stream_events(request_id,seq,payload,payload_hash) VALUES($1,1,$2,$3)`, request.ID, payload, hash[:]); err != nil {
		t.Fatal(err)
	}
	if page, err := s.RequestStream(ctx, actors[1], c.ID, request.ID, 0); err != nil || len(page.Events) != 1 {
		t.Fatal("fixture stream", err)
	}
	approveKey := uuid.NewString()
	if _, err = s.ActOnRequest(ctx, actors[0], c.ID, request.ID, approveKey, RequestAction{Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimAvailableRequest(ctx, actors[0], c.ID, uuid.NewString(), []string{target.ID})
	if err != nil || claimed == nil {
		t.Fatal("claim", err)
	}
	if _, err = s.ActOnRequest(ctx, actors[0], c.ID, claimed.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claimed.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	sourceInput.ExpectedRevision = source.Revision
	sourceInput.Shared = false
	source, err = s.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), sourceInput)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range actors[:2] {
		if value, err := s.Request(ctx, actor, c.ID, request.ID); err != nil || !value.ContentRestricted || value.Prompt != "" || len(value.Sources) != 0 {
			t.Errorf("revoked request detail returned: %v", err)
		}
		if _, err = s.RequestStream(ctx, actor, c.ID, request.ID, 0); !errors.Is(err, ErrDenied) {
			t.Errorf("revoked stream returned: %v", err)
		}
		list, err := s.ListRequests(ctx, actor, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range list {
			if item.ID == request.ID && (!item.ContentRestricted || item.Prompt != "") {
				t.Error("revoked request content remained in list")
			}
		}
	}
	if _, err = s.CreateRequest(ctx, actors[1], c.ID, target.ID, key, input); !errors.Is(err, ErrDenied) {
		t.Errorf("create receipt exposed revoked prompt: %v", err)
	}
	replay, err := s.ActOnRequest(ctx, actors[0], c.ID, request.ID, approveKey, RequestAction{Action: "approve"})
	if err != nil || replay.Prompt != "" || len(replay.Sources) != 0 {
		t.Errorf("approval receipt exposed revoked prompt: %v", err)
	}
	cancelled, err := s.ActOnRequest(ctx, actors[1], c.ID, request.ID, uuid.NewString(), RequestAction{Action: "cancel"})
	if err != nil || cancelled.State != "cancel_requested" || cancelled.Prompt != "" || len(cancelled.Sources) != 0 {
		t.Errorf("safe cancellation failed: %v", err)
	}
	heartbeat, err := s.ActOnRequest(ctx, actors[0], c.ID, request.ID, uuid.NewString(), RequestAction{Action: "heartbeat", LeaseToken: claimed.LeaseToken})
	if err != nil || heartbeat.Prompt != "" || heartbeat.State != "cancel_requested" {
		t.Fatal("safe heartbeat", err)
	}
	finished, err := s.ActOnRequest(ctx, actors[0], c.ID, request.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: claimed.LeaseToken, Outcome: "cancelled", Evidence: "Provider confirmed cancellation"})
	if err != nil || finished.State != "cancelled" || finished.Prompt != "" || finished.Evidence != "" {
		t.Fatal("safe finish", err)
	}
	sourceInput.ExpectedRevision = source.Revision
	sourceInput.Shared = true
	if _, err = s.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), sourceInput); err != nil {
		t.Fatal(err)
	}
	if value, err := s.Request(ctx, actors[1], c.ID, request.ID); err != nil || !value.ContentRestricted || value.Prompt != "" {
		t.Errorf("reshare revived old request grant: %v", err)
	}
}

func TestPlanningRequestReadRequiresEveryPlannerSource(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	p := createAndStartPlanning(t, s, actors[1], c, input)
	if len(p.Version.Steps) != 2 {
		t.Fatal("missing fixture steps")
	}
	request := p.Version.Steps[1].RequestID
	for _, step := range p.Version.Steps {
		if _, err := s.ActOnRequest(t.Context(), actors[0], c.ID, step.RequestID, uuid.NewString(), RequestAction{Action: "approve"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Request(t.Context(), actors[1], c.ID, request); err != nil {
		t.Fatal(err)
	}
	source, err := s.Session(t.Context(), actors[0], c.ID, input.Planners[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PublishSession(t.Context(), actors[0], c.ID, uuid.NewString(), SessionInput{ID: source.ID, ProjectID: source.ProjectID, ProfileID: source.ProfileID, LocalSessionID: source.LocalSessionID, Title: source.Title, Model: source.Model, State: source.State, Stage: source.Stage, ExpectedRevision: source.Revision, Shared: false, AllowRequests: true})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := s.Request(t.Context(), actors[1], c.ID, request); err != nil || !value.ContentRestricted || value.Prompt != "" {
		t.Errorf("peer request exposed inaccessible planning context: %v", err)
	}
	list, err := s.ListRequests(t.Context(), actors[1], c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if !item.ContentRestricted || item.Prompt != "" {
			t.Fatal("planning request content survived peer source revocation")
		}
	}
	for range 2 {
		if claimed, err := s.ClaimAvailableWork(t.Context(), actors[0], c.ID, uuid.NewString(), []string{input.Planners[0].SessionID, input.Planners[1].SessionID}); err != nil || claimed != nil {
			t.Fatal("worker received revoked planning context", err)
		}
	}
}
