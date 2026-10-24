// Package testgit supplies a real loopback smart-HTTP Git fixture to tests.
package testgit

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func Command(t *testing.T, dir string, args ...string) string {
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
func Server(t *testing.T, seed func(string), before func(http.ResponseWriter, *http.Request) bool) (string, string, *atomic.Int32) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo.git")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	Command(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("pinned version\n"), 0600); err != nil {
		t.Fatal(err)
	}
	Command(t, repo, "add", ".")
	if seed != nil {
		seed(repo)
	}
	Command(t, repo, "commit", "-m", "Fixture")
	sha := Command(t, repo, "rev-parse", "HEAD")
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
