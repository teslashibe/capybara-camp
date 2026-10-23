package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type stageFixture struct {
	source, destination, state, branch, review string
	manifest                                   Manifest
}

func newStageFixture(t *testing.T) stageFixture {
	t.Helper()
	source := repo(t)
	write(t, source, "source.txt", "before\n", 0600)
	write(t, source, "removed.txt", "remove me\n", 0600)
	write(t, source, "script.sh", "run me\n", 0600)
	write(t, source, "other.txt", "unchanged\n", 0600)
	write(t, source, ".gitignore", ".env\n", 0600)
	gitTest(t, source, "add", ".")
	gitTest(t, source, "commit", "-qm", "Base")
	destination, state := t.TempDir(), t.TempDir()
	gitTest(t, destination, "clone", "-q", "--no-local", source, ".")
	for _, dir := range []string{destination, state} {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	branch := "camp/transfer-fixture"
	gitTest(t, destination, "checkout", "-qb", branch)
	write(t, source, "source.txt", "after\n", 0600)
	write(t, source, "new/nested/file.txt", "new file\n", 0600)
	if err := os.Remove(filepath.Join(source, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "script.sh"), 0700); err != nil {
		t.Fatal(err)
	}
	m, err := Capture(t.Context(), source, []string{"source.txt", "new/nested/file.txt", "removed.txt", "script.sh"})
	if err != nil {
		t.Fatal(err)
	}
	review, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return stageFixture{source, destination, state, branch, review, m}
}

func openFixtureStage(t *testing.T, f stageFixture) *Stager {
	t.Helper()
	s, err := OpenStager(f.destination, f.state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func assertFile(t *testing.T, dir, name, expected string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(body) != expected {
		t.Fatalf("unexpected %s contents or read failure: %v", name, err)
	}
}

func TestStageAppliesReviewedFilesAndReplaysVerifiedReceipt(t *testing.T) {
	f := newStageFixture(t)
	s := openFixtureStage(t, f)
	otherState := t.TempDir()
	if err := os.Chmod(otherState, 0700); err != nil {
		t.Fatal(err)
	}
	if other, err := OpenStager(f.destination, otherState); err == nil {
		other.Close()
		t.Fatal("two journal directories acquired the same checkout")
	}
	receipt, err := s.Apply(t.Context(), f.manifest, f.review, f.branch)
	if err != nil || receipt.ReviewSHA256 != f.review || receipt.BaseCommit != f.manifest.BaseCommit || receipt.Branch != f.branch || receipt.Files != len(f.manifest.Files) {
		t.Fatal(receipt, err)
	}
	assertFile(t, f.destination, "source.txt", "after\n")
	assertFile(t, f.destination, "new/nested/file.txt", "new file\n")
	assertFile(t, f.destination, "other.txt", "unchanged\n")
	if _, err = os.Lstat(filepath.Join(f.destination, "removed.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deletion not applied", err)
	}
	info, err := os.Stat(filepath.Join(f.destination, "script.sh"))
	if err != nil || info.Mode()&0111 == 0 || info.Mode().Perm()&0077 != 0 {
		t.Fatal("executable mode or privacy lost", err)
	}
	head, branch, err := revision(t.Context(), f.destination)
	if err != nil || head != f.manifest.BaseCommit || branch != f.branch {
		t.Fatal("Git history changed", err)
	}
	if err = VerifySource(t.Context(), f.source, f.manifest); err != nil {
		t.Fatal("destination write changed source", err)
	}
	before, err := os.ReadFile(filepath.Join(f.state, "stage.json"))
	if err != nil || bytes.Contains(before, []byte("after\n")) {
		t.Fatal("missing journal or journal contains payload", err)
	}
	s.Close()
	s = openFixtureStage(t, f)
	again, err := s.Apply(t.Context(), f.manifest, f.review, f.branch)
	if err != nil || again != receipt {
		t.Fatal("restart did not reproduce verified receipt", err)
	}
	after, err := os.ReadFile(filepath.Join(f.state, "stage.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("receipt replay rewrote journal", err)
	}
	entries, err := os.ReadDir(f.state)
	if err != nil || len(entries) != 2 {
		t.Fatal("temporary files retained", err)
	}
	write(t, f.destination, "source.txt", "edited after staging\n", 0600)
	if _, err = s.Apply(t.Context(), f.manifest, f.review, f.branch); !errors.Is(err, ErrDestination) {
		t.Fatal("completed stage accepted changed files", err)
	}
	assertFile(t, f.destination, "source.txt", "edited after staging\n")
}

func TestStageResumesPartialFilesWithoutDiscardingConflicts(t *testing.T) {
	f := newStageFixture(t)
	s := openFixtureStage(t, f)
	receipt := StageReceipt{f.review, f.manifest.BaseCommit, f.branch, len(f.manifest.Files)}
	if err := s.save(stageRecord{Version: 1, Root: s.dir, Inode: s.inode, Receipt: receipt}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyFile(t.Context(), f.manifest.Files[0]); err != nil {
		t.Fatal(err)
	}
	temporary, err := s.temporary([]byte("interrupted temporary bytes"), false)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openFixtureStage(t, f)
	write(t, f.destination, "other.txt", "unexpected work\n", 0600)
	if _, err = s.Apply(t.Context(), f.manifest, f.review, f.branch); !errors.Is(err, ErrDestination) {
		t.Fatal("resume discarded unrelated work", err)
	}
	assertFile(t, f.destination, "other.txt", "unexpected work\n")
	if _, err = os.Stat(filepath.Join(f.state, temporary)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("abandoned temporary file retained", err)
	}
	write(t, f.destination, "other.txt", "unchanged\n", 0600)
	changed := f.manifest
	changed.Branch = "different source"
	changedReview, err := changed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), changed, changedReview, f.branch); !errors.Is(err, ErrDestination) {
		t.Fatal("different reviewed transfer resumed old journal", err)
	}
	again, err := s.Apply(t.Context(), f.manifest, f.review, f.branch)
	if err != nil || again != receipt {
		t.Fatal("partial transfer did not resume", err)
	}
	assertFile(t, f.destination, "source.txt", "after\n")
	assertFile(t, f.destination, "new/nested/file.txt", "new file\n")
}

func TestStageRejectsConflictsBeforeWritingAnyFiles(t *testing.T) {
	for _, kind := range []string{"changed file", "untracked file", "ignored file", "staged file", "wrong branch", "wrong base", "false preimage", "symlink", "hardlink", "case alias", "review hash"} {
		t.Run(kind, func(t *testing.T) {
			f := newStageFixture(t)
			s := openFixtureStage(t, f)
			switch kind {
			case "changed file":
				write(t, f.destination, "source.txt", "keep these edits\n", 0600)
			case "untracked file":
				write(t, f.destination, "notes.txt", "keep these notes\n", 0600)
			case "ignored file":
				write(t, f.destination, ".env", "keep this private\n", 0600)
			case "staged file":
				write(t, f.destination, "other.txt", "staged\n", 0600)
				gitTest(t, f.destination, "add", "other.txt")
			case "wrong branch":
				f.branch = "other/branch"
			case "wrong base":
				f.manifest.BaseCommit = strings.Repeat("b", 40)
			case "false preimage":
				for i := range f.manifest.Files {
					if f.manifest.Files[i].Path == "source.txt" {
						f.manifest.Files[i].BeforeSHA256 = hash([]byte("false base"))
					}
				}
			case "symlink", "hardlink":
				outside := filepath.Join(t.TempDir(), "outside.txt")
				write(t, filepath.Dir(outside), filepath.Base(outside), "before\n", 0600)
				if err := os.Remove(filepath.Join(f.destination, "source.txt")); err != nil {
					t.Fatal(err)
				}
				link := os.Link
				if kind == "symlink" {
					link = os.Symlink
				}
				if err := link(outside, filepath.Join(f.destination, "source.txt")); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { assertFile(t, filepath.Dir(outside), filepath.Base(outside), "before\n") })
			case "case alias":
				f.manifest.Files = append(f.manifest.Files, File{Path: "OTHER.txt", SHA256: hash([]byte("replacement")), Data: []byte("replacement")})
			}
			f.review, _ = f.manifest.Digest()
			if kind == "review hash" {
				f.review = strings.Repeat("a", 64)
			}
			if _, err := s.Apply(t.Context(), f.manifest, f.review, f.branch); err == nil {
				t.Fatal("unsafe destination accepted")
			}
			if _, err := os.Stat(filepath.Join(f.destination, "new")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("wrote files before preflight completed", err)
			}
			assertFile(t, f.destination, "removed.txt", "remove me\n")
			if _, err := os.Stat(filepath.Join(f.state, "stage.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("journal admitted a conflicting destination", err)
			}
		})
	}
}

func TestStageBindsJournalToOneCheckoutAndRefusesUnsafeState(t *testing.T) {
	f := newStageFixture(t)
	s := openFixtureStage(t, f)
	if _, err := s.Apply(t.Context(), f.manifest, f.review, f.branch); err != nil {
		t.Fatal(err)
	}
	s.Close()
	other := t.TempDir()
	gitTest(t, other, "clone", "-q", "--no-local", f.source, ".")
	if err := os.Chmod(other, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, other, "checkout", "-qb", f.branch)
	foreign, err := OpenStager(other, f.state)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if _, err = foreign.Apply(t.Context(), f.manifest, f.review, f.branch); !errors.Is(err, ErrDestination) {
		t.Fatal("journal reused on another checkout", err)
	}
	for _, kind := range []string{"public checkout", "nested state", "symlink journal", "invalid journal", "changed root"} {
		t.Run(kind, func(t *testing.T) {
			f := newStageFixture(t)
			if kind == "public checkout" {
				if err := os.Chmod(f.destination, 0755); err != nil {
					t.Fatal(err)
				}
				if s, err := OpenStager(f.destination, f.state); err == nil {
					s.Close()
					t.Fatal("public checkout accepted")
				}
				return
			}
			if kind == "nested state" {
				state := filepath.Join(f.destination, "state")
				if err := os.Mkdir(state, 0700); err != nil {
					t.Fatal(err)
				}
				if s, err := OpenStager(f.destination, state); err == nil {
					s.Close()
					t.Fatal("state directory inside checkout accepted")
				}
				return
			}
			s := openFixtureStage(t, f)
			switch kind {
			case "symlink journal":
				outside := filepath.Join(t.TempDir(), "outside.json")
				if err := os.WriteFile(outside, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(f.state, "stage.json")); err != nil {
					t.Fatal(err)
				}
			case "invalid journal":
				if err := os.WriteFile(filepath.Join(f.state, "stage.json"), []byte(`{"version":1`), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed root":
				if err := os.Rename(f.destination, f.destination+"-old"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(f.destination + "-old") })
				if err := os.Mkdir(f.destination, 0700); err != nil {
					t.Fatal(err)
				}
				gitTest(t, f.destination, "clone", "-q", "--no-local", f.source, ".")
				gitTest(t, f.destination, "checkout", "-qb", f.branch)
			}
			if _, err := s.Apply(t.Context(), f.manifest, f.review, f.branch); !errors.Is(err, ErrDestination) {
				t.Fatal("unsafe journal or checkout accepted", err)
			}
			assertFile(t, f.destination, "source.txt", "before\n")
		})
	}
}

func TestStageCancellationAndManifestPathCollisionsDoNotWrite(t *testing.T) {
	f := newStageFixture(t)
	s := openFixtureStage(t, f)
	s.mu.Lock()
	_, busyErr := s.Apply(t.Context(), f.manifest, f.review, f.branch)
	s.mu.Unlock()
	if !errors.Is(busyErr, ErrStageBusy) {
		t.Fatal("overlapping staging did not fail immediately", busyErr)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Apply(ctx, f.manifest, f.review, f.branch); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled staging proceeded")
	}
	assertFile(t, f.destination, "source.txt", "before\n")
	for _, paths := range [][]string{{"new", "new/file"}, {"Dir/a", "dir/b"}, {"x/A/a", "x/a/b"}, {"a/file", "A"}} {
		m := Manifest{Version: 1, BaseCommit: f.manifest.BaseCommit}
		for _, name := range paths {
			m.Files = append(m.Files, File{Path: name, Data: []byte("contents"), SHA256: hash([]byte("contents"))})
		}
		if err := m.Validate(); !errors.Is(err, ErrInput) {
			t.Fatal("overlapping or aliased manifest paths accepted", paths, err)
		}
	}
	// An abandoned temporary file is safe to remove after acquiring both locks.
	if err := os.WriteFile(filepath.Join(f.state, "stage-temp-"+uuid.NewString()), []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), f.manifest, f.review, f.branch); err != nil {
		t.Fatal(err)
	}
}

type exitFixture struct {
	Destination string   `json:"destination"`
	State       string   `json:"state"`
	Branch      string   `json:"branch"`
	Review      string   `json:"review"`
	Manifest    Manifest `json:"manifest"`
}

func TestStageProcessExit(t *testing.T) {
	path := os.Getenv("CAMP_STAGE_EXIT_FIXTURE")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture exitFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStager(fixture.Destination, fixture.State)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	receipt := StageReceipt{fixture.Review, fixture.Manifest.BaseCommit, fixture.Branch, len(fixture.Manifest.Files)}
	if err := s.save(stageRecord{Version: 1, Root: s.dir, Inode: s.inode, Receipt: receipt}); err != nil {
		t.Fatal(err)
	}
	if err := s.applyFile(t.Context(), fixture.Manifest.Files[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.temporary([]byte("unfinished file write"), false); err != nil {
		t.Fatal(err)
	}
	// Exit without defer or normal cleanup, as a stopped process would.
	os.Exit(17)
}

func TestStageRecoversAfterProcessExit(t *testing.T) {
	f := newStageFixture(t)
	raw, err := json.Marshal(exitFixture{f.destination, f.state, f.branch, f.review, f.manifest})
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(t.TempDir(), "fixture.json")
	if err = os.WriteFile(fixturePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestStageProcessExit$")
	command.Env = append(os.Environ(), "CAMP_STAGE_EXIT_FIXTURE="+fixturePath)
	err = command.Run()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 17 {
		t.Fatalf("fixture did not exit at interruption: %v", err)
	}
	s := openFixtureStage(t, f)
	if _, err = s.Apply(t.Context(), f.manifest, f.review, f.branch); err != nil {
		t.Fatal("recovery after process exit", err)
	}
	assertFile(t, f.destination, "source.txt", "after\n")
	assertFile(t, f.destination, "new/nested/file.txt", "new file\n")
	entries, err := os.ReadDir(f.state)
	if err != nil || len(entries) != 2 {
		t.Fatal("interrupted files retained", err)
	}
}

func TestStageDoesNotExecuteRepositoryDiffOrMonitorCommands(t *testing.T) {
	f := newStageFixture(t)
	marker := filepath.Join(t.TempDir(), "executed")
	helper := filepath.Join(f.destination, ".git", "untrusted-helper")
	script := "#!/bin/sh\n: > '" + marker + "'\nexit 77\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, f.destination, "config", "diff.external", helper)
	gitTest(t, f.destination, "config", "core.fsmonitor", helper)
	s := openFixtureStage(t, f)
	if _, err := s.Apply(t.Context(), f.manifest, f.review, f.branch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("executed repository command", err)
	}
}

func TestStageAppliesChangesToSecretBearingBase(t *testing.T) {
	source := repo(t)
	key := "AKIA" + strings.Repeat("Q", 16)
	write(t, source, "edited.txt", "key = "+key+"\n", 0600)
	write(t, source, "deleted.txt", "key = "+key+"\n", 0600)
	gitTest(t, source, "add", ".")
	gitTest(t, source, "commit", "-qm", "Base")
	destination, state := t.TempDir(), t.TempDir()
	gitTest(t, destination, "clone", "-q", "--no-local", source, ".")
	for _, dir := range []string{destination, state} {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	branch := "camp/secret-base"
	gitTest(t, destination, "checkout", "-qb", branch)
	write(t, source, "edited.txt", "key = removed\n", 0600)
	if err := os.Remove(filepath.Join(source, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	m, err := Capture(t.Context(), source, []string{"edited.txt", "deleted.txt"})
	if err != nil || len(m.Files) != 2 {
		t.Fatal("Capture refused a change that removes a secret", m.Excluded, err)
	}
	review, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenStager(destination, state)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Apply(t.Context(), m, review, branch); err != nil {
		t.Fatal("Destination refused a reviewed change to a secret-bearing base", err)
	}
	assertFile(t, destination, "edited.txt", "key = removed\n")
	if _, err = os.Lstat(filepath.Join(destination, "deleted.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deletion not applied", err)
	}
}
