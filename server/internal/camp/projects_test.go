package camp

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"reflect"
	"testing"
)

func TestProjectAssignmentsDoNotInterruptRunningWork(t *testing.T) {
	s, _, actors, c, old, profile := sessionFixture(t)
	ctx := context.Background()
	owner := actors[0]
	target, err := s.CreateProjectWithRepositories(ctx, owner, c.ID, uuid.NewString(), "Client A", []string{"https://github.com/Owner/One.git", "https://github.com/owner/two"})
	if err != nil || len(target.Repositories) != 2 {
		t.Fatal(target, err)
	}
	source, err := s.PublishSession(ctx, owner, c.ID, uuid.NewString(), sessionInput(old, profile))
	if err != nil {
		t.Fatal(err)
	}
	queued, err := s.CreateRequest(ctx, owner, c.ID, source.ID, uuid.NewString(), RequestInput{Prompt: "Work already approved"})
	if err != nil {
		t.Fatal(err)
	}
	running, err := s.ClaimRequest(ctx, owner, c.ID, uuid.NewString())
	if err != nil || running == nil {
		t.Fatal(err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, queued.ID, uuid.NewString(), RequestAction{Action: "start", LeaseToken: running.LeaseToken}); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	input := ProjectAssignment{ProjectID: target.ID, ExpectedRevision: source.Revision}
	moved, err := s.AssignSessionProject(ctx, actors[1], c.ID, source.ID, key, input)
	if err != nil || moved.ProjectID != target.ID || moved.ID != source.ID || moved.PermissionRevision != source.PermissionRevision || moved.LocalSessionID != source.LocalSessionID {
		t.Fatal(moved, err)
	}
	if _, err = s.AssignSessionProject(ctx, actors[1], c.ID, source.ID, key, input); err != nil {
		t.Fatal("replay", err)
	}
	if _, err = s.AssignSessionProject(ctx, owner, c.ID, source.ID, uuid.NewString(), input); !errors.Is(err, ErrRevision) {
		t.Fatal("stale change", err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, queued.ID, uuid.NewString(), RequestAction{Action: "finish", LeaseToken: running.LeaseToken, Outcome: "completed", Evidence: "Finished in original workspace"}); err != nil {
		t.Fatal("assignment invalidated active work", err)
	}
	inbox, err := s.AssignSessionProject(ctx, owner, c.ID, source.ID, uuid.NewString(), ProjectAssignment{ExpectedRevision: moved.Revision})
	if err != nil || inbox.ProjectID == target.ID {
		t.Fatal("inbox", err)
	}
	archived, err := s.UpdateProject(ctx, owner, c.ID, target.ID, uuid.NewString(), ProjectUpdate{Name: target.Name, Repositories: target.Repositories, Archived: true, ExpectedRevision: target.Revision})
	if err != nil || !archived.Archived {
		t.Fatal(err)
	}
	if _, err = s.AssignSessionProject(ctx, owner, c.ID, source.ID, uuid.NewString(), ProjectAssignment{ProjectID: target.ID, ExpectedRevision: inbox.Revision}); !errors.Is(err, ErrInput) {
		t.Fatal("assigned archived", err)
	}
	if _, err = s.AssignSessionProject(ctx, owner, c.ID, source.ID, uuid.NewString(), ProjectAssignment{ProjectID: uuid.NewString(), ExpectedRevision: inbox.Revision}); !errors.Is(err, ErrDenied) {
		t.Fatal("guessed project", err)
	}
	if _, err = s.UpdateProject(ctx, actors[2], c.ID, target.ID, uuid.NewString(), ProjectUpdate{Name: "Denied", ExpectedRevision: archived.Revision}); !errors.Is(err, ErrDenied) {
		t.Fatal("viewer changed project", err)
	}
	snapshot, err := s.Snapshot(ctx, owner, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range snapshot.Projects {
		if p.Inbox {
			if _, err = s.UpdateProject(ctx, owner, c.ID, p.ID, uuid.NewString(), ProjectUpdate{Name: "Rename inbox", ExpectedRevision: p.Revision}); !errors.Is(err, ErrInput) {
				t.Fatal("inbox changed", err)
			}
		}
	}
}

func TestPlanningProjectAssignmentKeepsVersionAndRequests(t *testing.T) {
	s, actors, c, input := planningFixture(t)
	ctx := context.Background()
	owner := actors[0]
	plan := createAndStartPlanning(t, s, owner, c, input)
	target, err := s.CreateProject(ctx, owner, c.ID, uuid.NewString(), "Other client")
	if err != nil {
		t.Fatal(err)
	}
	moved, err := s.AssignPlanningProject(ctx, owner, c.ID, plan.ID, uuid.NewString(), ProjectAssignment{ProjectID: target.ID, ExpectedRevision: plan.Revision})
	if err != nil || moved.ID != plan.ID || moved.ProjectID != target.ID || !reflect.DeepEqual(moved.Version, plan.Version) {
		t.Fatal("assignment changed planning execution", err)
	}
	if request := claimPlanning(t, s, owner, c, input); request == nil {
		t.Fatal("queued planning was lost")
	}
}

func TestRepositoryLinksAreCanonicalAndBounded(t *testing.T) {
	actual, err := repositoryURLs([]string{"https://GitHub.com/Owner/Repo.git", "https://github.com/owner/repo"})
	if err != nil || !reflect.DeepEqual(actual, []string{"https://github.com/owner/repo"}) {
		t.Fatal(actual, err)
	}
	for _, bad := range []string{"https://user:secret@github.com/owner/repo", "http://github.com/owner/repo", "https://github.com/owner/repo?token=x", "https://github.com/owner/repo/tree/main", "https://github.com/owner/%72epo"} {
		if _, err = repositoryURLs([]string{bad}); !errors.Is(err, ErrInput) {
			t.Fatal("invalid repository accepted", bad)
		}
	}
}
