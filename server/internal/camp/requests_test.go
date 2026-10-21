package camp

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func TestAvailableClaimsAndReleaseBeforeStart(t *testing.T) {
	s, _, actors, c, p, profile := sessionFixture(t)
	ctx, owner := context.Background(), actors[0]
	first, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	input := sessionInput(p, profile)
	input.ID = uuid.NewString()
	input.LocalSessionID = "second-native-session"
	second, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []Session{first, second} {
		if _, err := s.CreateRequest(ctx, owner, c.ID, session.ID, uuid.NewString(), RequestInput{Prompt: "Run only where available"}); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := s.ClaimAvailableRequest(ctx, owner, c.ID, uuid.NewString(), nil); err != nil || r != nil {
		t.Fatal("empty availability claimed work", r, err)
	}
	if _, err := s.ClaimAvailableRequest(ctx, owner, c.ID, uuid.NewString(), []string{first.ID, first.ID}); !errors.Is(err, ErrInput) {
		t.Fatal("duplicate availability accepted", err)
	}
	key := uuid.NewString()
	r, err := s.ClaimAvailableRequest(ctx, owner, c.ID, key, []string{second.ID})
	if err != nil || r == nil || r.SessionID != second.ID {
		t.Fatal("claimed unavailable session", r, err)
	}
	if _, err := s.ClaimAvailableRequest(ctx, owner, c.ID, key, []string{first.ID}); !errors.Is(err, ErrConflict) {
		t.Fatal("claim replay changed eligibility", err)
	}
	oldLease := r.LeaseToken
	if _, err := s.ActOnRequest(ctx, actors[1], c.ID, r.ID, uuid.NewString(), RequestAction{Action: "release", LeaseToken: oldLease}); !errors.Is(err, ErrDenied) {
		t.Fatal("non-worker released claim", err)
	}
	if released, err := s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "release", LeaseToken: oldLease}); err != nil || released.State != "queued" || released.LeaseUntil != nil {
		t.Fatal("release failed", released, err)
	}
	r, err = s.ClaimAvailableRequest(ctx, owner, c.ID, uuid.NewString(), []string{second.ID})
	if err != nil || r == nil || r.LeaseToken == oldLease {
		t.Fatal("released work did not receive a new fence", r, err)
	}
	if _, err := s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: oldLease}); !errors.Is(err, ErrTransition) {
		t.Fatal("released fence started work", err)
	}
	if _, err := s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: r.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "release", LeaseToken: r.LeaseToken}); !errors.Is(err, ErrTransition) {
		t.Fatal("started work was requeued", err)
	}
}

func TestRemoteApprovalClaimsAndUnknownOutcome(t *testing.T) {
	s, pool, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner, member, viewer := actors[0], actors[1], actors[2]
	session, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	createKey := uuid.NewString()
	requestInput := RequestInput{Prompt: "Do the next task", Model: "chosen-model", Effort: "high", Speed: "fast"}
	r, err := s.CreateRequest(ctx, member, c.ID, session.ID, createKey, requestInput)
	if err != nil || r.State != "pending_approval" {
		t.Fatalf("remote request: %+v %v", r, err)
	}
	if _, err = s.Request(ctx, viewer, c.ID, r.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("unrelated reader saw prompt", err)
	}
	if claim, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString()); err != nil || claim != nil {
		t.Fatal("unapproved request claimed", err)
	}
	if _, err = s.ActOnRequest(ctx, member, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "approve"}); !errors.Is(err, ErrDenied) {
		t.Fatal("requester approved remote work", err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan *Request, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString())
			if err != nil {
				t.Error(err)
			}
			claims <- claim
		}()
	}
	wg.Wait()
	close(claims)
	var claimed *Request
	count := 0
	for claim := range claims {
		if claim != nil {
			claimed = claim
			count++
		}
	}
	if count != 1 || claimed.LeaseToken == "" {
		t.Fatalf("concurrent claims: %d", count)
	}
	if current, err := s.Request(ctx, owner, c.ID, r.ID); err != nil || current.LeaseToken != "" {
		t.Fatal("ordinary read leaked lease", err)
	}
	startKey := uuid.NewString()
	start := RequestAction{Action: "start", LeaseToken: claimed.LeaseToken}
	if _, err = s.ActOnRequest(ctx, member, c.ID, r.ID, uuid.NewString(), start); !errors.Is(err, ErrDenied) {
		t.Fatal("wrong device started", err)
	}
	if result, err := s.ActOnRequest(ctx, owner, c.ID, r.ID, startKey, start); err != nil || result.State != "started" {
		t.Fatal("start failed", err)
	}
	if result, err := s.ActOnRequest(ctx, owner, c.ID, r.ID, startKey, start); err != nil || result.State != "started" {
		t.Fatal("start receipt failed", err)
	}
	if replay, err := s.CreateRequest(ctx, member, c.ID, session.ID, createKey, requestInput); err != nil || replay.State != "started" || replay.Revision <= r.Revision {
		t.Fatal("create replay returned stale state", err)
	}
	second, err := s.CreateRequest(ctx, owner, c.ID, session.ID, uuid.NewString(), RequestInput{Prompt: "Wait for first"})
	if err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString()); err != nil || claim != nil {
		t.Fatal("second concurrent execution allowed", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ReapRequests(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := s.Request(ctx, owner, c.ID, r.ID)
	if err != nil || current.State != "outcome_unknown" {
		t.Fatalf("lost started request: %+v %v", current, err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, startKey, start); !errors.Is(err, ErrTransition) {
		t.Fatal("expired cached start authorized work", err)
	}
	if claim, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString()); err != nil || claim != nil {
		t.Fatal("unknown outcome redispatched", err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: claimed.LeaseToken, Outcome: "completed", Evidence: "late output"}); !errors.Is(err, ErrTransition) {
		t.Fatal("unknown outcome completed without reconciliation", err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "reconcile", LeaseToken: claimed.LeaseToken, Outcome: "completed", Evidence: "Confirmed saved provider turn 1"}); err != nil {
		t.Fatal(err)
	}
	before, err := s.Request(ctx, owner, c.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "cancel"})
	if err != nil || after.State != "completed" || after.Revision != before.Revision {
		t.Fatal("late cancel changed a completed request", err)
	}
	events, err := s.Events(ctx, owner, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	completions := 0
	for _, e := range events {
		if e.Kind == "request.completed" {
			completions++
		}
	}
	if completions != 1 {
		t.Fatalf("duplicate completion events: %d", completions)
	}
	claim, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString())
	if err != nil || claim == nil || claim.ID != second.ID {
		t.Fatal("queue did not resume after confirmed outcome", err)
	}
}

func TestExpiredClaimGetsNewFenceAndRevokedDeviceCannotStart(t *testing.T) {
	s, pool, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner := actors[0]
	session, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequest(ctx, owner, c.ID, session.ID, uuid.NewString(), RequestInput{Prompt: "Test claim recovery"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString())
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ReapRequests(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString())
	if err != nil || second == nil || second.LeaseToken == first.LeaseToken {
		t.Fatal("old claim fence retained", err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: first.LeaseToken}); !errors.Is(err, ErrTransition) {
		t.Fatal("stale fence started", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE devices SET revoked_at=now() WHERE id=$1`, owner.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: second.LeaseToken}); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("revoked device started", err)
	}
}

func TestRequestSourceRevocationAndFrozenSettings(t *testing.T) {
	s, _, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner, member, viewer := actors[0], actors[1], actors[2]
	targetInput := sessionInput(p, profile)
	target, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), targetInput)
	if err != nil {
		t.Fatal(err)
	}
	sourceInput := sessionInput(p, profile)
	source, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sourceInput)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteTranscript(ctx, owner, c.ID, source.ID, uuid.NewString(), TranscriptInput{Messages: []MessageInput{{ID: "m", Ordinal: 0, Role: "assistant", Text: "Source context"}}})
	if err != nil {
		t.Fatal(err)
	}
	input := RequestInput{Prompt: "Use context", Model: "selected-model", Effort: "high", Speed: "fast", Sources: []SourceRef{{SessionID: source.ID, TranscriptRevision: 1}}}
	if _, err = s.CreateRequest(ctx, viewer, c.ID, target.ID, uuid.NewString(), input); !errors.Is(err, ErrDenied) {
		t.Fatal("viewer requested execution", err)
	}
	key := uuid.NewString()
	r, err := s.CreateRequest(ctx, member, c.ID, target.ID, key, input)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := s.CreateRequest(ctx, member, c.ID, target.ID, key, input); err != nil || retry.ID != r.ID || retry.Model != "selected-model" || retry.Effort != "high" || retry.Speed != "fast" {
		t.Fatal("request settings changed on replay", err)
	}
	input.Prompt = "Changed request"
	if _, err = s.CreateRequest(ctx, member, c.ID, target.ID, key, input); !errors.Is(err, ErrConflict) {
		t.Fatal("changed prompt reused key", err)
	}
	sourceInput.ExpectedRevision = source.Revision
	sourceInput.Shared = false
	if _, err = s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sourceInput); err != nil {
		t.Fatal(err)
	}
	current, err := s.Request(ctx, member, c.ID, r.ID)
	if err != nil || current.State != "cancelled" || current.Reason != "permission_changed" {
		t.Fatalf("revoked source did not cancel: %+v %v", current, err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "approve"}); err == nil {
		t.Fatal("revoked source approved")
	}
	if _, err = s.CreateRequest(ctx, owner, c.ID, target.ID, uuid.NewString(), input); !errors.Is(err, ErrDenied) {
		t.Fatal("private source attached to shared output", err)
	}
	// Title and runtime-state changes must not invalidate the approval contract.
	targetInput.ExpectedRevision = target.Revision
	targetInput.Title = "Renamed session"
	renamed, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), targetInput)
	if err != nil || renamed.PermissionRevision != target.PermissionRevision {
		t.Fatal("metadata changed permission revision", err)
	}
}

func TestStartCancellationRacePreservesStopIntent(t *testing.T) {
	s, _, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner := actors[0]
	session, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequest(ctx, owner, c.ID, session.ID, uuid.NewString(), RequestInput{Prompt: "Race cancellation"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString())
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claimed.LeaseToken})
		if err != nil && !errors.Is(err, ErrTransition) {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		_, err := s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "cancel"})
		if err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	current, err := s.Request(ctx, owner, c.ID, r.ID)
	if err != nil || !oneOf(current.State, "cancelled", "cancel_requested") {
		t.Fatalf("stop intent lost: %+v %v", current, err)
	}
}

func TestPermissionChangeDuringExecutionKeepsOutcomeFence(t *testing.T) {
	s, pool, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner := actors[0]
	input := sessionInput(p, profile)
	session, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequest(ctx, owner, c.ID, session.ID, uuid.NewString(), RequestInput{Prompt: "Do bounded work"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString())
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: claim.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision = session.Revision
	input.AllowRequests = false
	if _, err = s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	current, err := s.Request(ctx, owner, c.ID, r.ID)
	if err != nil || current.State != "cancel_requested" {
		t.Fatal("permission change lost stop intent", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ReapRequests(ctx); err != nil {
		t.Fatal(err)
	}
	current, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "cancel"})
	if err != nil || current.State != "outcome_unknown" || current.Reason != "cancel_requested" {
		t.Fatal("cancellation removed an unknown-outcome fence", err)
	}
	if _, err = s.CreateRequest(ctx, owner, c.ID, session.ID, uuid.NewString(), RequestInput{Prompt: "Wait behind unknown work"}); err != nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString()); err != nil || next != nil {
		t.Fatal("unknown cancelled work allowed another run", err)
	}
}
