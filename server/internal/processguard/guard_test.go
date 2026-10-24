package processguard

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWrappedCommandContainsOnlyExplicitPolicy(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	executable, args, err := Wrap("/usr/bin/tool", []string{"tool", "--version"}, Policy{Home: home, Workspace: work})
	self, _ := os.Executable()
	if err != nil || executable != self || len(args) != 4 || args[0] != "process-guard" || args[2] != "/usr/bin/tool" || args[3] != "--version" {
		t.Fatal(executable, args, err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(args[1])
	if err != nil {
		t.Fatal(err)
	}
	var p Policy
	if json.Unmarshal(raw, &p) != nil || p.Home != home || p.Workspace != work || p.WriteWorkspace {
		t.Fatal("incorrect policy")
	}
	for _, invalid := range []Policy{{Home: "/", Workspace: work}, {Home: home, Workspace: "relative"}, {Home: home, Workspace: work, ReadFiles: []string{filepath.Join(home, "missing")}}} {
		if invalid.Validate() == nil {
			t.Fatal("unsafe policy accepted")
		}
	}
}
