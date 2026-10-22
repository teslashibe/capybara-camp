package camp

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func controllerAccount(t *testing.T, s *Store, a auth.Actor) ProviderAccount {
	t.Helper()
	out, err := s.CreateProviderAccount(context.Background(), a, uuid.NewString(), ProviderAccountInput{Provider: "Codex", Label: "Managed"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestManagedGrantConcurrencyCapacityAndEncryption(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	a := controllerAccount(t, s, actors[0])
	other := controllerAccount(t, s, actors[1])
	var wg sync.WaitGroup
	results := make(chan ControllerGrant, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := s.EnsureControllerGrant(ctx, a, 1)
			if err != nil {
				t.Error(err)
				return
			}
			results <- g
		}()
	}
	wg.Wait()
	close(results)
	var first ControllerGrant
	for g := range results {
		if first.ID == "" {
			first = g
		}
		if g.ID != first.ID || g.Token != first.Token {
			t.Fatal("concurrent ensure minted another grant")
		}
	}
	if first.ID == "" {
		t.Fatal("no grant")
	}
	var encrypted []byte
	if err := s.pool.QueryRow(ctx, `SELECT encrypted_token FROM runner_grants WHERE id=$1`, first.ID).Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte(first.Token)) {
		t.Fatal("grant not encrypted", err)
	}
	if _, err := s.EnsureControllerGrant(ctx, other, 1); !errors.Is(err, ErrRunnerCapacity) {
		t.Fatal("capacity exceeded", err)
	}
	if _, err := s.DisconnectProviderAccount(ctx, actors[0], a.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureControllerGrant(ctx, a, 1); !errors.Is(err, ErrTransition) {
		t.Fatal("stale account minted grant", err)
	}
	status, err := s.ProviderAccountStatus(ctx, actors[0], a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ControllerErased(ctx, status.Account); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunnerAccount(ctx, first.Token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("erased grant alive", err)
	}
	if _, err := s.EnsureControllerGrant(ctx, other, 1); err != nil {
		t.Fatal("erased slot not released", err)
	}
}

func TestManagedGrantRotationWaitsForLoginAndRejectsManualGrant(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	a := controllerAccount(t, s, actors[0])
	g, err := s.EnsureControllerGrant(ctx, a, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE runner_grants SET expires_at=now()+interval '20 minutes' WHERE id=$1`, g.ID); err != nil {
		t.Fatal(err)
	}
	login, err := s.BeginProviderLogin(ctx, actors[0], a.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.EnsureControllerGrant(ctx, a, 4)
	if err != nil || same.ID != g.ID {
		t.Fatal("rotated during login", err)
	}
	if _, err = s.CancelProviderLogin(ctx, actors[0], a.ID, login.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.EnsureControllerGrant(ctx, a, 4)
	if err != nil || fresh.ID == g.ID {
		t.Fatal("expired grant not replaced", err)
	}
	var old []byte
	if err = s.pool.QueryRow(ctx, `SELECT encrypted_token FROM runner_grants WHERE id=$1`, g.ID).Scan(&old); err != nil || old != nil {
		t.Fatal("revoked grant secret retained", err)
	}
	manual, _ := addCloud(t, s, actors[1], "Claude")
	if _, err = s.EnsureControllerGrant(ctx, manual, 4); !errors.Is(err, ErrManualRunner) {
		t.Fatal("controller replaced manual grant", err)
	}
}

func TestControllerErasureAndStatusAreEpochFenced(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	a := controllerAccount(t, s, actors[0])
	if err := s.ControllerErased(ctx, a); !errors.Is(err, ErrTransition) {
		t.Fatal("active account erased", err)
	}
	if err := s.ControllerWorkloadStatus(ctx, a, "failed", "capacity"); err != nil {
		t.Fatal(err)
	}
	disconnected, err := s.DisconnectProviderAccount(ctx, actors[0], a.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ControllerWorkloadStatus(ctx, a, "ready", ""); err != nil {
		t.Fatal(err)
	}
	current, err := s.ProviderAccountStatus(ctx, actors[0], a.ID)
	if err != nil || current.Account.WorkloadState == "ready" {
		t.Fatal("old status changed new epoch", err)
	}
	if err = s.ControllerErased(ctx, a); !errors.Is(err, ErrTransition) {
		t.Fatal("old erasure accepted", err)
	}
	if err = s.ControllerErased(ctx, disconnected); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReactivateProviderAccount(ctx, actors[0], a.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if err = s.ControllerErased(ctx, disconnected); !errors.Is(err, ErrTransition) {
		t.Fatal("old cleanup erased reactivation", err)
	}
}
