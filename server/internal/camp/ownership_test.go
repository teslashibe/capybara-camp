package camp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func TestOwnershipRequiresSameUserAndNoUncertainWork(t *testing.T) {
	s, pool, actors, c, p, profile := sessionFixture(t)
	ctx := context.Background()
	owner := actors[0]
	input := sessionInput(p, profile)
	session, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	device := auth.Actor{UserID: owner.UserID, DeviceID: uuid.NewString(), Email: owner.Email, Name: owner.Name}
	if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'New Mac',$3,now()+interval '1 day')`, device.DeviceID, device.UserID, auth.HashToken(device.DeviceID)); err != nil {
		t.Fatal(err)
	}
	newProfile, err := s.RegisterProfile(ctx, device, uuid.NewString(), ProfileInput{LocalID: strings.Repeat("c", 64), Provider: "Codex", Label: "New Mac"})
	if err != nil {
		t.Fatal(err)
	}
	change := OwnershipInput{ProfileID: newProfile.ID, ExpectedRevision: session.Revision}
	if _, err = pool.Exec(ctx, `UPDATE sessions SET external_owner=true WHERE id=$1`, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdoptSession(ctx, device, c.ID, session.ID, uuid.NewString(), change); !errors.Is(err, ErrTransition) {
		t.Fatal("external runtime changed owner", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE sessions SET external_owner=false WHERE id=$1`, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdoptSession(ctx, actors[1], c.ID, session.ID, uuid.NewString(), change); !errors.Is(err, ErrDenied) {
		t.Fatal("teammate adopted owner's session", err)
	}
	request, err := s.CreateRequest(ctx, owner, c.ID, session.ID, uuid.NewString(), RequestInput{Prompt: "Synthetic work", Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString())
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if _, err = s.AdoptSession(ctx, device, c.ID, session.ID, uuid.NewString(), change); !errors.Is(err, ErrTransition) {
		t.Fatal("claimed work changed owner", err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, request.ID, uuid.NewString(), RequestAction{Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.CreateRequest(ctx, owner, c.ID, session.ID, uuid.NewString(), RequestInput{Prompt: "Queued on old device", Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	adopted, err := s.AdoptSession(ctx, device, c.ID, session.ID, key, change)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.OwnerDeviceID != device.DeviceID || adopted.ProfileID != newProfile.ID || adopted.PermissionRevision != session.PermissionRevision+1 || !adopted.ExternalOwner {
		t.Fatalf("bad adoption: %+v", adopted)
	}
	if adopted.TranscriptRevision != session.TranscriptRevision || adopted.LocalSessionID != session.LocalSessionID {
		t.Fatal("adoption replaced history")
	}
	stopped, err := s.Request(ctx, device, c.ID, pending.ID)
	if err != nil || stopped.State != "cancelled" {
		t.Fatal("old device queue survived adoption", err)
	}
	replayed, err := s.AdoptSession(ctx, device, c.ID, session.ID, key, change)
	if err != nil || replayed.Revision != adopted.Revision {
		t.Fatal("adoption replay changed ownership twice", err)
	}
	if _, err = s.AdoptSession(ctx, owner, c.ID, session.ID, uuid.NewString(), OwnershipInput{ProfileID: profile.ID, ExpectedRevision: adopted.Revision - 1}); !errors.Is(err, ErrRevision) {
		t.Fatal("stale ownership update", err)
	}
	uncertain, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(p, profile))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequest(ctx, owner, c.ID, uncertain.ID, uuid.NewString(), RequestInput{Prompt: "Interrupted work", Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString())
	if err != nil || lease == nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, r.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: lease.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE prompt_requests SET lease_until=now()-interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ReapRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdoptSession(ctx, device, c.ID, uncertain.ID, uuid.NewString(), OwnershipInput{ProfileID: newProfile.ID, ExpectedRevision: uncertain.Revision}); !errors.Is(err, ErrTransition) {
		t.Fatal("uncertain work changed owner", err)
	}
}
