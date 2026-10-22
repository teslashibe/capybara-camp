package transfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitTest(t, dir, "init", "-q")
	gitTest(t, dir, "config", "user.name", "Transfer fixture")
	gitTest(t, dir, "config", "user.email", "fixture@example.com")
	return dir
}

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("/usr/bin/git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)...)
	command.Dir = dir
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LANG=C"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Git fixture failed: %v %s", err, output)
	}
}

func write(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureSelectedChangesAndVerifySource(t *testing.T) {
	dir := repo(t)
	write(t, dir, "source.txt", "before\n", 0600)
	write(t, dir, "removed.txt", "remove me\n", 0600)
	write(t, dir, "same.txt", "unchanged\n", 0600)
	write(t, dir, "script.sh", "script\n", 0600)
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-qm", "Fixture base")
	write(t, dir, "source.txt", "after\n", 0600)
	write(t, dir, "new.txt", "new file\n", 0600)
	write(t, dir, "not-selected.txt", "keep private\n", 0600)
	if err := os.Remove(filepath.Join(dir, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "script.sh"), 0700); err != nil {
		t.Fatal(err)
	}
	m, err := Capture(t.Context(), dir, []string{"new.txt", "source.txt", "same.txt", "script.sh", "removed.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 4 || len(m.Excluded) != 1 || m.Excluded[0] != (Exclusion{"same.txt", "unchanged"}) {
		t.Fatal("unexpected selected change inventory", len(m.Files), m.Excluded)
	}
	files := map[string]File{}
	for _, file := range m.Files {
		files[file.Path] = file
	}
	if files["source.txt"].BeforeSHA256 != hash([]byte("before\n")) || string(files["source.txt"].Data) != "after\n" {
		t.Fatal("lost source preimage or content")
	}
	if files["new.txt"].BeforeSHA256 != "" || !files["removed.txt"].Deleted || !files["script.sh"].Executable || files["script.sh"].BeforeExecutable {
		t.Fatal("lost add, delete or mode change")
	}
	raw, err := json.Marshal(m)
	if err != nil || bytes.Contains(raw, []byte("keep private")) || bytes.Contains(raw, []byte(dir)) {
		t.Fatal("unselected data or local path entered payload", err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := m.Digest()
	again, _ := decoded.Digest()
	if digest != again {
		t.Fatal("wire round-trip changed reviewed content")
	}
	decoded.Files[0], decoded.Files[1] = decoded.Files[1], decoded.Files[0]
	again, _ = decoded.Digest()
	if digest != again {
		t.Fatal("order changed the reviewed digest")
	}
	if err = VerifySource(t.Context(), dir, m); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdirectory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = VerifySource(t.Context(), filepath.Join(dir, "subdirectory"), m); !errors.Is(err, ErrRepository) {
		t.Fatal("a different working root was accepted", err)
	}
	write(t, dir, "source.txt", "changed after review\n", 0600)
	if err = VerifySource(t.Context(), dir, m); !errors.Is(err, ErrChanged) {
		t.Fatal("source drift was accepted", err)
	}
	write(t, dir, "source.txt", "after\n", 0600)
	gitTest(t, dir, "add", "new.txt")
	gitTest(t, dir, "commit", "-qm", "Different base")
	if err = VerifySource(t.Context(), dir, m); !errors.Is(err, ErrChanged) {
		t.Fatal("a different Git base was accepted", err)
	}
}

func TestCaptureExclusionsNeverReadCredentialOrLinkedContents(t *testing.T) {
	dir := repo(t)
	write(t, dir, "base.txt", "base", 0600)
	write(t, dir, ".env", "do not export this file", 0600)
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-qm", "Fixture base")
	write(t, dir, ".codex/auth.json", "private profile", 0600)
	write(t, dir, "token.txt", "sk-proj-"+strings.Repeat("Z", 40), 0600)
	write(t, dir, "key-note.txt", "-----BEGIN PGP PRIVATE KEY BLOCK-----\nfixture-only\n-----END PGP PRIVATE KEY BLOCK-----", 0600)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(dir, "hard.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	m, err := Capture(t.Context(), dir, []string{".env", ".codex/auth.json", "token.txt", "key-note.txt", "link.txt", "hard.txt", "folder"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 0 || len(m.Excluded) != 7 {
		t.Fatal("unsafe contents were exported", len(m.Files))
	}
	reasons := map[string]string{}
	for _, e := range m.Excluded {
		reasons[e.Path] = e.Reason
	}
	if reasons[".env"] != "policy" || reasons[".codex/auth.json"] != "policy" || reasons["token.txt"] != "policy" || reasons["key-note.txt"] != "policy" || reasons["link.txt"] != "symlink" || reasons["hard.txt"] != "hardlink" || reasons["folder"] != "unsupported" {
		t.Fatal("wrong exclusion reasons", reasons)
	}
	raw, _ := json.Marshal(m)
	for _, secret := range []string{"private profile", "outside data", strings.Repeat("Z", 40), "do not export"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("excluded content entered the manifest")
		}
	}
}

func TestCaptureRejectsTraversalAliasesLimitsAndHooks(t *testing.T) {
	dir := repo(t)
	write(t, dir, "file.txt", "original", 0600)
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-qm", "Fixture base")
	for _, names := range [][]string{{"../file"}, {"/absolute"}, {"a/../file"}, {".git/config"}, {"folder\\file"}, {"file.txt", "FILE.TXT"}, {"line\nbreak"}, {"e\u0301.txt"}} {
		if _, err := Capture(t.Context(), dir, names); !errors.Is(err, ErrInput) {
			t.Fatal("unsafe selection accepted", names, err)
		}
	}
	write(t, dir, "too-big", strings.Repeat("x", MaxFileBytes+1), 0600)
	if _, err := Capture(t.Context(), dir, []string{"too-big"}); !errors.Is(err, ErrLimit) {
		t.Fatal("oversized file accepted", err)
	}
	gitTest(t, dir, "add", "too-big")
	gitTest(t, dir, "commit", "-qm", "Large fixture blob")
	write(t, dir, "too-big", "small replacement", 0600)
	if _, err := Capture(t.Context(), dir, []string{"too-big"}); !errors.Is(err, ErrLimit) {
		t.Fatal("oversized base blob was not bounded", err)
	}
	if _, err := Capture(t.Context(), dir, []string{"missing"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing new file treated as a deletion", err)
	}
	write(t, dir, "monitor.sh", "#!/bin/sh\n: > \"${0%/*}/hook-ran\"\nexit 77\n", 0700)
	gitTest(t, dir, "config", "core.fsmonitor", filepath.Join(dir, "monitor.sh"))
	write(t, dir, "file.txt", "modified", 0600)
	if _, err := Capture(t.Context(), dir, []string{"file.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hook-ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("capture executed repository configuration", err)
	}
	gitTest(t, dir, "checkout", "--detach", "-q")
	m, err := Capture(t.Context(), dir, []string{"file.txt"})
	if err != nil || m.Branch != "" {
		t.Fatal("detached checkout did not retain its pinned commit", err)
	}
}

func TestManifestRejectsTamperingAndIncludesExclusionsInReview(t *testing.T) {
	m := Manifest{Version: 1, BaseCommit: strings.Repeat("a", 40), Files: []File{{Path: "a.txt", SHA256: hash([]byte("contents")), Data: []byte("contents")}}}
	digest, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	m.Excluded = []Exclusion{{Path: ".env", Reason: "policy"}}
	changed, _ := m.Digest()
	if changed == digest {
		t.Fatal("excluded paths were not covered by review")
	}
	m.Files[0].Data = []byte("tampered")
	if err = m.Validate(); !errors.Is(err, ErrInput) {
		t.Fatal("modified bytes passed validation", err)
	}
	m.Files[0].Data = []byte("contents")
	m.Files = append(m.Files, m.Files[0])
	if err = m.Validate(); !errors.Is(err, ErrInput) {
		t.Fatal("duplicate paths passed validation", err)
	}
	m.Files = m.Files[:1]
	raw, _ := json.Marshal(m)
	if _, err = Decode(append(raw, []byte("{}")...)); !errors.Is(err, ErrInput) {
		t.Fatal("trailing JSON accepted", err)
	}
	if _, err = Decode(bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1)); !errors.Is(err, ErrInput) {
		t.Fatal("unknown version accepted", err)
	}
	if _, err = Decode(bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"unknown":true`), 1)); !errors.Is(err, ErrInput) {
		t.Fatal("unknown contract accepted", err)
	}
}
