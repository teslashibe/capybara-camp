package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

// This server exists only in the test binary. It exercises the real API and
// database while routing synthetic sign-in emails to an owner-only directory.
type fixtureMail struct{ directory string }

func (s fixtureMail) Send(_ context.Context, to, _, body string) error {
	if to != "owner@example.com" && to != "k@example.com" {
		return errors.New("only synthetic fixture recipients are permitted")
	}
	match := regexp.MustCompile(`<strong>([^<]+)</strong>`).FindStringSubmatch(body)
	if len(match) != 2 {
		return errors.New("fixture code was not found")
	}
	name := strings.Split(to, "@")[0] + "-code.txt"
	return os.WriteFile(filepath.Join(s.directory, name), []byte(match[1]), 0600)
}

func TestNativeFixtureServer(t *testing.T) {
	directory := os.Getenv("CAMP_NATIVE_FIXTURE_DIR")
	if directory == "" {
		t.Skip("interactive native fixture not requested")
	}
	if !filepath.IsAbs(directory) {
		t.Fatal("fixture directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	pool := testdb.Open(t)
	ctx := context.Background()
	store, err := camp.NewWithProviderKey(pool, "synthetic-native-fixture-receipt-key", bytes.Repeat([]byte{31}, 32))
	if err != nil {
		t.Fatal(err)
	}
	actors := []auth.Actor{
		{UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: "owner@example.com", Name: "Camp owner"},
		{UserID: uuid.NewString(), DeviceID: uuid.NewString(), Email: "k@example.com", Name: "K"},
	}
	for _, actor := range actors {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, actor.UserID, actor.Email, actor.Name); err != nil {
			t.Fatal(err)
		}
	}
	// The driver controls only this disposable fixture's synthetic owner. Keep
	// its credential out of console output and out of the app's user defaults.
	driverToken, err := auth.Token()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Fixture driver',$3,now()+interval '1 hour')`, actors[0].DeviceID, actors[0].UserID, auth.HashToken(driverToken)); err != nil {
		t.Fatal(err)
	}
	driver, err := json.Marshal(map[string]string{"token": driverToken, "user_id": actors[0].UserID})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "driver.json"), driver, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := store.Create(ctx, actors[0], uuid.NewString(), "Demo Camp")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, actors[0], c.ID, uuid.NewString(), "Demo project")
	if err != nil {
		t.Fatal(err)
	}
	project, err = store.UpdateProject(ctx, actors[0], c.ID, project.ID, uuid.NewString(), camp.ProjectUpdate{Name: project.Name, Repositories: []string{"https://github.com/test-user/project"}, ExpectedRevision: project.Revision})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := store.Invite(ctx, actors[0], c.ID, uuid.NewString(), actors[1].Email, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Accept(ctx, actors[1], uuid.NewString(), invite.Token); err != nil {
		t.Fatal(err)
	}
	api := &API{Pool: pool, Auth: auth.New(pool, strings.Repeat("fixture", 8), "https://fixture.example", fixtureMail{directory}), Camps: store, Origin: "https://fixture.example"}
	api.Git, err = camp.NewGitStore(store, nativeGitProvider{}, bytes.Repeat([]byte{41}, 32))
	if err != nil {
		t.Fatal(err)
	}
	gitLogin, err := api.Git.Begin(ctx, actors[0], uuid.NewString(), "")
	if err != nil {
		t.Fatal(err)
	}
	gitURL, _ := url.Parse(gitLogin.URL)
	gitClaim, err := api.Git.Claim(ctx, gitURL.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = api.Git.Exchange(ctx, gitClaim, "native-fixture-code"); err != nil {
		t.Fatal(err)
	}
	grants := seedNativeAccounts(t, store, actors[0])
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	data, err := json.Marshal(map[string]any{"url": server.URL, "camp_id": c.ID, "project_id": project.ID, "pid": os.Getpid(), "owner": actors[0].Email, "teammate": actors[1].Email})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "fixture.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("Native fixture API ready on loopback")
	fixtureContext, stopFixture := context.WithCancel(ctx)
	fixtureDone := make(chan struct{})
	go func() {
		defer close(fixtureDone)
		serveFixtureProviderAccounts(fixtureContext, store, api.Git, actors[0], grants)
	}()
	defer func() { stopFixture(); <-fixtureDone }()
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stopped)
	<-stopped
}
