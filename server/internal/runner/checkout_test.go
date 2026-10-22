package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

func fixtureCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A real smart-HTTP Git transport exercises shallow fetch and checkout. No
// external account or repository is contacted by these tests.
func gitFixtureServer(t *testing.T, seed func(string), before func(http.ResponseWriter, *http.Request) bool) (string, string, *atomic.Int32) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo.git")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("pinned version\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, repo, "add", ".")
	if seed != nil {
		seed(repo)
	}
	fixtureCommand(t, repo, "commit", "-m", "Fixture")
	sha := fixtureCommand(t, repo, "rev-parse", "HEAD")
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/info/refs") {
			requests.Add(1)
		}
		if before != nil && before(w, r) {
			return
		}
		cmd := exec.CommandContext(r.Context(), "/usr/bin/git", "http-backend")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "PATH_INFO=" + r.URL.Path, "QUERY_STRING=" + r.URL.RawQuery, "REQUEST_METHOD=" + r.Method, "CONTENT_TYPE=" + r.Header.Get("Content-Type"), "REMOTE_USER=fixture"}
		if r.ContentLength >= 0 {
			cmd.Env = append(cmd.Env, "CONTENT_LENGTH="+strconv.FormatInt(r.ContentLength, 10))
		}
		cmd.Stdin = r.Body
		out, err := cmd.Output()
		if err != nil {
			http.Error(w, "Fixture transport failed", 500)
			return
		}
		header, body, ok := bytes.Cut(out, []byte("\r\n\r\n"))
		if !ok {
			http.Error(w, "Fixture headers missing", 500)
			return
		}
		status := 200
		for _, line := range strings.Split(string(header), "\r\n") {
			key, value, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			value = strings.TrimSpace(value)
			if key == "Status" {
				status, _ = strconv.Atoi(strings.Fields(value)[0])
			} else {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/repo.git", sha, &requests
}

func checkoutFixture(t *testing.T, remote, sha string) (*checkout, camp.Workspace, camp.RunnerWorkspaceToken) {
	t.Helper()
	c, err := openCheckout(privateTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	c.remote = func(camp.Workspace) string { return remote }
	c.gitConfig = []string{"-c", "protocol.http.allow=always"}
	w := camp.Workspace{ID: uuid.NewString(), SessionID: uuid.NewString(), RepositoryName: "fixture/repo", BaseCommit: sha}
	w.Branch = "camp/" + w.SessionID + "/" + w.ID
	return c, w, camp.RunnerWorkspaceToken{WorkspaceID: w.ID, ClaimID: uuid.NewString(), Token: "ghu_" + strings.Repeat("n", 36), ExpiresAt: time.Now().Add(time.Minute)}
}

func TestCheckoutPinsCommitAndPrivateIndependentDirectories(t *testing.T) {
	remote, sha, _ := gitFixtureServer(t, func(dir string) {
		if err := os.Symlink("README.md", filepath.Join(dir, "readme-link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../README.md", filepath.Join(dir, "nested", "readme-link")); err != nil {
			t.Fatal(err)
		}
		fixtureCommand(t, dir, "add", ".")
	}, nil)
	c, w, token := checkoutFixture(t, remote, sha)
	if err := c.prepare(t.Context(), w, token); err != nil {
		t.Fatal(err)
	}
	if err := c.verify(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(c.workdir(w), ".git", "config"))
	if err != nil || bytes.Contains(config, []byte(token.Token)) || bytes.Contains(config, []byte("credential")) {
		t.Fatal("credential persisted in repository", err)
	}
	entries, err := c.entries()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "credential-") {
			t.Fatal("temporary credential retained")
		}
	}
	other := w
	other.ID = uuid.NewString()
	other.Branch = "camp/" + other.SessionID + "/" + other.ID
	token.WorkspaceID = other.ID
	if err := c.prepare(t.Context(), other, token); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.workdir(w), "README.md"), []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.verify(t.Context(), w); !errors.Is(err, ErrState) {
		t.Fatal("dirty recovery accepted", err)
	}
	if err := c.verify(t.Context(), other); err != nil {
		t.Fatal("second ticket changed", err)
	}
	if err := c.keepWorkspace(other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.workdir(w)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retired checkout retained")
	}
}

func TestCheckoutVerificationCancellationIsNotCorruption(t *testing.T) {
	remote, sha, _ := gitFixtureServer(t, nil, nil)
	c, w, token := checkoutFixture(t, remote, sha)
	if err := c.prepare(t.Context(), w, token); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.verify(ctx, w); !errors.Is(err, context.Canceled) {
		t.Fatal("stopped verification reported corrupt state", err)
	}
	started := filepath.Join(t.TempDir(), "started")
	hook := filepath.Join(t.TempDir(), "wait-for-cancellation")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf started > "+shellQuote(started)+"\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c.gitConfig = append(c.gitConfig, "-c", "core.fsmonitor="+hook)
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.verify(ctx, w) }()
	eventually(t, func() bool { _, err := os.Stat(started); return err == nil })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("Git cancellation became an unsafe-state error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled Git did not join")
	}
	c.gitConfig = c.gitConfig[:len(c.gitConfig)-2]
	if err := c.verify(t.Context(), w); err != nil {
		t.Fatal("cancelled verification damaged the retained checkout", err)
	}
}

func TestCheckoutRejectsEscapingLinksAndSubmodules(t *testing.T) {
	for _, kind := range []string{"relative escape", "absolute escape", "symlink chain escape", "symlink cycle", "submodule"} {
		t.Run(kind, func(t *testing.T) {
			remote, sha, _ := gitFixtureServer(t, func(dir string) {
				switch kind {
				case "relative escape":
					if err := os.Symlink("../../outside", filepath.Join(dir, "unsafe")); err != nil {
						t.Fatal(err)
					}
				case "absolute escape":
					if err := os.Symlink("/tmp/outside", filepath.Join(dir, "unsafe")); err != nil {
						t.Fatal(err)
					}
				case "submodule":
					fixtureCommand(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("a", 40)+",child")
				case "symlink chain escape":
					if err := os.Mkdir(filepath.Join(dir, "d"), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(filepath.Join(dir, "x"), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "x", "keep"), []byte("fixture"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("../x", filepath.Join(dir, "d", "b")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("d/b/../../outside", filepath.Join(dir, "unsafe")); err != nil {
						t.Fatal(err)
					}
				case "symlink cycle":
					if err := os.Symlink("unsafe", filepath.Join(dir, "loop")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("loop", filepath.Join(dir, "unsafe")); err != nil {
						t.Fatal(err)
					}
				}
				if kind != "submodule" {
					fixtureCommand(t, dir, "add", ".")
				}
			}, nil)
			c, w, token := checkoutFixture(t, remote, sha)
			if err := c.prepare(t.Context(), w, token); !errors.Is(err, errCheckoutUnsafe) {
				t.Fatal("unsafe checkout accepted", err)
			}
			if _, err := os.Lstat(filepath.Join(c.workdir(w), "unsafe")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unsafe tree was materialized")
			}
			entries, _ := c.entries()
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "credential-") {
					t.Fatal("failed fetch retained token")
				}
			}
		})
	}
}

func TestCheckoutCancelsBlockedTransport(t *testing.T) {
	started := make(chan struct{}, 1)
	remote, sha, _ := gitFixtureServer(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return true
	})
	c, w, token := checkoutFixture(t, remote, sha)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.prepare(ctx, w, token) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("fetch did not begin")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Git process did not join")
	}
}

func TestCheckoutLostHeartbeatStopsAtLeaseDeadline(t *testing.T) {
	remote, sha, _ := gitFixtureServer(t, nil, func(w http.ResponseWriter, r *http.Request) bool { <-r.Context().Done(); return true })
	c, workspace, token := checkoutFixture(t, remote, sha)
	stopService := make(chan struct{})
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-stopService:
		}
	}))
	defer func() { close(stopService); service.Close() }()
	worker := &Worker{checkout: c, client: &client{origin: service.URL, http: service.Client()}, interval: 10 * time.Millisecond}
	now := time.Now()
	record := &workspaceRecord{Workspace: workspace, ClaimID: token.ClaimID, Phase: "preparing", LeaseUntil: now.Add(200 * time.Millisecond), Deadline: now.Add(5 * time.Second)}
	err := worker.prepareWorkspace(t.Context(), record, token)
	if !errors.Is(err, ErrRejected) || time.Since(now) > time.Second {
		t.Fatal("lost heartbeat exceeded lease", err, time.Since(now))
	}
}

func TestCheckoutTreeBoundsAndCleanup(t *testing.T) {
	c, w, _ := checkoutFixture(t, "http://unused", strings.Repeat("a", 40))
	for _, name := range []string{"../outside", "/outside", "a/../b", ".GIT/config", "a\\b", "a\nfile", "a:fork"} {
		data := []byte("100644 blob " + w.BaseCommit + " 1\t" + name + "\x00")
		if err := c.validateTree(t.Context(), w, data); !errors.Is(err, errCheckoutUnsafe) {
			t.Fatalf("path accepted: %q %v", name, err)
		}
	}
	if err := c.validateTree(t.Context(), w, []byte(fmt.Sprintf("100644 blob %s %d\tlarge\x00", w.BaseCommit, 33<<20))); !errors.Is(err, errCheckoutSize) {
		t.Fatal("large object accepted", err)
	}
	var data strings.Builder
	for i := 0; i < 20001; i++ {
		fmt.Fprintf(&data, "100644 blob %s 0\tf%d\x00", w.BaseCommit, i)
	}
	if err := c.validateTree(t.Context(), w, []byte(data.String())); !errors.Is(err, errCheckoutSize) {
		t.Fatal("unbounded file count", err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, c.workdir(w)); err != nil {
		t.Fatal(err)
	}
	if err := c.cleanAll(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal("cleanup followed a symlink", err)
	}
}

func TestGitCredentialExactDestinationAndExpiry(t *testing.T) {
	file := filepath.Join(t.TempDir(), "credential")
	token := "ghu_" + strings.Repeat("n", 36)
	write := func(expiry time.Time) {
		raw, _ := json.Marshal(gitCredential{URL: "https://github.com/fixture/repo.git", Token: token, ExpiresAt: expiry})
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(time.Now().Add(time.Minute))
	request := "protocol=https\nhost=github.com\npath=fixture/repo.git\n\n"
	var out bytes.Buffer
	if err := GitCredential("get", file, strings.NewReader(request), &out); err != nil || !strings.Contains(out.String(), token) {
		t.Fatal("authorized credential missing", err)
	}
	for _, input := range []string{strings.Replace(request, "github.com", "elsewhere.example", 1), strings.Replace(request, "fixture/repo.git", "fixture/other.git", 1), strings.Replace(request, "https", "http", 1), "protocol=https\nprotocol=https\nhost=github.com\npath=fixture/repo.git\n\n", strings.Repeat("x", 8193)} {
		out.Reset()
		if err := GitCredential("get", file, strings.NewReader(input), &out); err == nil || out.Len() != 0 {
			t.Fatal("destination constraint bypassed")
		}
	}
	write(time.Now().Add(-time.Second))
	out.Reset()
	if err := GitCredential("get", file, strings.NewReader(request), &out); err == nil || out.Len() != 0 {
		t.Fatal("expired credential returned")
	}
	write(time.Now().Add(time.Minute))
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if err := GitCredential("get", file, strings.NewReader(request), &out); err == nil {
		t.Fatal("public credential accepted")
	}
}

func TestWorkspaceJournalSurvivesGrantRotationWithoutCredentials(t *testing.T) {
	dir := privateTemp(t)
	j, err := openJournal(dir, testGrant(), "https://camp.example")
	if err != nil {
		t.Fatal(err)
	}
	_, w, _ := checkoutFixture(t, "http://unused", strings.Repeat("a", 40))
	r := &workspaceRecord{Workspace: w, ClaimID: uuid.NewString(), Phase: "queued"}
	if err = j.saveWorkspace(r); err != nil {
		t.Fatal(err)
	}
	j.close()
	j, err = openJournal(dir, "campr_"+strings.Repeat("a", 43), "https://camp.example")
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	got, err := j.loadWorkspace()
	if err != nil || got.ClaimID != r.ClaimID {
		t.Fatal("metadata could not survive rotation", err)
	}
}
