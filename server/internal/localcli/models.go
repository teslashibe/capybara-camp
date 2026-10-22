package localcli

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

type Model struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Efforts       []string `json:"efforts"`
	Speeds        []string `json:"speeds"`
	DefaultEffort string   `json:"defaultEffort"`
	ShowInPicker  bool     `json:"showInPicker"`
}

// Preserve the current CLI catalogue, including version IDs used by history.
var claudeAliases = map[string]string{"opus": "claude-opus-5-5", "sonnet": "claude-sonnet-5-5", "haiku": "claude-haiku-5-5", "fable": "claude-fable-5-1"}
var claudeModern = regexp.MustCompile(`^claude-(?:opus-(?:5-5|5|4-8|4-7)|sonnet-(?:5-5|5)|haiku-5-5|fable-(?:5-1|5))(?:-|$)`)
var claudeFast = regexp.MustCompile(`^claude-opus-(?:5-5|5|4-8)(?:-|$)`)

func claudeCapabilities(id string) Model {
	resolved := claudeAliases[id]
	label := id
	if resolved == "" {
		resolved = id
	} else {
		label = strings.ToUpper(id[:1]) + id[1:]
	}
	m := Model{ID: id, Label: label, Efforts: []string{}, Speeds: []string{"standard"}, DefaultEffort: "high", ShowInPicker: true}
	if claudeModern.MatchString(resolved) {
		m.Efforts = []string{"low", "medium", "high", "xhigh", "max"}
	} else if resolved == "claude-opus-4-6" || resolved == "claude-sonnet-4-6" {
		m.Efforts = []string{"low", "medium", "high", "max"}
	}
	if claudeFast.MatchString(resolved) {
		m.Speeds = append(m.Speeds, "fast")
	}
	if strings.HasSuffix(resolved, "5-5") {
		m.DefaultEffort = "medium"
	}
	return m
}

func modelsFor(source Source) []Model {
	result := []Model{}
	if source.Provider == "Claude" {
		for _, id := range []string{"opus", "sonnet", "haiku", "fable", "claude-opus-5-5", "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-5-5", "claude-sonnet-5", "claude-sonnet-4-6", "claude-haiku-5-5", "claude-fable-5-1", "claude-fable-5"} {
			m := claudeCapabilities(id)
			_, m.ShowInPicker = claudeAliases[id]
			result = append(result, m)
		}
		return result
	}
	if source.Provider != "Codex" {
		return result
	}
	var cache struct {
		Models []map[string]any `json:"models"`
	}
	if readJSON(filepath.Join(source.Root, "models_cache.json"), &cache) != nil {
		return result
	}
	for _, entry := range cache.Models {
		id := str(entry["slug"])
		if id == "" || str(entry["visibility"]) != "list" {
			continue
		}
		m := Model{ID: id, Label: str(entry["display_name"]), Efforts: []string{}, Speeds: []string{"standard"}, DefaultEffort: str(entry["default_reasoning_level"]), ShowInPicker: true}
		if m.Label == "" {
			m.Label = id
		}
		for _, level := range array(entry["supported_reasoning_levels"]) {
			if effort := str(object(level)["effort"]); effort != "" {
				m.Efforts = append(m.Efforts, effort)
			}
		}
		advertised := map[string]bool{}
		for _, tier := range array(entry["additional_speed_tiers"]) {
			advertised[str(tier)] = true
		}
		for _, tier := range array(entry["service_tiers"]) {
			id := str(object(tier)["id"])
			if id == "priority" {
				id = "fast"
			}
			advertised[id] = true
		}
		for _, speed := range []string{"fast", "ultrafast"} {
			if advertised[speed] {
				m.Speeds = append(m.Speeds, speed)
			}
		}
		result = append(result, m)
	}
	return result
}

func capabilitiesFor(s Session, model string) Model {
	if s.Provider == "Claude" {
		return claudeCapabilities(model)
	}
	for _, m := range modelsFor(Source{Provider: s.Provider, Root: s.Root}) {
		if m.ID == model {
			return m
		}
	}
	return Model{ID: model, Label: model, Efforts: []string{}, Speeds: []string{"standard"}}
}

func quoted(value string) string { b, _ := json.Marshal(value); return string(b) }

func runtimeCommand(s Session, model, effort, speed string) ([]string, error) {
	if s.Provider != "Codex" && s.Provider != "Claude" {
		return nil, errors.New("Choose a supported provider")
	}
	if len(model) > 160 || strings.HasPrefix(model, "-") || strings.ContainsFunc(model, unicode.IsSpace) {
		return nil, errors.New("Choose a valid model ID")
	}
	selected := model
	if selected == "" {
		selected = s.Model
	}
	capabilities := capabilitiesFor(s, selected)
	if effort != "" && !slices.Contains(capabilities.Efforts, effort) {
		return nil, errors.New("This model does not advertise the selected effort level")
	}
	if speed != "" && !slices.Contains(capabilities.Speeds, speed) {
		return nil, errors.New("This model does not support the selected speed")
	}
	command := []string{"claude"}
	if s.Provider == "Codex" {
		command = []string{"codex", "exec"}
	}
	if model != "" {
		command = append(command, "--model", model)
	}
	if s.Provider == "Codex" {
		if effort != "" {
			command = append(command, "-c", "model_reasoning_effort="+quoted(effort))
		}
		if speed != "" {
			tier := speed
			if speed == "standard" {
				tier = "default"
			}
			command = append(command, "-c", "service_tier="+quoted(tier))
			if speed == "fast" || speed == "ultrafast" {
				command = append(command, "-c", "features."+speed+"_mode=true")
			}
		}
		return append(command, "--ignore-user-config", "-s", "read-only", "--json", "--skip-git-repo-check", "resume", s.SessionID, "-"), nil
	}
	if effort != "" {
		command = append(command, "--effort", effort)
	}
	if speed != "" {
		settings, _ := json.Marshal(map[string]bool{"fastMode": speed == "fast"})
		command = append(command, "--settings", string(settings))
	}
	return append(command, "--resume", s.SessionID, "-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--tools", "", "--strict-mcp-config", "--safe-mode"), nil
}
