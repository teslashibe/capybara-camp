package localcli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ActivityRequest struct {
	Sources    []Source `json:"sources"`
	SelectedID string   `json:"selectedID"`
}

type LiveState struct {
	ID       string  `json:"id"`
	SourceID string  `json:"sourceID"`
	State    string  `json:"state"`
	Model    string  `json:"model"`
	Children []Child `json:"children"`
	Owner    string  `json:"owner"`
	PID      int     `json:"pid"`
}

type ActivityResult struct {
	Activity   []LiveState        `json:"activity"`
	Sessions   []Session          `json:"sessions"`
	Models     map[string][]Model `json:"models"`
	Errors     map[string]string  `json:"errors"`
	ObservedAt float64            `json:"observedAt"`
}

func (a *App) liveClaude(ctx context.Context, source Source) ([]map[string]any, error) {
	registries, _ := filepath.Glob(filepath.Join(source.Root, "sessions", "*.json"))
	if len(registries) == 0 {
		return []map[string]any{}, nil
	}
	out, err := a.probe(ctx, Session{Provider: "Claude", Root: source.Root}, []string{"claude", "--safe-mode", "agents", "--json"}, 8*time.Second)
	if err != nil {
		return nil, err
	}
	var rows []any
	if err = json.Unmarshal(out, &rows); err != nil || rows == nil {
		return nil, errors.New("Claude returned invalid live status")
	}
	result := []map[string]any{}
	for _, value := range rows {
		row := object(value)
		if str(row["sessionId"]) != "" {
			result = append(result, row)
		}
	}
	return result, nil
}

func findTranscript(source Source, id string) string {
	if id == "" || len(id) > 256 || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00*?[]") {
		return ""
	}
	// Enumerate project folders rather than interpreting provider IDs as glob
	// patterns or paths outside this selected profile.
	entries, err := os.ReadDir(filepath.Join(source.Root, "projects"))
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(source.Root, "projects", entry.Name(), id+".jsonl")
		if isFile(path) {
			return path
		}
	}
	return ""
}

func (a *App) Activity(ctx context.Context, request ActivityRequest) (ActivityResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := ActivityResult{Activity: []LiveState{}, Sessions: []Session{}, Models: map[string][]Model{}, Errors: map[string]string{}}
	if len(request.Sources) > 256 {
		return result, errors.New("Too many source profiles in one status request")
	}
	metadata := map[string]map[string]map[string]any{}
	for _, source := range request.Sources {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Models[source.ID] = modelsFor(source)
		if source.Provider != "Claude" {
			continue
		}
		rows, err := a.liveClaude(ctx, source)
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.Errors[source.ID] = err.Error()
			continue
		}
		home := filepath.Dir(source.Root)
		if metadata[home] == nil {
			metadata[home] = claudeMetadata(home)
		}
		for _, row := range rows {
			id := str(row["sessionId"])
			path := findTranscript(source, id)
			if path == "" {
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			state := map[string]string{"busy": "running", "idle": "idle", "waiting": "waiting", "waiting_for_input": "waiting"}[str(row["status"])]
			if state == "" {
				state = "unknown"
			}
			meta := metadata[home][id]
			title := str(meta["title"])
			if title == "" {
				title = str(row["name"])
			}
			s := makeSession(source, id, title, str(row["cwd"]), path, float64(info.ModTime().UnixNano())/1e9)
			s.Model = str(meta["model"])
			if s.Model == "" {
				s.Model = lastModel(ctx, path)
			}
			children := []Child{}
			if state == "running" || s.ID == request.SelectedID {
				children, err = childrenFor(ctx, s, state == "running")
				if err != nil {
					if ctx.Err() != nil {
						return result, ctx.Err()
					}
					// Missing child files do not invalidate the authoritative
					// parent's live state returned by the CLI.
				}
			}
			result.Activity = append(result.Activity, LiveState{ID: s.ID, SourceID: source.ID, State: state, Model: s.Model, Children: children, Owner: "Claude Code", PID: int(number(row["pid"]))})
			result.Sessions = append(result.Sessions, s)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.ObservedAt = float64(time.Now().UnixNano()) / 1e9
	return result, nil
}

func (a *App) assertClaudeAvailable(ctx context.Context, s Session) error {
	if s.IsSubagent {
		return errors.New("Subagents are read-only in Camp. Continue their parent session")
	}
	rows, err := a.liveClaude(ctx, Source{Root: s.Root, Provider: "Claude"})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if str(row["sessionId"]) == s.SessionID {
			return errors.New("This session is open in Claude Code. Close it there before sending from Camp")
		}
	}
	return nil
}
