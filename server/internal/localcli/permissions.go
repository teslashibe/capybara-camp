package localcli

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

type PermissionSettings map[string]string

// NormalizePermissions shares the supported CLI permission vocabulary with
// the control plane. It validates a choice; it does not authorize execution.
func NormalizePermissions(provider string, input PermissionSettings) (PermissionSettings, error) {
	return normalizePermissions(provider, input)
}

var permissionProfile = regexp.MustCompile(`^[:a-zA-Z0-9_.-]+$`)

func normalizePermissions(provider string, input PermissionSettings) (PermissionSettings, error) {
	value := PermissionSettings{}
	for key, v := range input {
		if !slices.Contains([]string{"profile", "sandbox", "approval", "reviewer", "claudeMode"}, key) || len(v) > 160 {
			return nil, errors.New("Invalid permission settings")
		}
		if v != "" {
			value[key] = v
		}
	}
	switch provider {
	case "Codex":
		if value["claudeMode"] != "" {
			return nil, errors.New("Claude permission modes cannot be used with Codex")
		}
		if value["sandbox"] != "" && value["profile"] != "" {
			return nil, errors.New("Choose a Codex permission profile or sandbox, not both")
		}
		if !slices.Contains([]string{"", "read-only", "workspace-write", "danger-full-access"}, value["sandbox"]) ||
			!slices.Contains([]string{"", "on-request", "never"}, value["approval"]) ||
			!slices.Contains([]string{"", "user", "auto_review"}, value["reviewer"]) ||
			value["profile"] != "" && !permissionProfile.MatchString(value["profile"]) {
			return nil, errors.New("Unsupported Codex permission settings")
		}
	case "Claude":
		for key := range value {
			if key != "claudeMode" {
				return nil, errors.New("Unsupported Claude permission mode")
			}
		}
		if !slices.Contains([]string{"", "manual", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"}, value["claudeMode"]) {
			return nil, errors.New("Unsupported Claude permission mode")
		}
	default:
		return nil, errors.New("Choose a supported provider")
	}
	return value, nil
}

func codexOverrides(settings PermissionSettings) (map[string]any, error) {
	value, err := normalizePermissions("Codex", settings)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	for key, field := range map[string]string{"profile": "permissions", "sandbox": "sandbox", "approval": "approvalPolicy", "reviewer": "approvalsReviewer"} {
		if value[key] != "" {
			result[field] = value[key]
		}
	}
	return result, nil
}

func allowedString(list any, value string) bool {
	for _, item := range array(list) {
		if str(item) == value {
			return true
		}
	}
	return false
}

func checkCodexPermissions(settings PermissionSettings, config, requirements map[string]any) (PermissionSettings, error) {
	value, err := normalizePermissions("Codex", settings)
	if err != nil {
		return nil, err
	}
	for key, rule := range map[string]string{"approval": "allowedApprovalPolicies", "reviewer": "allowedApprovalsReviewers", "sandbox": "allowedSandboxModes"} {
		if value[key] != "" && requirements[rule] != nil && !allowedString(requirements[rule], value[key]) {
			return nil, errors.New("This Codex permission choice is blocked by managed requirements")
		}
	}
	allowed := requirements["allowedPermissionProfiles"]
	if value["sandbox"] != "" && allowed != nil {
		return nil, errors.New("Managed Codex requires a named permission profile")
	}
	if name := value["profile"]; name != "" {
		_, installed := object(config["permissions"])[name]
		if !slices.Contains([]string{":read-only", ":workspace", ":danger-full-access"}, name) && !installed {
			return nil, errors.New("This Codex permission profile is no longer installed")
		}
		if allowed != nil && object(allowed)[name] != true {
			return nil, errors.New("This Codex permission profile is blocked by managed requirements")
		}
	}
	approval := value["approval"]
	if approval == "" {
		approval = str(config["approval_policy"])
	}
	if value["reviewer"] == "auto_review" && approval == "never" {
		return nil, errors.New("Automatic review requires an interactive approval policy")
	}
	return value, nil
}

func sandboxMode(response map[string]any) string {
	return map[string]string{"readOnly": "read-only", "workspaceWrite": "workspace-write", "dangerFullAccess": "danger-full-access"}[str(object(response["sandbox"])["type"])]
}

func codexPermissionSummary(response map[string]any) string {
	access := str(object(response["activePermissionProfile"])["id"])
	if access == "" {
		access = sandboxMode(response)
	}
	if access == "" {
		access = str(object(response["sandbox"])["type"])
	}
	if access == "" {
		access = "Provider default access"
	}
	policy := str(response["approvalPolicy"])
	if policy == "" {
		policy = "Provider approval rules"
	}
	reviewer := str(response["approvalsReviewer"])
	if reviewer == "" {
		reviewer = "Provider default reviewer"
	}
	return strings.Join([]string{access, policy, reviewer}, " · ")
}

type PermissionCatalog struct {
	Provider  string   `json:"provider"`
	Modes     []string `json:"modes"`
	Profiles  []string `json:"profiles"`
	Sandboxes []string `json:"sandboxes"`
	Approvals []string `json:"approvals"`
	Reviewers []string `json:"reviewers"`
	Inherited string   `json:"inherited"`
	Note      string   `json:"note"`
}

var claudePermissionChoices = regexp.MustCompile(`(?s)--permission-mode\b.*?\(choices:\s*([^)]*)\)`)
var quotedChoice = regexp.MustCompile(`"([a-zA-Z]+)"`)

func (a *App) permissionCatalog(ctx context.Context, s Session) (PermissionCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := PermissionCatalog{Provider: s.Provider, Modes: []string{}, Profiles: []string{}, Sandboxes: []string{}, Approvals: []string{}, Reviewers: []string{}}
	if !isDir(s.CWD) || !isDir(s.Root) || (s.Provider != "Codex" && s.Provider != "Claude") {
		return result, errors.New("Restore the selected project and profile before reading permissions")
	}
	if s.Provider == "Claude" {
		out, err := a.probeAt(ctx, s, []string{"claude", "--help"}, 10*time.Second, s.CWD)
		if err != nil {
			return result, err
		}
		match := claudePermissionChoices.FindStringSubmatch(string(out))
		if len(match) > 1 {
			for _, choice := range quotedChoice.FindAllStringSubmatch(match[1], -1) {
				result.Modes = append(result.Modes, choice[1])
			}
		}
		if len(result.Modes) == 0 {
			return result, errors.New("The installed Claude CLI does not advertise permission modes")
		}
		result.Inherited = "Claude profile and project rules"
		result.Note = "Auto mode and bypass availability are checked by Claude for this account and model"
		return result, nil
	}
	c, err := a.connect(ctx, s, []string{"codex", "app-server", "--stdio"}, false, nil)
	if err != nil {
		return result, err
	}
	defer c.Close()
	if _, err = c.RPC("initialize", map[string]any{"clientInfo": map[string]any{"name": "capybara_permissions", "version": "0.0.1"}, "capabilities": map[string]any{"experimentalApi": true}}, 0); err != nil {
		return result, err
	}
	if err = c.Write(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return result, err
	}
	response, err := c.RPC("config/read", map[string]any{"includeLayers": false, "cwd": s.CWD}, 0)
	if err != nil {
		return result, err
	}
	config := object(response["config"])
	response, err = c.RPC("configRequirements/read", map[string]any{}, 0)
	if err != nil {
		return result, err
	}
	requirements := object(response["requirements"])
	profiles := []string{":read-only", ":workspace", ":danger-full-access"}
	var names []string
	for name := range object(config["permissions"]) {
		names = append(names, name)
	}
	sort.Strings(names)
	profiles = append(profiles, names...)
	for _, name := range profiles {
		if !slices.Contains(result.Profiles, name) && (requirements["allowedPermissionProfiles"] == nil || object(requirements["allowedPermissionProfiles"])[name] == true) {
			result.Profiles = append(result.Profiles, name)
		}
	}
	if requirements["allowedPermissionProfiles"] == nil {
		result.Sandboxes = []string{"read-only", "workspace-write", "danger-full-access"}
		if allowed := requirements["allowedSandboxModes"]; allowed != nil {
			result.Sandboxes = []string{}
			for _, item := range array(allowed) {
				if value := str(item); value != "" {
					result.Sandboxes = append(result.Sandboxes, value)
				}
			}
		}
	}
	for _, policy := range []string{"on-request", "never"} {
		if requirements["allowedApprovalPolicies"] == nil || allowedString(requirements["allowedApprovalPolicies"], policy) {
			result.Approvals = append(result.Approvals, policy)
		}
	}
	for _, reviewer := range []string{"user", "auto_review"} {
		if requirements["allowedApprovalsReviewers"] == nil || allowedString(requirements["allowedApprovalsReviewers"], reviewer) {
			result.Reviewers = append(result.Reviewers, reviewer)
		}
	}
	access := config["default_permissions"]
	if access == nil || access == "" {
		access = config["sandbox_mode"]
	}
	if access == nil || access == "" {
		access = "Provider default access"
	}
	policy := config["approval_policy"]
	if policy == nil || policy == "" {
		policy = "Provider approval rules"
	}
	reviewer := config["approvals_reviewer"]
	if reviewer == nil || reviewer == "" {
		reviewer = "user"
	}
	inherited := []string{}
	for _, value := range []any{access, policy, reviewer} {
		if text, ok := value.(string); ok {
			inherited = append(inherited, text)
		} else {
			inherited = append(inherited, "Custom rules")
		}
	}
	result.Inherited = strings.Join(inherited, " · ")
	result.Note = "Managed requirements remain authoritative at launch"
	return result, nil
}
