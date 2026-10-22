package localcli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
)

var chatDisabledFeatures = []string{"hooks", "shell_tool", "unified_exec", "multi_agent", "multi_agent_v2", "view_image", "image_generation", "apps", "plugins", "remote_plugin", "skill_search", "tool_suggest", "sleep_tool", "goals", "request_permissions_tool"}

func codexChatCommand(ctx context.Context, session Session, grant *contextGrant) ([]string, error) {
	parents := ancestors(session.CWD)
	if len(parents) > 128 {
		return nil, errors.New("The project path has too many configuration levels")
	}
	slices.Reverse(parents)
	paths := []string{filepath.Join(session.Root, "config.toml")}
	for _, parent := range parents {
		paths = append(paths, filepath.Join(parent, ".codex/config.toml"))
	}
	servers := map[string]any{}
	total := 0
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("Could not read the Codex project configuration")
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			file.Close()
			return nil, errors.New("Codex configuration is invalid or exceeds 1 MB")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 1<<20+1))
		file.Close()
		total += len(data)
		if readErr != nil || len(data) > 1<<20 || total > 4<<20 {
			return nil, errors.New("Codex configuration exceeds the read limit")
		}
		var config map[string]any
		if toml.Unmarshal(data, &config) != nil {
			return nil, errors.New("Could not parse the Codex project configuration")
		}
		if raw, exists := config["mcp_servers"]; exists {
			entries, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("Codex MCP configuration must contain named servers")
			}
			for key, value := range entries {
				if _, ok := value.(map[string]any); !ok {
					return nil, errors.New("Codex MCP configuration contains an invalid server")
				}
				servers[key] = value
			}
		}
	}
	command := []string{"codex", "app-server", "--stdio"}
	for _, feature := range chatDisabledFeatures {
		command = append(command, "-c", "features."+feature+"=false")
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	disabled := make([]string, 0, len(names))
	for _, name := range names {
		fields := "enabled=false"
		if _, hasURL := object(servers[name])["url"]; hasURL {
			// Replace desktop-only URL placeholders before the CLI parses them.
			fields += `,url="http://127.0.0.1:9"`
		}
		disabled = append(disabled, quoted(name)+"={"+fields+"}")
	}
	command = append(command, "-c", "mcp_servers={"+strings.Join(disabled, ",")+"}")
	if grant != nil {
		command = append(command, "-c", "mcp_servers.camp_context={command="+quoted(str(grant.server["command"]))+",args="+jsonValue(grant.server["args"])+",enabled=true,required=true}")
	}
	return command, nil
}

func codexChatConfig(config map[string]any, grant *contextGrant) map[string]any {
	servers := map[string]any{}
	for name := range object(config["mcp_servers"]) {
		servers[name] = map[string]any{"enabled": false}
	}
	result := map[string]any{"mcp_servers": servers, "web_search": "disabled"}
	for _, feature := range chatDisabledFeatures {
		result["features."+feature] = false
	}
	if grant != nil {
		value, _ := cloneObject(grant.server)
		value["enabled"], value["required"] = true, true
		servers["camp_context"] = value
	}
	return result
}

func removeCommandFlag(command []string, flag string, value bool) ([]string, error) {
	index := slices.Index(command, flag)
	if index < 0 || value && index == len(command)-1 {
		return nil, errors.New("The provider command is missing a required setting")
	}
	end := index + 1
	if value {
		end++
	}
	return append(command[:index:index], command[end:]...), nil
}

func executionCommand(session Session, validated []string, fresh bool, settings PermissionSettings) ([]string, error) {
	selection, err := normalizePermissions(session.Provider, settings)
	if err != nil {
		return nil, err
	}
	if session.Provider == "Codex" {
		return []string{"codex", "app-server", "--stdio"}, nil
	}
	command := slices.Clone(validated)
	for _, flag := range []string{"--tools", "--safe-mode", "--strict-mcp-config"} {
		command, err = removeCommandFlag(command, flag, flag == "--tools")
		if err != nil {
			return nil, err
		}
	}
	if fresh {
		if _, err := uuid.Parse(session.SessionID); err != nil {
			return nil, errors.New("Claude needs a valid new session identity")
		}
		command, err = removeCommandFlag(command, "--resume", true)
		if err != nil {
			return nil, err
		}
		command = append(command, "--session-id", session.SessionID)
	}
	if mode := selection["claudeMode"]; mode != "" {
		command = append(command, "--permission-mode", mode)
	}
	return append(command, "--permission-prompt-tool", "stdio", "--input-format", "stream-json", "--replay-user-messages"), nil
}
