//go:build linux

package processguard

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "process-guard" {
		if err := Run(os.Args[2:]); err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(90)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestLinuxGuardRejectsControlFilesAndInheritedChildren(t *testing.T) {
	if _, err := abiVersion(); err != nil {
		if os.Getenv("CAMP_REQUIRE_PROCESS_GUARD") == "1" {
			t.Fatal("Test environment must enforce Landlock ABI 6 or later:", err)
		}
		t.Skip("Landlock ABI 6 unavailable; run the required Linux processguard check before release")
	}
	root := t.TempDir()
	home := filepath.Join(root, "provider")
	work := filepath.Join(root, "workspace")
	control := filepath.Join(root, "control")
	for _, dir := range []string{home, work, control} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(control, "grant"), []byte("synthetic-control-grant"), 0600); err != nil {
		t.Fatal(err)
	}
	readFile := filepath.Join(control, "one-checkout-token")
	if err := os.WriteFile(readFile, []byte("synthetic-checkout-only"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "source"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(control, "grant"), filepath.Join(work, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(work, filepath.Join(home, "workspace-link")); err != nil {
		t.Fatal(err)
	}
	secret, err := os.Open(filepath.Join(control, "grant"))
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Close()
	self, _ := os.Executable()
	for _, write := range []bool{false, true} {
		p := Policy{Home: home, Workspace: work, WriteWorkspace: write, ReadFiles: []string{readFile}}
		raw, _ := json.Marshal(p)
		script := `set -eu
cat "$WORK/source" >/dev/null
cat "$READ_FILE" >/dev/null
printf ok > "$PROVIDER/own-file"
if cat "$CONTROL/grant" 2>/dev/null; then exit 12; fi
if cat "$WORK/escape" 2>/dev/null; then exit 13; fi
if printf bad > "$CONTROL/journal" 2>/dev/null; then exit 14; fi
if /bin/sh -c 'cat "$CONTROL/grant"' 2>/dev/null; then exit 15; fi
if /bin/kill -0 "$PARENT" 2>/dev/null; then exit 16; fi
if cat "/proc/$PARENT/environ" 2>/dev/null; then exit 17; fi
if cat "/proc/$PARENT/mem" 2>/dev/null; then exit 19; fi
if cat "/proc/$PARENT/fd/$SECRET_FD" 2>/dev/null; then exit 20; fi
if cat "/proc/$PARENT/root$CONTROL/grant" 2>/dev/null; then exit 21; fi
if [ "$WRITE" = true ]; then printf changed > "$WORK/result"; else
 if printf denied > "$WORK/result" 2>/dev/null; then exit 18; fi
 if printf denied > "$PROVIDER/workspace-link/source" 2>/dev/null; then exit 22; fi
 if ln "$WORK/source" "$PROVIDER/hard-link" 2>/dev/null; then exit 23; fi
fi
printf guarded
`
		cmd := exec.Command(self, "process-guard", base64.RawURLEncoding.EncodeToString(raw), "/bin/sh", "-c", script)
		cmd.Env = append(os.Environ(), "WORK="+work, "PROVIDER="+home, "CONTROL="+control, "PARENT="+fmtPID(os.Getpid()), "WRITE="+fmtBool(write), "SECRET_FD="+fmtPID(int(secret.Fd())), "READ_FILE="+readFile)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "guarded") {
			t.Fatalf("write=%v: %s %v", write, out, err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(control, "grant")); string(data) != "synthetic-control-grant" {
		t.Fatal("control secret changed")
	}
}
func fmtPID(pid int) string     { return strconv.Itoa(pid) }
func fmtBool(value bool) string { return strconv.FormatBool(value) }
