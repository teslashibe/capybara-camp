package transfer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// filterRepo builds a provider-written workspace whose repository-local
// configuration names a clean filter. The filter leaves a marker inside .git
// and tries to copy a control file that only the runner may read.
func filterRepo(t *testing.T) (work, control string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work, control = filepath.Join(root, "workspace"), filepath.Join(root, "control")
	for _, dir := range []string{work, control} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(control, "grant"), []byte("synthetic-control-grant"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "init", "-q")
	gitTest(t, work, "config", "user.name", "Transfer fixture")
	gitTest(t, work, "config", "user.email", "fixture@example.com")
	write(t, work, "source.txt", "source\n", 0600)
	write(t, work, ".gitattributes", "* filter=leak\n", 0600)
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-qm", "Initial")
	gitTest(t, work, "config", "filter.leak.clean", "touch "+filepath.Join(work, ".git", "ran")+"; cp "+filepath.Join(control, "grant")+" "+filepath.Join(control, "copied")+"; cat")
	return work, control
}

// statDirty keeps the content but moves the timestamp, so status must hash the
// file through its clean filter.
func statDirty(t *testing.T, work string) {
	t.Helper()
	later := time.Now().Add(time.Duration(2+time.Now().UnixNano()%7) * time.Second)
	if err := os.Chtimes(filepath.Join(work, "source.txt"), later, later); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedGitRunsThroughProcessGuard(t *testing.T) {
	work, _ := filterRepo(t)
	state := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command, cleanup, err := gitCommand(Isolated(t.Context(), state), work, []string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Path != self || len(command.Args) < 4 || command.Args[1] != "process-guard" || command.Args[3] != "/usr/bin/git" || command.Dir != work {
		t.Fatal("Isolated transfer Git bypassed the process guard", command.Path, command.Args)
	}
	homes, _ := filepath.Glob(filepath.Join(state, "transfer-git-home-*"))
	if len(homes) != 1 {
		t.Fatal("Guarded Git needs one private home", homes)
	}
	cleanup()
	if homes, _ = filepath.Glob(filepath.Join(state, "transfer-git-home-*")); len(homes) != 0 {
		t.Fatal("Guarded Git home was left behind", homes)
	}
	command, cleanup, err = gitCommand(trusted(Isolated(t.Context(), state)), work, []string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if command.Path != "/usr/bin/git" {
		t.Fatal("Package-owned repositories should not need the guard", command.Path)
	}
}
