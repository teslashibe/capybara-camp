package camp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

func TestPlanningSourcesAreOwnerScopedAndRequireCurrentAssignments(t *testing.T) {
	g, w, _, owner, input := executionFixture(t, "planning")
	s := g.store
	if _, err := s.AssignExecution(t.Context(), owner, w.CampID, w.SessionID, uuid.NewString(), input); err != nil {
		t.Fatal(err)
	}
	values, err := s.PlanningSources(t.Context(), owner, w.CampID, w.ProjectID, w.ProviderAccountID)
	if err != nil || len(values) != 1 {
		t.Fatal("prepared source missing", values, err)
	}
	value := values[0]
	if value.Session.ID != w.SessionID || value.Session.ExecutionLocation != "cloud" || value.Session.ExecutionWorkspaceID != w.ID || value.AccountID != w.ProviderAccountID || value.RepositoryName != "fixture/repo" {
		t.Fatal("source lost its execution binding", value)
	}
	raw, err := json.Marshal(value)
	if err != nil || strings.Contains(string(raw), "credential") || strings.Contains(string(raw), "git_connection") || strings.Contains(string(raw), "token") {
		t.Fatal("source exposed credential metadata", err)
	}
	var member auth.Actor
	if err = s.pool.QueryRow(t.Context(), `SELECT m.user_id,d.id FROM memberships m JOIN devices d ON d.user_id=m.user_id WHERE m.camp_id=$1 AND m.user_id<>$2 LIMIT 1`, w.CampID, owner.UserID).Scan(&member.UserID, &member.DeviceID); err != nil {
		t.Fatal(err)
	}
	values, err = s.PlanningSources(t.Context(), member, w.CampID, w.ProjectID, w.ProviderAccountID)
	if err != nil || len(values) != 0 {
		t.Fatal("teammate received owner account sources", values, err)
	}
	if _, err = s.PlanningSources(t.Context(), auth.Actor{UserID: uuid.NewString()}, w.CampID, w.ProjectID, ""); !errors.Is(err, ErrDenied) {
		t.Fatal("outsider read sources", err)
	}
	for _, filter := range []struct{ project, account string }{{uuid.NewString(), w.ProviderAccountID}, {w.ProjectID, uuid.NewString()}} {
		values, err = s.PlanningSources(t.Context(), owner, w.CampID, filter.project, filter.account)
		if err != nil || len(values) != 0 {
			t.Fatal("filter crossed project or account", values, err)
		}
	}
	for _, filter := range []struct{ project, account string }{{"invalid", ""}, {w.ProjectID, "invalid"}} {
		if _, err = s.PlanningSources(t.Context(), owner, w.CampID, filter.project, filter.account); !errors.Is(err, ErrInput) {
			t.Fatal("invalid filter accepted", err)
		}
	}
	for _, change := range []struct{ name, reject, restore, id string }{
		{"expired provider", `UPDATE provider_installations SET credential_expires_at=now()-interval '1 minute' WHERE id=$1`, `UPDATE provider_installations SET credential_expires_at=NULL WHERE id=$1`, w.ProviderInstallationID},
		{"changed provider", `UPDATE provider_installations SET epoch=epoch+1 WHERE id=$1`, `UPDATE provider_installations SET epoch=epoch-1 WHERE id=$1`, w.ProviderInstallationID},
		{"changed Git login", `UPDATE git_connections SET epoch=epoch+1 WHERE id=$1`, `UPDATE git_connections SET epoch=epoch-1 WHERE id=$1`, w.GitConnectionID},
		{"changed project", `UPDATE projects SET revision=revision+1 WHERE id=$1`, `UPDATE projects SET revision=revision-1 WHERE id=$1`, w.ProjectID},
		{"archived project", `UPDATE projects SET archived=true WHERE id=$1`, `UPDATE projects SET archived=false WHERE id=$1`, w.ProjectID},
	} {
		t.Run(change.name, func(t *testing.T) {
			if _, err := s.pool.Exec(t.Context(), change.reject, change.id); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, err := s.pool.Exec(ctx, change.restore, change.id); err != nil {
					t.Error(err)
				}
			})
			values, err := s.PlanningSources(t.Context(), owner, w.CampID, w.ProjectID, w.ProviderAccountID)
			if err != nil || len(values) != 0 {
				t.Fatal("unavailable source was offered", values, err)
			}
		})
	}
}
