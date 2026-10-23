package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func commitID(t *testing.T, dir string) string {
	t.Helper()
	head, _, err := revision(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return head
}
func committed(t *testing.T, dir, message string) string {
	t.Helper()
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-qm", message)
	return commitID(t, dir)
}
func archiveFixture(t *testing.T) (string, string, string) {
	t.Helper()
	source := repo(t)
	write(t, source, "base.txt", "Original base\n", 0600)
	committed(t, source, "First base")
	write(t, source, "second.txt", "Second base\n", 0600)
	base := committed(t, source, "Remote base")
	parent := t.TempDir()
	destination := filepath.Join(parent, "destination")
	gitTest(t, parent, "clone", "-q", "--no-hardlinks", source, destination)
	if err := os.Chmod(destination, 0700); err != nil {
		t.Fatal(err)
	}
	return source, destination, base
}
func TestCommitArchivePreservesUnpublishedMergeAndReplays(t *testing.T) {
	source, destination, base := archiveFixture(t)
	_, main, err := revision(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	// One merge parent forks before the selected base, requiring its ancestry.
	gitTest(t, source, "checkout", "-qb", "feature", base+"^")
	write(t, source, "feature.txt", "Feature from earlier ancestor\n", 0600)
	committed(t, source, "Feature author's commit")
	gitTest(t, source, "checkout", "-q", main)
	write(t, source, "binary.bin", "\x00\x01\xff\n", 0600)
	write(t, source, "empty.txt", "", 0600)
	write(t, source, "run.sh", "#!/bin/sh\necho fixture\n", 0700)
	committed(t, source, "Unpublished executable and binary")
	gitTest(t, source, "merge", "--no-ff", "-qm", "Preserve this merge", "feature")
	head := commitID(t, source)
	write(t, source, ".env", "UNSELECTED_PRIVATE_VALUE\n", 0600)
	write(t, source, "scratch.txt", "Uncommitted private note\n", 0600)
	archive, err := CaptureCommits(t.Context(), source, base, head)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(archive)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(source)) || bytes.Contains(raw, []byte("UNSELECTED_PRIVATE_VALUE")) {
		t.Fatal("Unselected source material entered archive")
	}
	decoded, err := DecodeCommitArchive(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := archive.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(decoded.Objects)-1; i < j; i, j = i+1, j-1 {
		decoded.Objects[i], decoded.Objects[j] = decoded.Objects[j], decoded.Objects[i]
	}
	reordered, _ := decoded.Digest()
	if digest != reordered {
		t.Fatal("Object order changed review")
	}
	original, err := git(t.Context(), source, 1<<20, "cat-file", "commit", head)
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		receipt, err := RestoreCommits(t.Context(), destination, decoded, digest)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.HeadCommit != head || receipt.BaseCommit != base || receipt.ReviewSHA256 != digest || receipt.Objects != len(archive.Objects) {
			t.Fatal("Wrong receipt")
		}
		if commitID(t, destination) != base {
			t.Fatal("Object installation moved HEAD")
		}
		status, err := git(t.Context(), destination, 1024, "status", "--porcelain")
		if err != nil || len(status) != 0 {
			t.Fatal("Object installation changed working files", err)
		}
		got, err := git(t.Context(), destination, 1<<20, "cat-file", "commit", head)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatal("Commit metadata or parent identity changed", err)
		}
	}
	// Checkout is an explicit separate operation, after object verification.
	gitTest(t, destination, "checkout", "-qb", "reviewed-transfer", head)
	for _, path := range []string{"feature.txt", "second.txt", "binary.bin", "empty.txt", "run.sh"} {
		want, e1 := os.ReadFile(filepath.Join(source, path))
		got, e2 := os.ReadFile(filepath.Join(destination, path))
		if e1 != nil || e2 != nil || !bytes.Equal(want, got) {
			t.Fatal("Lost committed file", path, e1, e2)
		}
	}
	mode, err := os.Stat(filepath.Join(destination, "run.sh"))
	if err != nil || mode.Mode()&0111 == 0 {
		t.Fatal("Lost executable bit", err)
	}
	for _, name := range []string{".env", "scratch.txt"} {
		if _, err := os.Stat(filepath.Join(destination, name)); !os.IsNotExist(err) {
			t.Fatal("Uncommitted source copied", name)
		}
	}
}
func TestCommitArchiveRefusesSecretsAcrossDeletedHistory(t *testing.T) {
	for _, kind := range []string{"token", "path", "message"} {
		t.Run(kind, func(t *testing.T) {
			source, _, base := archiveFixture(t)
			switch kind {
			case "token":
				write(t, source, "note.txt", "sk-proj-"+strings.Repeat("Z", 40), 0600)
				committed(t, source, "Temporary note")
				os.Remove(filepath.Join(source, "note.txt"))
			case "path":
				write(t, source, ".env", "PRIVATE=fixture", 0600)
				committed(t, source, "Temporary environment")
				os.Remove(filepath.Join(source, ".env"))
			case "message":
				write(t, source, "note.txt", "Safe file", 0600)
				committed(t, source, "ghp_"+strings.Repeat("Z", 36))
				write(t, source, "note.txt", "Another safe file", 0600)
			}
			head := committed(t, source, "Removed from current tree")
			if _, err := CaptureCommits(t.Context(), source, base, head); !errors.Is(err, ErrInput) {
				t.Fatal("Sensitive historical material accepted", err)
			}
		})
	}
}
func TestCommitArchiveRejectsIncompleteAndUnrelatedObjectsBeforeInstall(t *testing.T) {
	source, destination, base := archiveFixture(t)
	write(t, source, "new.txt", "New committed file", 0600)
	head := committed(t, source, "Unpublished")
	archive, err := CaptureCommits(t.Context(), source, base, head)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := archive.Digest()
	if _, err := RestoreCommits(t.Context(), destination, archive, strings.Repeat("0", 64)); !errors.Is(err, ErrChanged) {
		t.Fatal("Wrong review accepted", err)
	}
	missing := archive
	missing.Objects = nil
	for _, o := range archive.Objects {
		if o.Kind != "blob" {
			missing.Objects = append(missing.Objects, o)
		}
	}
	missingDigest, err := missing.Digest()
	if err != nil {
		t.Fatal("Fixture must reach connectivity verification", err)
	}
	if _, err := RestoreCommits(t.Context(), destination, missing, missingDigest); err == nil {
		t.Fatal("Missing blob accepted")
	}
	if _, err := git(t.Context(), destination, 1024, "cat-file", "-e", head); err == nil {
		t.Fatal("Rejected archive installed objects")
	}
	extra := archive
	body := []byte("Unrelated private object")
	extra.Objects = append(append([]CommitObject{}, archive.Objects...), CommitObject{gitObjectID("blob", body), "blob", body})
	if err := extra.Validate(); !errors.Is(err, ErrInput) {
		t.Fatal("Unreachable object accepted", err)
	}
	changed := archive
	changed.Objects = append([]CommitObject{}, archive.Objects...)
	changed.Objects[0].Data = append(bytes.Clone(changed.Objects[0].Data), '!')
	if err := changed.Validate(); !errors.Is(err, ErrInput) {
		t.Fatal("Changed object accepted", err)
	}
	write(t, destination, "local.txt", "Keep this file", 0600)
	if _, err := RestoreCommits(t.Context(), destination, archive, digest); !errors.Is(err, ErrDestination) {
		t.Fatal("Dirty checkout accepted", err)
	}
	got, _ := os.ReadFile(filepath.Join(destination, "local.txt"))
	if string(got) != "Keep this file" {
		t.Fatal("Local edit lost")
	}
}
func TestCommitArchiveBoundsAndSourceIdentity(t *testing.T) {
	source, _, base := archiveFixture(t)
	write(t, source, "new.txt", "New", 0600)
	head := committed(t, source, "Unpublished")
	if _, err := CaptureCommits(t.Context(), source, base, base); !errors.Is(err, ErrInput) {
		t.Fatal("Empty archive accepted", err)
	}
	if _, err := CaptureCommits(t.Context(), source, base, strings.Repeat("a", 40)); !errors.Is(err, ErrChanged) {
		t.Fatal("Different source HEAD accepted", err)
	}
	archive, err := CaptureCommits(t.Context(), source, base, head)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(archive)
	for _, invalid := range [][]byte{append(bytes.Clone(raw), []byte(" {}")...), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"unknown":true`), 1)} {
		if _, err := DecodeCommitArchive(invalid); !errors.Is(err, ErrInput) {
			t.Fatal("Invalid envelope accepted", err)
		}
	}
	archive.Objects = append(archive.Objects, CommitObject{ID: strings.Repeat("b", 40), Kind: "blob", Data: make([]byte, MaxFileBytes+1)})
	if err := archive.Validate(); !errors.Is(err, ErrLimit) {
		t.Fatal("Oversized object accepted", err)
	}
}

func TestCommitArchiveRequiresCompleteSourceHistory(t *testing.T) {
	source, _, base := archiveFixture(t)
	write(t, source, "new.txt", "New", 0600)
	head := committed(t, source, "Unpublished")
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, source, "worktree", "add", "--detach", linked, head)
	if _, err := CaptureCommits(t.Context(), linked, base, head); err != nil {
		t.Fatal("Ordinary linked source worktree rejected", err)
	}
	// Even a graft that does not affect this particular range is not a trusted
	// description of history. Linked worktrees must inspect the common store.
	grafts := filepath.Join(source, ".git", "info", "grafts")
	if err := os.WriteFile(grafts, []byte(base+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureCommits(t.Context(), linked, base, head); !errors.Is(err, ErrRepository) {
		t.Fatal("Grafted ancestry accepted", err)
	}
	if err := os.Remove(grafts); err != nil {
		t.Fatal(err)
	}
	shallow := filepath.Join(t.TempDir(), "shallow")
	gitTest(t, source, "clone", "-q", "--depth=2", "file://"+source, shallow)
	if _, err := CaptureCommits(t.Context(), shallow, base, head); !errors.Is(err, ErrRepository) {
		t.Fatal("Shallow ancestry accepted", err)
	}
}

func TestCommitArchiveDestinationRechecksHistoricalTrees(t *testing.T) {
	for _, kind := range []string{"excluded-path", "escaping-symlink"} {
		t.Run(kind, func(t *testing.T) {
			source, destination, base := archiveFixture(t)
			switch kind {
			case "excluded-path":
				write(t, source, ".env", "PRIVATE=fixture", 0600)
			case "escaping-symlink":
				if err := os.Symlink("../../private", filepath.Join(source, "link")); err != nil {
					t.Fatal(err)
				}
			}
			head := committed(t, source, "Unsafe unpublished tree")
			// Construct the wire archive without the trusted capture function.
			// The destination must independently enforce the tree policy.
			ids, err := commitObjectIDs(t.Context(), source, base, head)
			if err != nil {
				t.Fatal(err)
			}
			objects, err := readCommitObjects(t.Context(), source, ids)
			if err != nil {
				t.Fatal(err)
			}
			archive := CommitArchive{1, base, head, objects}
			digest, err := archive.Digest()
			if err != nil {
				t.Fatal("Fixture must reach destination tree validation", err)
			}
			if _, err = RestoreCommits(t.Context(), destination, archive, digest); err == nil {
				t.Fatal("Unsafe tree installed")
			}
			if _, err = git(t.Context(), destination, 1024, "cat-file", "-e", head); err == nil {
				t.Fatal("Rejected archive escaped quarantine")
			}
			left, err := filepath.Glob(filepath.Join(filepath.Dir(destination), ".camp-commit-review-*"))
			if err != nil || len(left) != 0 {
				t.Fatal("Quarantine was not removed", left, err)
			}
		})
	}
}

func TestCommitArchiveCancellationDoesNotInstall(t *testing.T) {
	source, destination, base := archiveFixture(t)
	write(t, source, "new.txt", "New", 0600)
	head := committed(t, source, "Unpublished")
	archive, err := CaptureCommits(t.Context(), source, base, head)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := archive.Digest()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := CaptureCommits(ctx, source, base, head); !errors.Is(err, context.Canceled) {
		t.Fatal("Cancelled capture continued", err)
	}
	if _, err := RestoreCommits(ctx, destination, archive, digest); !errors.Is(err, context.Canceled) {
		t.Fatal("Cancelled restore continued", err)
	}
	if _, err := git(t.Context(), destination, 1024, "cat-file", "-e", head); err == nil {
		t.Fatal("Cancelled archive installed")
	}
}

func TestTransferGitRefusesRepositoryProtocolOverrides(t *testing.T) {
	source, destination, _ := archiveFixture(t)
	// A per-protocol repository override takes precedence over protocol.allow.
	// The environment allowlist must still forbid every transport. This uses a
	// local fixture so a regression cannot contact a real provider.
	gitTest(t, destination, "config", "protocol.file.allow", "always")
	if _, err := git(t.Context(), destination, 8192, "ls-remote", "file://"+source); !errors.Is(err, ErrRepository) {
		t.Fatal("Repository configuration enabled a transport", err)
	}
}

func TestCommitArchiveManifestAndWorkingChanges(t *testing.T) {
	source, destination, base := archiveFixture(t)
	write(t, source, "new.txt", "Committed contents\n", 0600)
	head := committed(t, source, "Unpublished")
	write(t, source, "new.txt", "Selected working changes\n", 0600)
	m, err := SnapshotWithCommits(t.Context(), source, []string{"new.txt"}, base)
	if err != nil {
		t.Fatal(err)
	}
	review, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	m, err = Decode(raw)
	if err != nil || m.BaseCommit != head || m.Commits == nil || m.Commits.BaseCommit != base {
		t.Fatal("Archive lost its remote base or working-change head", err)
	}
	objects := m.Commits.Objects
	for i, j := 0, len(objects)-1; i < j; i, j = i+1, j-1 {
		objects[i], objects[j] = objects[j], objects[i]
	}
	got, err := m.Digest()
	if err != nil || got != review {
		t.Fatal("Wire object order changed approval", err)
	}
	without := m
	without.Commits = nil
	unbound, err := without.Digest()
	if err != nil || unbound == review {
		t.Fatal("Archive was not bound to approval", err)
	}
	// Preserve the prior digest for transfers that have no commit archive.
	legacy := without
	legacy.Files = append([]File{}, without.Files...)
	for i := range legacy.Files {
		legacy.Files[i].Data = nil
	}
	legacyRaw, _ := json.Marshal(legacy)
	if unbound != hash(append([]byte("capybara-transfer-v1\n"), legacyRaw...)) {
		t.Fatal("Existing transfer approval changed")
	}
	wrong := m
	wrong.BaseCommit = base
	if err := wrong.Validate(); !errors.Is(err, ErrInput) {
		t.Fatal("Wrong file base accepted", err)
	}
	archiveReview, _ := m.Commits.Digest()
	if _, err := RestoreCommits(t.Context(), destination, *m.Commits, archiveReview); err != nil {
		t.Fatal(err)
	}
	branch := "camp/reviewed-commits"
	gitTest(t, destination, "checkout", "-qb", branch, head)
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	stager, err := OpenStager(destination, state)
	if err != nil {
		t.Fatal(err)
	}
	defer stager.Close()
	for i := 0; i < 2; i++ {
		if _, err := stager.Apply(t.Context(), m, review, branch); err != nil {
			t.Fatal(err)
		}
	}
	contents, err := os.ReadFile(filepath.Join(destination, "new.txt"))
	if err != nil || string(contents) != "Selected working changes\n" || commitID(t, destination) != head {
		t.Fatal("Working changes or commit identity lost", err)
	}
	// Commit objects and selected working files share one payload byte budget.
	oversized := m
	oversized.Files = nil
	body := bytes.Repeat([]byte("x"), MaxFileBytes)
	for i := 0; i < MaxTotalBytes/MaxFileBytes; i++ {
		oversized.Files = append(oversized.Files, File{Path: string(rune('a'+i)) + ".txt", Data: body, SHA256: hash(body)})
	}
	if err := oversized.Validate(); !errors.Is(err, ErrLimit) {
		t.Fatal("Combined payload limit bypassed", err)
	}
}
