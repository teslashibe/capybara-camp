package config

import "testing"

func TestRunnerWorkspaceDirectoryBoundaries(t *testing.T) {
	base := Runner{Origin: "https://camp.example", GrantFile: "/run/grant", StateDir: "/state/private", WorkspaceDir: "/workspaces/private"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"relative", "/state/private", "/state/private/child", "/state", "/run", "/run/grant/child", "/workspaces/../state"} {
		value := base
		value.WorkspaceDir = dir
		if value.Validate() == nil {
			t.Fatalf("accepted unsafe workspace directory %q", dir)
		}
	}
}
