package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

func planningHTTPFixture(t *testing.T) (*API, string, camp.Camp, camp.PlanningInput) {
	t.Helper()
	pool := testdb.Open(t)
	ctx := context.Background()
	token, err := auth.Token()
	if err != nil {
		t.Fatal(err)
	}
	owner := auth.Actor{UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: "planning@example.com", Name: "Planner"}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, owner.UserID, owner.Email, owner.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Test',$3,now()+interval '1 day')`, owner.DeviceID, owner.UserID, auth.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	api := &API{Pool: pool, Auth: auth.New(pool, strings.Repeat("x", 40), "https://camp.example", unusedMail{}), Camps: camp.New(pool, "fixture-planning-key"), Origin: "https://camp.example"}
	c, err := api.Camps.Create(ctx, owner, uuid.NewString(), "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, err := api.Camps.CreateProject(ctx, owner, c.ID, uuid.NewString(), "Fixture")
	if err != nil {
		t.Fatal(err)
	}
	input := camp.PlanningInput{Title: "Fixture", ProjectID: p.ID, Brief: "Plan a test", Planners: []camp.PlannerInput{}}
	for i, provider := range []string{"Codex", "Claude"} {
		profile, err := api.Camps.RegisterProfile(ctx, owner, uuid.NewString(), camp.ProfileInput{Provider: provider, LocalID: strings.Repeat(string(rune('a'+i)), 64), Label: provider})
		if err != nil {
			t.Fatal(err)
		}
		session, err := api.Camps.PublishSession(ctx, owner, c.ID, uuid.NewString(), camp.SessionInput{ID: uuid.NewString(), ProfileID: profile.ID, ProjectID: p.ID, LocalSessionID: uuid.NewString(), Title: provider, State: "idle", Stage: "Ready", Shared: true, AllowRequests: true})
		if err != nil {
			t.Fatal(err)
		}
		input.Planners = append(input.Planners, camp.PlannerInput{SessionID: session.ID, Model: "fixture"})
	}
	return api, token, c, input
}

func TestPlanningHTTPAuthenticationAndBudgetRefusal(t *testing.T) {
	api, token, c, input := planningHTTPFixture(t)
	handler := api.Handler()
	send := func(method, path, authToken string, body any) *httptest.ResponseRecorder {
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", uuid.NewString())
		if authToken != "" {
			r.Header.Set("Authorization", "Bearer "+authToken)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/v1/camps/" + c.ID + "/planning"
	if r := send(http.MethodPost, path, "", input); r.Code != 401 {
		t.Fatal("missing auth", r.Code)
	}
	created := send(http.MethodPost, path, token, input)
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var plan camp.PlanningTicket
	if err := json.Unmarshal(created.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	started := send(http.MethodPost, path+"/"+plan.ID+"/actions", token, camp.PlanningAction{Action: "start", ExpectedRevision: plan.Revision})
	if started.Code != 409 || !strings.Contains(started.Body.String(), "token_ceiling_unavailable") {
		t.Fatal("budget not refused", started.Code, started.Body.String())
	}
	if r := send(http.MethodGet, path, token, nil); r.Code != 200 {
		t.Fatal("list failed", r.Code)
	}
	crafted := map[string]any{"prompt": "plan", "kind": "planning"}
	if r := send(http.MethodPost, "/v1/camps/"+c.ID+"/sessions/"+input.Planners[0].SessionID+"/requests", token, crafted); r.Code != 400 {
		t.Fatal("chat endpoint accepted planning metadata", r.Code)
	}
}
