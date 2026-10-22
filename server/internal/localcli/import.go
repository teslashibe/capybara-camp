package localcli

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	_ "modernc.org/sqlite"
)

const maxRecord = 32 << 20

var errStop = errors.New("stop reading")

func titleFor(raw string) string {
	var out []rune
	space := false
	for _, r := range raw {
		if unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f {
			space = len(out) > 0
			continue
		}
		if space {
			out = append(out, ' ')
			space = false
			if len(out) == 110 {
				break
			}
		}
		out = append(out, r)
		if len(out) == 110 {
			break
		}
	}
	title := string(out)
	if strings.HasPrefix(title, "<") || strings.HasPrefix(title, "# AGENTS.md") || strings.HasPrefix(title, "You are ") {
		return "Imported session"
	}
	if title == "" {
		return "Untitled session"
	}
	return title
}

func textBlocks(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	var text []string
	for _, item := range array(value) {
		d := object(item)
		if str(d["type"]) == "text" || str(d["type"]) == "input_text" || str(d["type"]) == "output_text" {
			if s, ok := d["text"].(string); ok {
				text = append(text, s)
			}
		}
	}
	return strings.Join(text, "\n")
}
func str(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}
func number(v any) float64 {
	switch v := v.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	}
	return 0
}
func object(v any) map[string]any { d, _ := v.(map[string]any); return d }
func array(v any) []any           { a, _ := v.([]any); return a }
func boolean(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return number(v) != 0
}

func readLines(ctx context.Context, path string, strict bool, visit func(int, map[string]any) error) error {
	return readLinesN(ctx, path, strict, 0, visit)
}

func readLinesN(ctx context.Context, path string, strict bool, limit int, visit func(int, map[string]any) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return scanLines(ctx, f, strict, limit, visit)
}

func scanLines(ctx context.Context, input io.Reader, strict bool, limit int, visit func(int, map[string]any) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 65536), maxRecord)
	line := 0
	for (limit <= 0 || line < limit) && scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var d map[string]any
		err := json.Unmarshal(scanner.Bytes(), &d)
		if err != nil || d == nil {
			if strict && strings.TrimSpace(scanner.Text()) != "" {
				return errors.New("The transcript is incomplete; sharing will retry after it is written")
			}
		} else if err := visit(line, d); err != nil {
			if errors.Is(err, errStop) {
				return nil
			}
			return err
		}
		line++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("Read session record (limit 32 MiB): %w", err)
	}
	return ctx.Err()
}

func openReadDB(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(3000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	if table != "threads" {
		return nil, errors.New("Unsupported local table")
	}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(threads)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, name, typ, notnull, defaultValue, pk any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		cols[str(name)] = true
	}
	return cols, rows.Err()
}

func detectProvider(root string) string {
	if !isDir(root) {
		return ""
	}
	if isFile(filepath.Join(root, "state_5.sqlite")) {
		return "Codex"
	}
	if isDir(filepath.Join(root, "projects")) {
		return "Claude"
	}
	if isDir(filepath.Join(root, "sessions")) || isDir(filepath.Join(root, "archived_sessions")) {
		return "Codex"
	}
	return ""
}

func makeSession(source Source, id, title, cwd, path string, updated float64) Session {
	return Session{ID: source.ID + ":" + id, SessionID: id, Provider: source.Provider, SourceID: source.ID, SourceLabel: source.Label,
		Root: source.Root, Title: titleFor(title), CWD: cwd, Path: path, Updated: updated}
}

func (a *App) sources() ([]Source, []string, error) {
	var saved []string
	if err := readJSON(filepath.Join(a.Config.Support, "sources.json"), &saved); err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	homes := []string{a.Config.Home}
	if entries, err := os.ReadDir(a.Config.UsersDir); err == nil {
		for _, entry := range entries {
			path := filepath.Join(a.Config.UsersDir, entry.Name())
			if path != a.Config.Home && !strings.HasPrefix(entry.Name(), ".") && isDir(path) {
				homes = append(homes, path)
			}
		}
	}
	var roots []string
	for _, home := range homes {
		if entries, err := os.ReadDir(home); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".codex") || strings.HasPrefix(e.Name(), ".claude") {
					path := filepath.Join(home, e.Name())
					if isDir(path) {
						roots = append(roots, path)
					}
				}
			}
		}
	}
	roots = append(roots, saved...)
	roots = append(roots, a.Config.ProfileRoots...)
	result := []Source{}
	seen := map[string]bool{}
	for _, raw := range roots {
		root, err := canonical(raw, a.Config.Home)
		if err != nil || seen[root] {
			continue
		}
		provider := detectProvider(root)
		if provider == "" {
			continue
		}
		seen[root] = true
		label := filepath.Base(root)
		if filepath.Dir(root) != a.Config.Home {
			label = filepath.Base(filepath.Dir(root)) + "/" + label
		}
		result = append(result, Source{ID: provider + ":" + root, Provider: provider, Root: root, Label: label})
	}
	return result, saved, nil
}

func codexSessions(ctx context.Context, source Source) ([]Session, error) {
	result := []Session{}
	if isFile(filepath.Join(source.Root, "state_5.sqlite")) {
		db, err := openReadDB(filepath.Join(source.Root, "state_5.sqlite"))
		if err != nil {
			return nil, err
		}
		defer db.Close()
		cols, err := tableColumns(ctx, db, "threads")
		if err != nil {
			return nil, err
		}
		for _, name := range []string{"id", "title", "cwd", "updated_at", "rollout_path", "source"} {
			if !cols[name] {
				return nil, errors.New("This Codex database has an unsupported threads schema")
			}
		}
		selected := []string{"id", "title", "cwd", "updated_at", "rollout_path", "source"}
		for _, name := range []string{"creator_account_id", "archived", "model"} {
			if cols[name] {
				selected = append(selected, name)
			}
		}
		rows, err := db.QueryContext(ctx, "SELECT "+strings.Join(selected, ",")+" FROM threads ORDER BY updated_at DESC")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			values := make([]any, len(selected))
			refs := make([]any, len(selected))
			for i := range refs {
				refs[i] = &values[i]
			}
			if err = rows.Scan(refs...); err != nil {
				return nil, err
			}
			d := map[string]any{}
			for i, key := range selected {
				d[key] = values[i]
			}
			var origin map[string]any
			_ = json.Unmarshal([]byte(str(d["source"])), &origin)
			if _, child := origin["subagent"]; child {
				continue
			}
			s := makeSession(source, str(d["id"]), str(d["title"]), str(d["cwd"]), str(d["rollout_path"]), number(d["updated_at"]))
			s.AccountID = str(d["creator_account_id"])
			s.Archived = boolean(d["archived"])
			s.Model = str(d["model"])
			result = append(result, s)
		}
		return result, rows.Err()
	}
	for _, folder := range []string{"sessions", "archived_sessions"} {
		base := filepath.Join(source.Root, folder)
		if !isDir(base) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			return readLinesN(ctx, path, false, 1, func(_ int, v map[string]any) error {
				p := object(v["payload"])
				id := str(p["id"])
				if id == "" {
					id = str(p["session_id"])
				}
				if id == "" {
					return errStop
				}
				stat, err := os.Stat(path)
				if err != nil {
					return err
				}
				title := str(p["title"])
				if title == "" {
					title = "Codex session"
				}
				s := makeSession(source, id, title, str(p["cwd"]), path, float64(stat.ModTime().UnixNano())/1e9)
				s.Archived = folder == "archived_sessions"
				result = append(result, s)
				return errStop
			})
		})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

type cacheEntry struct {
	Signature []int64 `json:"signature"`
	Session   Session `json:"session"`
}

func claudeSessions(ctx context.Context, source Source, cache map[string]cacheEntry) ([]Session, error) {
	projects, err := os.ReadDir(filepath.Join(source.Root, "projects"))
	if err != nil {
		return nil, err
	}
	result := []Session{}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		base := filepath.Join(source.Root, "projects", project.Name())
		var index struct {
			Entries []map[string]any `json:"entries"`
		}
		_ = readJSON(filepath.Join(base, "sessions-index.json"), &index)
		entries := map[string]map[string]any{}
		for _, e := range index.Entries {
			entries[str(e["sessionId"])] = e
		}
		paths, err := filepath.Glob(filepath.Join(base, "*.jsonl"))
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			if strings.HasPrefix(filepath.Base(path), "agent-") {
				continue
			}
			stat, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			sig := []int64{stat.ModTime().UnixNano(), stat.Size()}
			if old, ok := cache[path]; ok && len(old.Signature) == 2 && old.Signature[0] == sig[0] && old.Signature[1] == sig[1] {
				result = append(result, old.Session)
				continue
			}
			id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
			entry := entries[id]
			cwd, title := str(entry["projectPath"]), str(entry["summary"])
			if title == "" {
				title = str(entry["firstPrompt"])
			}
			sidechain := false
			err = readLinesN(ctx, path, false, 100, func(_ int, d map[string]any) error {
				if v := str(d["cwd"]); v != "" {
					cwd = v
				}
				if v := str(d["sessionId"]); v != "" {
					id = v
				}
				sidechain = sidechain || boolean(d["isSidechain"])
				if str(d["type"]) == "user" {
					if title == "" {
						title = textBlocks(object(d["message"])["content"])
					}
					if cwd != "" {
						return errStop
					}
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
			if sidechain {
				continue
			}
			s := makeSession(source, id, title, cwd, path, float64(stat.ModTime().UnixNano())/1e9)
			lastSettings(ctx, "Claude", path).apply(&s)
			cache[path] = cacheEntry{sig, s}
			result = append(result, s)
		}
	}
	return result, nil
}

func claudeMetadata(home string) map[string]map[string]any {
	paths, _ := filepath.Glob(filepath.Join(home, "Library/Application Support/Claude/claude-code-sessions/*/*/local_*.json"))
	result := map[string]map[string]any{}
	for _, path := range paths {
		var d map[string]any
		if readJSON(path, &d) != nil {
			continue
		}
		id := str(d["cliSessionId"])
		if id != "" && number(d["lastActivityAt"]) >= number(result[id]["lastActivityAt"]) {
			result[id] = d
		}
	}
	return result
}

type ImportResult struct {
	Sessions    []Session `json:"sessions"`
	Sources     []Source  `json:"sources"`
	Errors      []string  `json:"errors"`
	FailedRoots []string  `json:"failedRoots"`
}

func (a *App) Scan(ctx context.Context) (ImportResult, error) {
	result := ImportResult{Sessions: []Session{}, Sources: []Source{}, Errors: []string{}, FailedRoots: []string{}}
	sources, saved, err := a.sources()
	if err != nil {
		return result, err
	}
	result.Sources = sources
	cache := map[string]cacheEntry{}
	_ = readJSON(filepath.Join(a.Config.Support, "import-cache.json"), &cache)
	if cache == nil {
		cache = map[string]cacheEntry{}
	}
	discovered := map[string]bool{}
	for _, s := range sources {
		discovered[s.Root] = true
	}
	for _, raw := range saved {
		path, _ := canonical(raw, a.Config.Home)
		if !discovered[path] {
			result.FailedRoots = append(result.FailedRoots, path)
			result.Errors = append(result.Errors, "Profile folder is unavailable or has no session store: "+raw)
		}
	}
	newest := map[string]Session{}
	metadata := map[string]map[string]map[string]any{}
	for i, source := range sources {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var sessions []Session
		if source.Provider == "Codex" {
			sessions, err = codexSessions(ctx, source)
		} else {
			sessions, err = claudeSessions(ctx, source, cache)
		}
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.Errors = append(result.Errors, source.Label+": "+err.Error())
			result.FailedRoots = append(result.FailedRoots, source.Root)
			continue
		}
		if source.Provider == "Claude" {
			home := filepath.Dir(source.Root)
			if metadata[home] == nil {
				metadata[home] = claudeMetadata(home)
			}
			for j := range sessions {
				meta := metadata[home][sessions[j].SessionID]
				if value := str(meta["title"]); value != "" {
					sessions[j].Title = titleFor(value)
				}
				if sessions[j].Model == "" {
					sessions[j].Model = str(meta["model"])
				}
			}
		}
		for _, s := range sessions {
			old, ok := newest[s.ID]
			if !ok || s.Updated > old.Updated {
				newest[s.ID] = s
			}
		}
		result.Sources[i].Count = len(sessions)
	}
	for _, s := range newest {
		result.Sessions = append(result.Sessions, s)
	}
	sort.Slice(result.Sessions, func(i, j int) bool {
		if result.Sessions[i].Updated == result.Sessions[j].Updated {
			return result.Sessions[i].ID < result.Sessions[j].ID
		}
		return result.Sessions[i].Updated > result.Sessions[j].Updated
	})
	for i := range result.Sources {
		count := 0
		for _, s := range result.Sessions {
			if s.SourceID == result.Sources[i].ID {
				count++
			}
		}
		result.Sources[i].Count = count
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := saveJSON(filepath.Join(a.Config.Support, "import-cache.json"), cache); err != nil {
		return result, err
	}
	return result, nil
}
