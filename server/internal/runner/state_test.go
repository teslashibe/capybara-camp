package runner

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/config"
)

func testGrant() string {
	return "campr_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
}

func privateTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestJournalEncryptionLockAndBinding(t *testing.T) {
	dir := privateTemp(t)
	j, err := openJournal(dir, testGrant(), "https://one.example")
	if err != nil {
		t.Fatal(err)
	}
	if another, err := openJournal(dir, testGrant(), "https://one.example"); err == nil {
		another.close()
		t.Fatal("two workers acquired same directory")
	}
	r := &record{InstallationID: uuid.NewString(), Provider: "Codex", Epoch: 1, AttemptID: uuid.NewString(), ClaimID: uuid.NewString(), DeliveryID: uuid.NewString(), Phase: "finishing", Report: &camp.RunnerLoginReport{State: "connected", VerifiedSubject: "fixture-subject", Credentials: json.RawMessage(`{"fixture":"private-proof"}`)}}
	if err := j.save(r); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "login.enc"))
	if err != nil || bytes.Contains(raw, []byte("private-proof")) || bytes.Contains(raw, []byte(r.ClaimID)) {
		t.Fatal("plaintext journal", err)
	}
	loaded, err := j.load()
	if err != nil || loaded == nil || loaded.ClaimID != r.ClaimID || !bytes.Equal(loaded.Report.Credentials, r.Report.Credentials) {
		t.Fatal("journal round trip", err)
	}
	j.close()
	for _, value := range []struct{ token, origin string }{{testGrant(), "https://other.example"}, {"campr_" + strings.Repeat("a", 43), "https://one.example"}} {
		wrong, err := openJournal(dir, value.token, value.origin)
		if err != nil {
			t.Fatal(err)
		}
		_, err = wrong.load()
		wrong.close()
		if !errors.Is(err, ErrState) {
			t.Fatal("journal opened under wrong binding")
		}
	}
}

func TestPrivateFilesAndScratchCleanup(t *testing.T) {
	dir := privateTemp(t)
	file := filepath.Join(dir, "grant")
	if err := os.WriteFile(file, []byte(testGrant()), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := privateFile(file, 128); err == nil {
		t.Fatal("public grant accepted")
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := privateFile(link, 128); err == nil {
		t.Fatal("symlink grant accepted")
	}
	if _, err := privateFile(file, 8); err == nil {
		t.Fatal("oversized grant accepted")
	}
	j, err := openJournal(dir, testGrant(), "https://camp.example")
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	if err = os.Mkdir(filepath.Join(dir, "provider-login-abandoned"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = j.cleanScratch(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(file); err != nil {
		t.Fatal("cleanup removed unrelated file")
	}
	if _, err = os.Stat(filepath.Join(dir, "provider-login-abandoned")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("scratch retained")
	}
	if err = os.Symlink(file, filepath.Join(dir, "login.enc")); err != nil {
		t.Fatal(err)
	}
	if _, err := j.load(); err == nil {
		t.Fatal("symlink journal accepted")
	}
}

func TestRunnerRejectsRedirectAndUnsafeConfig(t *testing.T) {
	var targetCalls int
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls++ }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	file := filepath.Join(t.TempDir(), "grant")
	if err := os.WriteFile(file, []byte(testGrant()), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Runner{Origin: source.URL, GrantFile: file, StateDir: privateTemp(t)}
	w, err := New(cfg, source.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err = w.client.account(t.Context()); !errors.Is(err, ErrRejected) || targetCalls != 0 {
		t.Fatal("redirect accepted or followed", err)
	}
	for _, origin := range []string{"http://localhost:1234", "https://user:pass@host", "https://host/path", "https://host?secret=value"} {
		cfg.Origin = origin
		if cfg.Validate() == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
}
