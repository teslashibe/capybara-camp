package transfer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestChangesAndExplicitEmptySnapshot(t *testing.T) {
	dir := repo(t)
	write(t, dir, "old.txt", "old\n", 0600)
	write(t, dir, "change.txt", "before\n", 0600)
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-qm", "Initial")
	gitTest(t, dir, "mv", "old.txt", "renamed.txt")
	write(t, dir, "change.txt", "after\n", 0600)
	write(t, dir, "added.txt", "new\n", 0600)
	write(t, dir, ".env", "SECRET=fixture\n", 0600)
	if err := os.Symlink("change.txt", filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	changes, err := Changes(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]Change{}
	for _, c := range changes.Files {
		found[c.Path] = c
	}
	if found["old.txt"].Kind != "deleted" || found["renamed.txt"].Kind != "added" || found["change.txt"].Kind != "modified" || found["added.txt"].Kind != "added" || found[".env"].Excluded == "" || found["linked"].Excluded == "" {
		t.Fatal("Incomplete change review", found)
	}
	empty, err := Snapshot(t.Context(), dir, []string{})
	if err != nil || empty.BaseCommit != changes.BaseCommit || len(empty.Files) != 0 {
		t.Fatal("Explicit no-file move failed", err)
	}
	if _, err = Snapshot(t.Context(), dir, nil); !errors.Is(err, ErrInput) {
		t.Fatal("Missing selection was accepted", err)
	}
	selected, err := Snapshot(t.Context(), dir, []string{"added.txt", ".env"})
	if err != nil || len(selected.Files) != 1 || len(selected.Excluded) != 1 {
		t.Fatal("Selected snapshot failed", err)
	}
}
