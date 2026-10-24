package localcli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProviderPermissionValidationAndManagedRestrictions(t *testing.T) {
	for _, invalid := range []PermissionSettings{{"sandbox": "workspace-write", "profile": ":workspace"}, {"approval": "untrusted"}, {"claudeMode": "bypassPermissions"}, {"reviewer": "automatic"}, {"unexpected": ""}, {"profile": strings.Repeat("x", 161)}} {
		if _, err := normalizePermissions("Codex", invalid); err == nil {
			t.Fatal("Invalid permission selection accepted", invalid)
		}
	}
	for _, invalid := range []PermissionSettings{{"sandbox": "read-only"}, {"claudeMode": "all"}} {
		if _, err := normalizePermissions("Claude", invalid); err == nil {
			t.Fatal(invalid)
		}
	}
	if value, err := normalizePermissions("Codex", PermissionSettings{"sandbox": "", "approval": ""}); err != nil || len(value) != 0 {
		t.Fatal(value, err)
	}
	for _, mode := range []string{"manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"} {
		if _, err := normalizePermissions("Claude", PermissionSettings{"claudeMode": mode}); err != nil {
			t.Fatal(mode, err)
		}
	}
	tests := []struct {
		settings     PermissionSettings
		requirements map[string]any
	}{
		{PermissionSettings{"sandbox": "read-only"}, map[string]any{"allowedSandboxModes": []any{}}},
		{PermissionSettings{"approval": "never"}, map[string]any{"allowedApprovalPolicies": []any{"on-request"}}},
		{PermissionSettings{"reviewer": "auto_review"}, map[string]any{"allowedApprovalsReviewers": []any{}}},
		{PermissionSettings{"profile": ":workspace"}, map[string]any{"allowedPermissionProfiles": map[string]any{":read-only": true}}},
		{PermissionSettings{"sandbox": "workspace-write"}, map[string]any{"allowedPermissionProfiles": map[string]any{":workspace": true}}},
	}
	for _, test := range tests {
		if _, err := checkCodexPermissions(test.settings, nil, test.requirements); err == nil {
			t.Fatal("Managed policy ignored", test)
		}
	}
	if _, err := checkCodexPermissions(PermissionSettings{"profile": "missing"}, nil, nil); err == nil {
		t.Fatal("Missing profile accepted")
	}
	config := map[string]any{"permissions": map[string]any{"company": map[string]any{}}}
	if _, err := checkCodexPermissions(PermissionSettings{"profile": "company"}, config, map[string]any{"allowedPermissionProfiles": map[string]any{"company": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := checkCodexPermissions(PermissionSettings{"reviewer": "auto_review"}, map[string]any{"approval_policy": "never"}, nil); err == nil {
		t.Fatal("Contradictory inherited policy accepted")
	}
}

func TestPermissionCatalogueReadsCLIAndFiltersManagedChoices(t *testing.T) {
	a := providerFixture(t, "rpc")
	a.Config.Environment = append(a.Config.Environment,
		`CAMP_LOCAL_TEST_CONFIG={"permissions":{"company":{},"other":{}},"default_permissions":"company","approval_policy":"on-request","approvals_reviewer":"auto_review","private":"fixture-only-do-not-return"}`,
		`CAMP_LOCAL_TEST_REQUIREMENTS={"allowedPermissionProfiles":{"company":true},"allowedApprovalsReviewers":["auto_review"],"allowedApprovalPolicies":["on-request"]}`)
	s := Session{Provider: "Codex", Root: a.Config.Home, CWD: a.Config.Home}
	catalog, err := a.permissionCatalog(context.Background(), s)
	if err != nil || !reflect.DeepEqual(catalog.Profiles, []string{"company"}) || len(catalog.Sandboxes) != 0 || !reflect.DeepEqual(catalog.Reviewers, []string{"auto_review"}) || !reflect.DeepEqual(catalog.Approvals, []string{"on-request"}) {
		t.Fatal(catalog, err)
	}
	if catalog.Inherited != "company · on-request · auto_review" {
		t.Fatal(catalog)
	}
	encoded, _ := json.Marshal(catalog)
	if strings.Contains(string(encoded), "fixture-only-do-not-return") {
		t.Fatal("Private configuration returned")
	}
}

func TestClaudeCatalogueUsesAdvertisedModes(t *testing.T) {
	a := providerFixture(t, "reply")
	a.Config.Environment = append(a.Config.Environment, `CAMP_LOCAL_TEST_REPLY=Usage: claude --permission-mode <mode> (choices: "manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions")`)
	catalog, err := a.permissionCatalog(context.Background(), Session{Provider: "Claude", Root: a.Config.Home, CWD: a.Config.Home})
	if err != nil || len(catalog.Modes) != 6 || catalog.Modes[5] != "bypassPermissions" || len(catalog.Profiles) != 0 {
		t.Fatal(catalog, err)
	}
}

func TestExecutionContextPreservesSelectionAndTracksConfig(t *testing.T) {
	a := fixture(t)
	root := filepath.Join(a.Config.Home, ".codex")
	cwd := filepath.Join(a.Config.Home, "project")
	write(t, filepath.Join(root, "config.toml"), "approval_policy='on-request'\n")
	write(t, filepath.Join(cwd, ".codex/config.toml"), "[features]\nhooks=false\n")
	s := Session{Provider: "Codex", Root: root, CWD: cwd}
	first, err := a.executionContext(context.Background(), s, nil)
	if err != nil || len(first.Fingerprint) != 64 {
		t.Fatal(first, err)
	}
	second, err := a.executionContext(context.Background(), s, PermissionSettings{"sandbox": "", "approval": ""})
	if err != nil || first.Fingerprint != second.Fingerprint {
		t.Fatal(second, err)
	}
	selected, err := a.executionContext(context.Background(), s, PermissionSettings{"sandbox": "read-only", "approval": "never"})
	if err != nil || selected.Fingerprint == first.Fingerprint {
		t.Fatal("Permission changes did not invalidate approval", selected, err)
	}
	write(t, filepath.Join(cwd, ".codex/config.toml"), "[features]\nhooks=true\n")
	changed, err := a.executionContext(context.Background(), s, nil)
	if err != nil || changed.Fingerprint == first.Fingerprint {
		t.Fatal("Project changes did not invalidate approval", changed, err)
	}
	write(t, filepath.Join(root, "config.toml"), strings.Repeat("x", (1<<20)+1))
	if _, err := a.executionContext(context.Background(), s, nil); err == nil {
		t.Fatal("Oversized config accepted")
	}
}

func TestASCIIJSONCompatibility(t *testing.T) {
	for input, expected := range map[string]string{"plain": `"plain"`, "<tag>&": `"<tag>&"`, "é😀": `"\u00e9\ud83d\ude00"`, "\x00\x7f\b\f\t\n\r\\\"": `"\u0000\u007f\b\f\t\n\r\\\""`} {
		if got := asciiJSONString(input); got != expected {
			t.Fatalf("JSON representation differs: %q != %q", got, expected)
		}
	}
}
