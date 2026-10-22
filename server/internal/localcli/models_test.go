package localcli

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func tuningFixture(t *testing.T) Session {
	t.Helper()
	a := fixture(t)
	root := filepath.Join(a.Config.Home, ".codex")
	write(t, filepath.Join(root, "models_cache.json"), `{"models":[{"slug":"sol","visibility":"list","default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"},{"effort":"ultra"}],"service_tiers":[{"id":"priority"},{"id":"ultrafast"}]},{"slug":"luna","visibility":"list","supported_reasoning_levels":[{"effort":"low"},{"effort":"max"}],"additional_speed_tiers":["fast"]},{"slug":"private","visibility":"hide"}]}`)
	return Session{Provider: "Codex", Root: root, Model: "sol", SessionID: "test"}
}

func TestModelCatalogueAndCLISettings(t *testing.T) {
	s := tuningFixture(t)
	models := modelsFor(Source{Provider: s.Provider, Root: s.Root})
	if len(models) != 2 || models[0].DefaultEffort != "low" || !reflect.DeepEqual(models[0].Speeds, []string{"standard", "fast", "ultrafast"}) || !reflect.DeepEqual(models[1].Speeds, []string{"standard", "fast"}) {
		t.Fatal(models)
	}
	for _, test := range []struct{ model, effort, speed, expected string }{{"sol", "high", "fast", `features.fast_mode=true`}, {"sol", "ultra", "ultrafast", `service_tier="ultrafast"`}, {"sol", "", "standard", `service_tier="default"`}} {
		command, err := runtimeCommand(s, test.model, test.effort, test.speed)
		if err != nil || !slices.Contains(command, test.expected) || !slices.Contains(command, "--ignore-user-config") || !slices.Contains(command, s.SessionID) {
			t.Fatal(command, err)
		}
	}
	for _, invalid := range []struct{ model, effort, speed string }{{"luna", "ultra", "standard"}, {"luna", "low", "ultrafast"}, {"--bad", "", ""}, {"bad model", "", ""}, {strings.Repeat("a", 161), "", ""}} {
		if _, err := runtimeCommand(s, invalid.model, invalid.effort, invalid.speed); err == nil {
			t.Fatal("Unsupported selection accepted", invalid)
		}
	}
	if m := capabilitiesFor(s, "historical"); m.ShowInPicker || len(m.Efforts) != 0 || !reflect.DeepEqual(m.Speeds, []string{"standard"}) {
		t.Fatal(m)
	}
}

func TestClaudeVersionsEffortAndSpeedStayConsistent(t *testing.T) {
	s := tuningFixture(t)
	s.Provider = "Claude"
	models := modelsFor(Source{Provider: "Claude"})
	if len(models) != 15 {
		t.Fatal(models)
	}
	for i, m := range models {
		if m.ShowInPicker != (i < 4) || slices.Contains(m.Speeds, "ultrafast") {
			t.Fatal(m)
		}
		if _, err := runtimeCommand(s, m.ID, "", "ultrafast"); err == nil {
			t.Fatal("Claude accepted Ultra Fast", m)
		}
	}
	if !reflect.DeepEqual(claudeCapabilities("claude-opus-4-6").Efforts, []string{"low", "medium", "high", "max"}) || len(claudeCapabilities("unknown").Efforts) != 0 {
		t.Fatal("Historical effort catalogue changed")
	}
	for _, model := range []string{"sonnet", "haiku", "fable", "claude-opus-4-7"} {
		if _, err := runtimeCommand(s, model, "", "fast"); err == nil {
			t.Fatal("Unsupported Fast mode accepted", model)
		}
	}
	for _, speed := range []string{"standard", "fast"} {
		command, err := runtimeCommand(s, "opus", "max", speed)
		if err != nil || !slices.Contains(command, "--safe-mode") || !slices.Contains(command, "--strict-mcp-config") {
			t.Fatal(command, err)
		}
		var settings map[string]bool
		index := slices.Index(command, "--settings")
		if index < 0 || json.Unmarshal([]byte(command[index+1]), &settings) != nil || settings["fastMode"] != (speed == "fast") {
			t.Fatal(command)
		}
	}
}

func TestRuntimeEnvironmentSeparatesProfilesAndAmbientCredentials(t *testing.T) {
	a := fixture(t)
	a.Config.Environment = []string{"PATH=/test/bin", "UNRELATED=kept", "OPENAI_API_KEY=fixture", "CODEX_API_KEY=fixture", "ANTHROPIC_API_KEY=fixture", "ANTHROPIC_AUTH_TOKEN=fixture", "CLAUDE_CODE_OAUTH_TOKEN=fixture", "CLAUDECODE=1", "CODEX_THREAD_ID=old", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE=old", "CLAUDE_CODE_EFFORT_LEVEL=max", "ANTHROPIC_MODEL=wrong", "ANTHROPIC_DEFAULT_MODEL=wrong", "CODEX_HOME=/old/codex", "CLAUDE_CONFIG_DIR=/old/claude"}
	for _, provider := range []string{"Codex", "Claude"} {
		env := map[string]string{}
		for _, entry := range a.runtimeEnvironment(Session{Provider: provider, Root: "/new/profile"}) {
			key, value, _ := strings.Cut(entry, "=")
			env[key] = value
		}
		if env["UNRELATED"] != "kept" || !strings.HasSuffix(env["PATH"], "/test/bin") {
			t.Fatal("Non-provider environment lost")
		}
		for key := range env {
			if key != "PATH" && key != "UNRELATED" && key != map[string]string{"Codex": "CODEX_HOME", "Claude": "CLAUDE_CONFIG_DIR"}[provider] {
				t.Fatal("Ambient provider setting survived", key)
			}
		}
		if env[map[string]string{"Codex": "CODEX_HOME", "Claude": "CLAUDE_CONFIG_DIR"}[provider]] != "/new/profile" {
			t.Fatal("Selected profile was not used")
		}
	}
	for _, entry := range a.runtimeEnvironment(Session{Provider: "Claude", Root: filepath.Join(a.Config.Home, ".claude")}) {
		if strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") || strings.HasPrefix(entry, "CODEX_HOME=") {
			t.Fatal("Default Claude keychain namespace changed")
		}
	}
}
