package localcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
)

type ExecutionContext struct {
	Fingerprint       string `json:"fingerprint"`
	CWD               string `json:"cwd"`
	Profile           string `json:"profile"`
	Provider          string `json:"provider"`
	PermissionSummary string `json:"permissionSummary"`
}

// Match the saved adapter's ASCII JSON representation so existing approvals
// keep the same fingerprint after migration, including Unicode project paths.
func asciiJSONString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r >= 0x7f {
				if r <= 0xffff {
					fmt.Fprintf(&b, `\u%04x`, r)
				} else {
					first, second := utf16.EncodeRune(r)
					fmt.Fprintf(&b, `\u%04x\u%04x`, first, second)
				}
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func ancestors(path string) []string {
	result := []string{path}
	for {
		parent := filepath.Dir(path)
		if parent == path {
			return result
		}
		result = append(result, parent)
		path = parent
	}
}

func (a *App) executionContext(ctx context.Context, s Session, settings PermissionSettings) (ExecutionContext, error) {
	result := ExecutionContext{}
	if s.Provider != "Codex" && s.Provider != "Claude" {
		return result, errors.New("Choose a supported implementation provider")
	}
	root, err := canonical(s.Root, a.Config.Home)
	if err != nil {
		return result, err
	}
	cwd, err := canonical(s.CWD, a.Config.Home)
	if err != nil || s.CWD == "" || s.Root == "" || !isDir(root) || !isDir(cwd) {
		return result, errors.New("Restore the selected project and profile before implementation")
	}
	selected, err := normalizePermissions(s.Provider, settings)
	if err != nil {
		return result, err
	}
	paths := map[string]bool{}
	if s.Provider == "Codex" {
		for _, path := range []string{filepath.Join(root, "config.toml"), filepath.Join(root, "requirements.toml"), "/etc/codex/requirements.toml"} {
			paths[path] = true
		}
		for _, parent := range ancestors(cwd) {
			paths[filepath.Join(parent, ".codex/config.toml")] = true
		}
	} else {
		paths[filepath.Join(root, "settings.json")] = true
		for _, name := range []string{"managed-settings.json", "managed-mcp.json"} {
			paths[filepath.Join("/Library/Application Support/ClaudeCode", name)] = true
		}
		for _, parent := range ancestors(cwd) {
			for _, name := range []string{"settings.json", "settings.local.json"} {
				paths[filepath.Join(parent, ".claude", name)] = true
			}
		}
	}
	digest := sha256.New()
	_, _ = io.WriteString(digest, "["+asciiJSONString(s.Provider)+", "+asciiJSONString(root)+", "+asciiJSONString(cwd)+"]")
	if len(selected) > 0 {
		keys := make([]string, 0, len(selected))
		for key := range selected {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var pairs []string
		for _, key := range keys {
			pairs = append(pairs, asciiJSONString(key)+":"+asciiJSONString(selected[key]))
		}
		_, _ = io.WriteString(digest, "{"+strings.Join(pairs, ",")+"}")
	}
	var ordered []string
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		_, _ = io.WriteString(digest, path)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			_, _ = io.WriteString(digest, "missing")
			continue
		}
		if err != nil {
			return result, errors.New("A CLI configuration file could not be read")
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return result, errors.New("A CLI configuration file is invalid or exceeds 1 MB")
		}
		file, err := os.Open(path)
		if err != nil {
			return result, errors.New("A CLI configuration file could not be read")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 1<<20+1))
		_ = file.Close()
		if readErr != nil || len(data) > 1<<20 {
			return result, errors.New("A CLI configuration file is invalid or exceeds 1 MB")
		}
		after, err := os.Stat(path)
		if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || info.ModTime() != after.ModTime() {
			return result, errors.New("CLI configuration changed while reading; review the permission settings again")
		}
		_, _ = digest.Write(data)
	}
	return ExecutionContext{Fingerprint: hex.EncodeToString(digest.Sum(nil)), CWD: cwd, Profile: root, Provider: s.Provider, PermissionSummary: "Installed CLI project rules"}, nil
}
