package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDevelopmentProfilesCannotChangeProductionDiscovery(t *testing.T) {
	t.Setenv("CAMP_UI_AUDIT", "")
	previous := developmentBuild
	t.Cleanup(func() { developmentBuild = previous })
	developmentBuild = ""
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAMP_DEV_PROFILE_ROOT", root)
	prod, err := LoadLocal()
	if err != nil || prod.UsersDir != "/Users" || prod.Home == filepath.Join(root, "home") {
		t.Fatal("production discovery changed", err)
	}
	developmentBuild = "1"
	t.Setenv("CODEX_HOME", "/not-a-fixture")
	t.Setenv("CLAUDE_CONFIG_DIR", "/not-a-fixture-either")
	dev, err := LoadLocal()
	if err != nil || dev.Home != filepath.Join(root, "home") || dev.UsersDir != filepath.Join(root, "users") || len(dev.ProfileRoots) != 0 {
		t.Fatal("development discovery escaped fixture root", err)
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocal(); err == nil {
		t.Fatal("shared fixture root accepted")
	}
}

func TestUIAuditCannotFallBackToProductionDiscovery(t *testing.T) {
	previous := developmentBuild
	t.Cleanup(func() { developmentBuild = previous })
	t.Setenv("CAMP_UI_AUDIT", "1")
	t.Setenv("CAMP_DEV_PROFILE_ROOT", t.TempDir())
	developmentBuild = ""
	if _, err := LoadLocal(); err == nil {
		t.Fatal("audit accepted a production helper")
	}
	developmentBuild = "1"
	t.Setenv("CAMP_DEV_PROFILE_ROOT", "")
	if _, err := LoadLocal(); err == nil {
		t.Fatal("audit without fixture root fell back to real profiles")
	}
}
