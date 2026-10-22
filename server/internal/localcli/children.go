package localcli

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Child struct {
	ID        string  `json:"id"`
	SessionID string  `json:"sessionID"`
	Name      string  `json:"name"`
	Phase     string  `json:"phase"`
	Workflow  string  `json:"workflow"`
	Status    string  `json:"status"`
	Model     string  `json:"model"`
	Path      string  `json:"path"`
	Updated   float64 `json:"updated"`
}

func tailLines(ctx context.Context, path string, size int64, visit func(int, map[string]any) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	var reader io.Reader = f
	if info.Size() > size {
		if _, err = f.Seek(-size, io.SeekEnd); err != nil {
			return err
		}
		r := bufio.NewReader(f)
		if _, err = r.ReadBytes('\n'); err != nil && err != io.EOF {
			return err
		}
		reader = r
	}
	return scanLines(ctx, reader, false, 0, visit)
}

func lastModel(ctx context.Context, path string) string {
	model := ""
	_ = tailLines(ctx, path, 256<<10, func(_ int, d map[string]any) error {
		if value := str(object(d["message"])["model"]); value != "" && value != "<synthetic>" {
			model = value
		}
		return nil
	})
	return model
}

func childrenFor(ctx context.Context, parent Session, busy bool) ([]Child, error) {
	result := []Child{}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if parent.Provider == "Codex" {
		return codexChildren(ctx, parent)
	}
	if parent.Provider != "Claude" || parent.Path == "" {
		return result, nil
	}
	folder := strings.TrimSuffix(parent.Path, filepath.Ext(parent.Path))
	agents := filepath.Join(folder, "subagents")
	journals, _ := filepath.Glob(filepath.Join(agents, "workflows", "*", "journal.jsonl"))
	latest := ""
	var updated int64
	for _, path := range journals {
		if info, err := os.Stat(path); err == nil && info.ModTime().UnixNano() > updated {
			updated = info.ModTime().UnixNano()
			latest = path
		}
	}
	state := map[string]Child{}
	for _, path := range journals {
		workflow := filepath.Base(filepath.Dir(path))
		var terminal map[string]any
		_ = readJSON(filepath.Join(folder, "workflows", workflow+".json"), &terminal)
		err := readLines(ctx, path, false, func(_ int, d map[string]any) error {
			id := str(d["agentId"])
			if id == "" {
				return nil
			}
			key := workflow + ":" + id
			row, ok := state[key]
			if !ok {
				row = Child{Name: id, Status: "unknown"}
			}
			switch str(d["type"]) {
			case "started":
				row.Name = str(d["label"])
				if row.Name == "" {
					row.Name = id
				}
				row.Phase = str(d["phase"])
				row.Status = "unknown"
				if busy && path == latest && !isFile(filepath.Join(folder, "workflows", workflow+".json")) {
					row.Status = "running"
				}
			case "result", "completed":
				row.Status = "done"
			case "error", "failed":
				row.Status = "failed"
			case "cancelled", "stopped":
				row.Status = "stopped"
			}
			if row.Status == "running" || row.Status == "unknown" {
				switch str(terminal["status"]) {
				case "cancelled", "stopped", "failed":
					row.Status = str(terminal["status"])
				}
			}
			state[key] = row
			return nil
		})
		if err != nil && ctx.Err() != nil {
			return result, err
		}
	}
	err := filepath.WalkDir(agents, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "agent-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		id := strings.TrimSuffix(strings.TrimPrefix(d.Name(), "agent-"), ".jsonl")
		workflow := filepath.Base(filepath.Dir(path))
		if filepath.Dir(path) == agents {
			workflow = ""
		}
		var meta map[string]any
		_ = readJSON(strings.TrimSuffix(path, ".jsonl")+".meta.json", &meta)
		row, known := state[workflow+":"+id]
		if !known {
			row.Status = "unknown"
			_ = tailLines(ctx, path, 128<<10, func(_ int, d map[string]any) error {
				if str(d["type"]) == "system" && str(d["subtype"]) == "stop_hook_summary" {
					row.Status = "done"
				}
				return nil
			})
		}
		row.ID = workflow + ":" + id
		row.SessionID = id
		row.Path = path
		row.Model = lastModel(ctx, path)
		if row.Name == "" {
			row.Name = str(meta["description"])
		}
		if row.Name == "" {
			row.Name = id
		}
		if row.Phase == "" {
			row.Phase = str(meta["workflowPhase"])
		}
		row.Workflow = workflow
		if workflow != "" {
			scripts, _ := filepath.Glob(filepath.Join(folder, "workflows", "scripts", "*"+workflow+".js"))
			if len(scripts) > 0 {
				row.Workflow = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(scripts[0]), ".js"), "-"+workflow)
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		row.Updated = float64(info.ModTime().UnixNano()) / 1e9
		result = append(result, row)
		return ctx.Err()
	})
	if err != nil {
		return result, err
	}
	sort.Slice(result, func(i, j int) bool {
		if (result[i].Status == "running") != (result[j].Status == "running") {
			return result[i].Status == "running"
		}
		return result[i].Updated > result[j].Updated
	})
	return result, nil
}

func codexChildren(ctx context.Context, parent Session) ([]Child, error) {
	result := []Child{}
	path := filepath.Join(parent.Root, "state_5.sqlite")
	if !isFile(path) {
		return result, nil
	}
	db, err := openReadDB(path)
	if err != nil {
		return result, err
	}
	defer db.Close()
	cols, err := tableColumns(ctx, db, "threads")
	if err != nil {
		return result, err
	}
	for _, key := range []string{"id", "title", "rollout_path", "updated_at"} {
		if !cols[key] {
			return result, nil
		}
	}
	extra := []string{}
	for _, key := range []string{"model", "agent_nickname", "agent_role"} {
		if cols[key] {
			extra = append(extra, key)
		}
	}
	query := `WITH RECURSIVE children(id) AS (SELECT child_thread_id FROM thread_spawn_edges WHERE parent_thread_id=?
UNION SELECT e.child_thread_id FROM thread_spawn_edges e JOIN children c ON e.parent_thread_id=c.id)
SELECT t.id,t.title,t.rollout_path,t.updated_at`
	for _, key := range extra {
		query += ",t." + key
	}
	query += ` FROM threads t JOIN children c ON t.id=c.id WHERE t.id<>?`
	rows, err := db.QueryContext(ctx, query, parent.SessionID, parent.SessionID)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, nil
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]any, 4+len(extra))
		refs := make([]any, len(values))
		for i := range refs {
			refs[i] = &values[i]
		}
		if err := rows.Scan(refs...); err != nil {
			return result, err
		}
		row := Child{ID: str(values[0]), SessionID: str(values[0]), Name: str(values[1]), Path: str(values[2]), Updated: number(values[3]), Status: "unknown", Workflow: "Codex subagent"}
		for i, key := range extra {
			switch key {
			case "model":
				row.Model = str(values[4+i])
			case "agent_nickname":
				if name := str(values[4+i]); name != "" {
					row.Name = name
				}
			case "agent_role":
				row.Phase = str(values[4+i])
			}
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
