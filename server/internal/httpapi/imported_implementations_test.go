package httpapi

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImportedImplementationHTTPRequiresPlanAccess(t *testing.T) {
	api, token, c, definition := planningHTTPFixture(t)
	actor, err := api.Auth.Authenticate(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	input := camp.PlanningImportInput{SourceID: strings.Repeat("a", 64), Title: "History fixture", ProjectID: definition.ProjectID,
		Versions:        []camp.ImportedPlanningVersion{{Version: 1, Mode: "single", State: "draft", BudgetPolicy: "calls", Planners: definition.Planners[:1]}},
		Implementations: []camp.ImportedImplementationInput{{ImportedImplementationOrigin: camp.ImportedImplementationOrigin{SourceID: strings.Repeat("b", 64), PlanVersion: 1, SessionID: definition.Planners[0].SessionID, Provider: "Codex", State: "approved"}, ImportedImplementationDetails: camp.ImportedImplementationDetails{PlanDigest: strings.Repeat("c", 64), ContextFingerprint: strings.Repeat("d", 64), InitialPrompt: "Review before implementing"}, Attempts: []camp.ImportedImplementationAttempt{{ID: uuid.NewString(), Prompt: "Saved attempt", State: "uncertain", Error: "Outcome unconfirmed"}}}}}
	plan, err := api.Camps.ImportPlanning(t.Context(), actor, c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	handler := api.Handler()
	get := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/v1/camps/" + c.ID + "/planning/" + plan.ID + "/implementations"
	if r := get(path, ""); r.Code != 401 {
		t.Fatal("Unauthenticated history read", r.Code)
	}
	page := get(path+"?version=1&limit=1", token)
	var values camp.ImportedImplementationPage
	if page.Code != 200 || json.Unmarshal(page.Body.Bytes(), &values) != nil || len(values.Implementations) != 1 {
		t.Fatal(page.Code, page.Body.String())
	}
	detail := path + "/" + values.Implementations[0].ID
	for _, suffix := range []string{"", "/attempts", "/transcript"} {
		if r := get(detail+suffix, token); r.Code != 200 {
			t.Fatal(suffix, r.Code, r.Body.String())
		}
	}
	if r := get(path+"?limit=51", token); r.Code != 400 {
		t.Fatal("Unbounded implementation list", r.Code)
	}
	if r := get(detail+"/attempts?limit=11", token); r.Code != 400 {
		t.Fatal("Unbounded attempts", r.Code)
	}
	if r := get("/v1/camps/"+uuid.NewString()+"/planning/"+plan.ID+"/implementations", token); r.Code != 404 {
		t.Fatal("Foreign Camp access", r.Code)
	}
	if r := get("/v1/camps/"+c.ID+"/planning/"+uuid.NewString()+"/implementations/"+values.Implementations[0].ID, token); r.Code != 404 {
		t.Fatal("Foreign plan access", r.Code)
	}
}
