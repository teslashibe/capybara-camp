//go:build linux

package transfer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/teslashibe/capybara-camp/server/internal/processguard"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "process-guard" {
		if err := processguard.Run(os.Args[2:]); err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(90)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestIsolatedChangesKeepRepositoryFiltersInsideGuard(t *testing.T) {
	if err := processguard.Available(); err != nil {
		if os.Getenv("CAMP_REQUIRE_PROCESS_GUARD") == "1" {
			t.Fatal("Test environment must enforce Landlock ABI 6 or later:", err)
		}
		t.Skip("Landlock ABI 6 unavailable; run tools/test-processguard.sh before release")
	}
	work, control := filterRepo(t)
	ran, copied := filepath.Join(work, ".git", "ran"), filepath.Join(control, "copied")

	// Plain Git runs the provider's filter with the caller's access. This
	// proves the fixture reaches the filter before checking the guarded path.
	statDirty(t, work)
	if _, err := Changes(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(copied); err != nil {
		t.Fatal("Fixture filter did not run under plain Git", err)
	}
	for _, name := range []string{ran, copied} {
		if err := os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}

	statDirty(t, work)
	changes, err := Changes(Isolated(t.Context(), t.TempDir()), work)
	if err != nil {
		t.Fatal("Guarded change review failed", err)
	}
	if _, err = os.Stat(ran); err != nil {
		t.Fatal("Filter did not run inside the guard, so the check proves nothing", err)
	}
	if _, err = os.Stat(copied); !os.IsNotExist(err) {
		t.Fatal("Repository filter read the control file outside the guard", err)
	}
	for _, c := range changes.Files {
		if c.Path == "source.txt" {
			t.Fatal("Unchanged content was reported as modified", changes.Files)
		}
	}
}

// Runner integration tests run unguarded, so this covers every transfer step
// a cloud runner performs on a provider workspace with the guard enforced.
func TestIsolatedTransferRoundTrip(t *testing.T) {
	if err := processguard.Available(); err != nil {
		if os.Getenv("CAMP_REQUIRE_PROCESS_GUARD") == "1" {
			t.Fatal("Test environment must enforce Landlock ABI 6 or later:", err)
		}
		t.Skip("Landlock ABI 6 unavailable; run tools/test-processguard.sh before release")
	}
	source, destination, base := archiveFixture(t)
	write(t, source, "new.txt", "Committed contents\n", 0600)
	head := committed(t, source, "Unpublished")
	write(t, source, "new.txt", "Selected working changes\n", 0600)
	ctx := Isolated(t.Context(), t.TempDir())
	changes, err := Changes(ctx, source)
	if err != nil || changes.BaseCommit != head || len(changes.Files) != 1 || changes.Files[0].Path != "new.txt" {
		t.Fatal("Guarded change review failed", changes, err)
	}
	m, err := SnapshotWithCommits(ctx, source, []string{"new.txt"}, base)
	if err != nil || m.Commits == nil {
		t.Fatal("Guarded capture failed", err)
	}
	if err = VerifySource(ctx, source, m); err != nil {
		t.Fatal("Guarded source verification failed", err)
	}
	review, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if m, err = Decode(raw); err != nil {
		t.Fatal(err)
	}
	archiveReview, _ := m.Commits.Digest()
	if _, err = RestoreCommits(ctx, destination, *m.Commits, archiveReview); err != nil {
		t.Fatal("Guarded commit restore failed", err)
	}
	branch := "camp/reviewed-commits"
	gitTest(t, destination, "checkout", "-qb", branch, head)
	state := t.TempDir()
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	stager, err := OpenStager(destination, state)
	if err != nil {
		t.Fatal(err)
	}
	defer stager.Close()
	if _, err = stager.Apply(ctx, m, review, branch); err != nil {
		t.Fatal("Guarded stage failed", err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "new.txt"))
	if err != nil || string(contents) != "Selected working changes\n" {
		t.Fatal("Guarded transfer lost the working change", string(contents), err)
	}
}
