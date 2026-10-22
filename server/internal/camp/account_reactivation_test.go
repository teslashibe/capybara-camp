package camp

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func TestAccountReactivationWaitsForErasureAndFencesOldGrants(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	account, grant := addCloud(t, s, actors[0], "Codex")
	login, claim := startCloudLogin(t, s, actors[0], account, grant)
	if _, err := s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, loginProof("original-subject")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReactivateProviderAccount(ctx, actors[1], account.ID, uuid.NewString()); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign owner reactivated", err)
	}
	if _, err := s.DisconnectProviderAccount(ctx, actors[0], account.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	if _, err := s.ReactivateProviderAccount(ctx, actors[0], account.ID, key); !errors.Is(err, ErrTransition) {
		t.Fatal("reactivated before erasure", err)
	}
	if err := s.AcknowledgeRunnerErased(ctx, grant.Token); err != nil {
		t.Fatal(err)
	}
	active, err := s.ReactivateProviderAccount(ctx, actors[0], account.ID, key)
	if err != nil || active.State != "pending" || active.ID != account.ID || active.InstallationID != account.InstallationID || active.Epoch != 3 || active.VerifiedSubject != "original-subject" {
		t.Fatal("reactivation lost account identity or fence", err)
	}
	retry, err := s.ReactivateProviderAccount(ctx, actors[0], account.ID, key)
	if err != nil || retry.Epoch != active.Epoch || retry.Revision != active.Revision {
		t.Fatal("reactivation replay changed epoch", err)
	}
	if _, err = s.ReactivateProviderAccount(ctx, actors[0], account.ID, uuid.NewString()); !errors.Is(err, ErrTransition) {
		t.Fatal("another reactivation changed pending account", err)
	}
	if _, err = s.RunnerAccount(ctx, grant.Token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("old grant revived", err)
	}
	fresh, err := s.CreateRunnerGrant(ctx, actors[0], account.ID, uuid.NewString())
	if err != nil || fresh.Epoch != active.Epoch || fresh.Token == grant.Token {
		t.Fatal("fresh grant not isolated", err)
	}
	login, claim = startCloudLogin(t, s, actors[0], active, fresh)
	result, err := s.ReportRunnerLogin(ctx, fresh.Token, login.ID, claim, loginProof("different-subject"))
	if err != nil || result.FailureCode != "identity_mismatch" {
		t.Fatal("reactivation accepted another identity", err)
	}
	if _, err = s.DisconnectProviderAccount(ctx, actors[0], account.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgeRunnerErased(ctx, fresh.Token); err != nil {
		t.Fatal(err)
	}
	stale, err := s.ReactivateProviderAccount(ctx, actors[0], account.ID, key)
	if err != nil || stale.State != "disconnected" {
		t.Fatal("old receipt reactivated a later disconnect", err)
	}
}

func TestAccountReactivationWithoutPriorRunner(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	account, err := s.CreateProviderAccount(ctx, actors[0], uuid.NewString(), ProviderAccountInput{Provider: "Claude", Label: "Unused"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DisconnectProviderAccount(ctx, actors[0], account.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	active, err := s.ReactivateProviderAccount(ctx, actors[0], account.ID, uuid.NewString())
	if err != nil || active.State != "pending" || active.Epoch != 3 {
		t.Fatal("unused account could not reactivate", err)
	}
}
