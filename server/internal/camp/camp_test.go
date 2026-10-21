package camp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

func TestCommandRetriesAreAtomicAndPayloadBound(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	s := New(pool, "fixture-receipt-key")
	a := auth.Actor{UserID: uuid.NewString(), Email: "owner@example.com", Name: "Owner"}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, a.UserID, a.Email, a.Name); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	results := make(chan Camp, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.Create(ctx, a, key, "Retry Camp")
			if err != nil {
				t.Error(err)
				return
			}
			results <- c
		}()
	}
	wg.Wait()
	close(results)
	var first Camp
	for c := range results {
		if first.ID == "" {
			first = c
		}
		if c != first {
			t.Fatal("retry changed result")
		}
	}
	if first.ID == "" {
		t.Fatal("no successful command")
	}
	events, err := s.Events(ctx, a, first.ID, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("duplicate event: %d %v", len(events), err)
	}
	if _, err = s.Create(ctx, a, key, "Different payload"); !errors.Is(err, ErrConflict) {
		t.Fatal("request ID accepted different payload", err)
	}
	inviteKey := uuid.NewString()
	inv, err := s.Invite(ctx, a, first.ID, inviteKey, "member@example.com", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.Invite(ctx, a, first.ID, inviteKey, "member@example.com", "viewer")
	if err != nil || retry != inv {
		t.Fatal("invitation retry changed token", err)
	}
	var receipt []byte
	if err = pool.QueryRow(ctx, `SELECT encrypted_result FROM command_results WHERE request_id=$1`, inviteKey).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(receipt, []byte(inv.Token)) {
		t.Fatal("invitation token stored in plaintext")
	}
}

func TestMembershipIsolationInvitationsAndEvents(t *testing.T) {
	pool := testdb.Open(t)
	ctx := context.Background()
	s := New(pool, "fixture-receipt-key")
	actors := make([]auth.Actor, 3)
	for i := range actors {
		actors[i] = auth.Actor{UserID: uuid.NewString(), Email: fmt.Sprintf("member%d@example.com", i), Name: fmt.Sprintf("Member %d", i)}
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, actors[i].UserID, actors[i].Email, actors[i].Name); err != nil {
			t.Fatal(err)
		}
	}
	a, b, k := actors[0], actors[1], actors[2]
	c, err := s.Create(ctx, a, uuid.NewString(), "Test Camp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Snapshot(ctx, b, c.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign snapshot accepted", err)
	}
	if _, err = s.Events(ctx, b, c.ID, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign event read accepted", err)
	}
	inv, err := s.Invite(ctx, a, c.ID, uuid.NewString(), k.Email, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(ctx, b, uuid.NewString(), inv.Token); !errors.Is(err, ErrDenied) {
		t.Fatal("wrong email accepted", err)
	}
	var wg sync.WaitGroup
	accepted := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Accept(ctx, k, uuid.NewString(), inv.Token)
			accepted <- err == nil
		}()
	}
	wg.Wait()
	close(accepted)
	success := 0
	for ok := range accepted {
		if ok {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("invitation accepted %d times", success)
	}
	inv, err = s.Invite(ctx, a, c.ID, uuid.NewString(), b.Email, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(ctx, b, uuid.NewString(), inv.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateProject(ctx, b, c.ID, uuid.NewString(), "Forbidden"); !errors.Is(err, ErrDenied) {
		t.Fatal("viewer created project", err)
	}
	const projects = 12
	errorsCh := make(chan error, projects)
	for i := 0; i < projects; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.CreateProject(ctx, k, c.ID, uuid.NewString(), fmt.Sprintf("Project %d", i))
			errorsCh <- err
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := s.Snapshot(ctx, b, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Members) != 3 || len(snapshot.Projects) != projects {
		t.Fatalf("unexpected snapshot members=%d projects=%d", len(snapshot.Members), len(snapshot.Projects))
	}
	events, err := s.Events(ctx, b, c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range events {
		if e.Cursor != int64(i+1) {
			t.Fatalf("unordered cursor: %d", e.Cursor)
		}
	}
	if int64(len(events)) != snapshot.Camp.Cursor {
		t.Fatal("snapshot and events disagree")
	}
	if err = s.RemoveMember(ctx, a, c.ID, uuid.NewString(), k.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Snapshot(ctx, k, c.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked member read", err)
	}
	if _, err = s.CreateProject(ctx, k, c.ID, uuid.NewString(), "After removal"); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked member wrote", err)
	}
	if _, err = s.Events(ctx, k, c.ID, 0); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked replay accepted", err)
	}
}
