package camp

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestSharedBoardMoveChecksRoleRevisionAndReplay(t *testing.T) {
	s, _, actors, c, project, profile := sessionFixture(t)
	ctx := context.Background()
	input := sessionInput(project, profile)
	initial, err := s.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range actors[2:] {
		if _, err = s.MoveSession(ctx, actor, c.ID, initial.ID, uuid.NewString(), "Done", initial.Revision); !errors.Is(err, ErrDenied) {
			t.Fatalf("unauthorized move: %v", err)
		}
	}
	key := uuid.NewString()
	moved, err := s.MoveSession(ctx, actors[1], c.ID, initial.ID, key, "Needs you", initial.Revision)
	if err != nil || moved.Stage != "Needs you" || moved.Revision != initial.Revision+1 {
		t.Fatalf("editor move: %+v %v", moved, err)
	}
	if moved.PermissionRevision != initial.PermissionRevision || moved.OwnerDeviceID != initial.OwnerDeviceID || moved.Model != initial.Model {
		t.Fatal("board move changed session authority")
	}
	retry, err := s.MoveSession(ctx, actors[1], c.ID, initial.ID, key, "Needs you", initial.Revision)
	if err != nil || retry.Revision != moved.Revision {
		t.Fatalf("move replay: %+v %v", retry, err)
	}
	if _, err = s.MoveSession(ctx, actors[1], c.ID, initial.ID, uuid.NewString(), "Done", initial.Revision); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale move: %v", err)
	}
	if _, err = s.MoveSession(ctx, actors[1], c.ID, initial.ID, key, "Done", initial.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	input.ExpectedRevision = moved.Revision
	input.Shared = false
	if _, err = s.PublishSession(ctx, actors[0], c.ID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MoveSession(ctx, actors[1], c.ID, initial.ID, key, "Needs you", initial.Revision); !errors.Is(err, ErrDenied) {
		t.Fatalf("replay after unshare: %v", err)
	}
}
