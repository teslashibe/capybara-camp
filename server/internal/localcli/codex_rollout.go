package localcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type rolloutWriter struct {
	SourceID, Path string
	PID            int
	Device, Inode  uint64
}

func (a *App) captureSystem(ctx context.Context, path string, args []string, timeout time.Duration, limit int) (int, []byte, error) {
	if a.systemCapture != nil {
		return a.systemCapture(ctx, path, args, timeout, limit)
	}
	if path != "/bin/ps" && path != "/usr/sbin/lsof" {
		return -1, nil, errors.New("Unsupported process inspection")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, args...)
	command.Env = a.runtimeEnvironment(Session{})
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 250 * time.Millisecond
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return killGroup(command.Process.Pid)
	}
	out := &boundedOutput{limit: limit}
	command.Stdout = out
	command.Stderr = io.Discard
	err := command.Run()
	if ctx.Err() != nil {
		return -1, nil, ctx.Err()
	}
	if errors.Is(err, errProcessOutputLimit) || out.Len() >= limit {
		return -1, nil, errProcessOutputLimit
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), out.Bytes(), nil
		}
		return -1, nil, errors.New("Process inspection failed")
	}
	return 0, out.Bytes(), nil
}

func (a *App) codexWriters(ctx context.Context, sources []Source) ([]rolloutWriter, error) {
	code, data, err := a.captureSystem(ctx, "/bin/ps", []string{"-axo", "pid=,comm="}, 3*time.Second, 4*codexFrameLimit)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, errors.New("Could not inspect Codex processes")
	}
	pids := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
		if filepath.Base(name) == "codex" {
			pids = append(pids, fields[0])
		}
	}
	if len(pids) == 0 {
		return []rolloutWriter{}, nil
	}
	if len(pids) > 256 {
		return nil, errors.New("Too many Codex processes to inspect")
	}
	code, data, err = a.captureSystem(ctx, "/usr/sbin/lsof", []string{"-nP", "-a", "-p", strings.Join(pids, ","), "-F0pfatDin"}, 5*time.Second, 8*codexFrameLimit)
	if err != nil {
		return nil, err
	}
	if code != 0 && code != 1 {
		return nil, errors.New("Could not inspect Codex session ownership")
	}
	files := parseCodexWriters(data, sources)
	if len(files) > 8192 {
		return nil, errors.New("Too many Codex transcript writers")
	}
	return files, nil
}

func under(path, base string) bool {
	relative, err := filepath.Rel(base, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func parseCodexWriters(data []byte, sources []Source) []rolloutWriter {
	files := []rolloutWriter{}
	seen := map[rolloutWriter]bool{}
	pid := 0
	item := map[byte]string{}
	finish := func() {
		if pid <= 0 || (item['a'] != "w" && item['a'] != "u") || item['t'] != "REG" || filepath.Ext(item['n']) != ".jsonl" {
			return
		}
		path, err := filepath.EvalSymlinks(item['n'])
		if err != nil {
			return
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return
		}
		inode, e1 := strconv.ParseUint(item['i'], 10, 64)
		device, e2 := strconv.ParseUint(strings.TrimPrefix(item['D'], "0x"), 16, 64)
		if e1 != nil || e2 != nil || inode != uint64(stat.Ino) || device != uint64(stat.Dev) {
			return
		}
		for _, source := range sources {
			if under(path, filepath.Join(source.Root, "sessions")) || under(path, filepath.Join(source.Root, "archived_sessions")) {
				value := rolloutWriter{SourceID: source.ID, Path: path, PID: pid, Device: device, Inode: inode}
				if !seen[value] {
					files = append(files, value)
					seen[value] = true
				}
				return
			}
		}
	}
	for _, raw := range bytes.Split(data, []byte{0}) {
		raw = bytes.TrimLeft(raw, "\n")
		if len(raw) == 0 {
			continue
		}
		name, value := raw[0], string(raw[1:])
		switch name {
		case 'p':
			finish()
			item = map[byte]string{}
			pid, _ = strconv.Atoi(value)
		case 'f':
			finish()
			item = map[byte]string{}
		default:
			item[name] = value
		}
	}
	finish()
	return files
}

type rolloutIndex struct {
	path                   string
	device, inode          uint64
	offset                 int64
	partial                []byte
	discard                bool
	state, turn, sessionID string
	settings               settingsReader
}

func (r *rolloutIndex) consume(data []byte) {
	if bytes.Contains(data, []byte(`"turn_context"`)) || bytes.Contains(data, []byte(`"thread_settings"`)) {
		var record map[string]any
		if json.Unmarshal(data, &record) == nil {
			r.settings.consume("Codex", record)
		}
	}
	if r.sessionID == "" {
		var v map[string]any
		if json.Unmarshal(data, &v) == nil && str(v["type"]) == "session_meta" {
			if id := str(object(v["payload"])["id"]); len(id) <= 200 {
				r.sessionID = id
			}
		}
	}
	if !bytes.Contains(data, []byte("task_started")) && !bytes.Contains(data, []byte("turn_started")) && !bytes.Contains(data, []byte("task_complete")) && !bytes.Contains(data, []byte("turn_complete")) && !bytes.Contains(data, []byte("turn_aborted")) {
		return
	}
	var v map[string]any
	if json.Unmarshal(data, &v) != nil || str(v["type"]) != "event_msg" {
		return
	}
	p := object(v["payload"])
	turn := str(p["turn_id"])
	if len(turn) > 200 {
		r.state = "unknown"
		return
	}
	switch str(p["type"]) {
	case "task_started", "turn_started":
		r.turn = turn
		r.state = "running"
	case "task_complete", "turn_complete", "turn_aborted":
		if turn == "" || r.turn == "" || turn == r.turn {
			r.state = "idle"
		}
	}
}

func (r *rolloutIndex) update(ctx context.Context, writer rolloutWriter, budget int64) (string, int64, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return "unknown", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "unknown", 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != writer.Device || uint64(stat.Ino) != writer.Inode {
		return "unknown", 0, errors.New("Codex rollout writer changed")
	}
	if r.inode != writer.Inode || r.device != writer.Device || info.Size() < r.offset {
		*r = rolloutIndex{path: r.path, device: writer.Device, inode: writer.Inode, state: "unknown"}
	}
	if _, err = f.Seek(r.offset, io.SeekStart); err != nil {
		return "unknown", 0, err
	}
	used := int64(0)
	chunk := make([]byte, 65536)
	for used < budget {
		if err = ctx.Err(); err != nil {
			return "unknown", used, err
		}
		limit := int64(len(chunk))
		if budget-used < limit {
			limit = budget - used
		}
		n, e := f.Read(chunk[:limit])
		used += int64(n)
		r.offset += int64(n)
		parts := bytes.Split(chunk[:n], []byte{'\n'})
		for i, part := range parts {
			if !r.discard {
				if len(r.partial)+len(part) > codexFrameLimit {
					r.partial = nil
					r.discard = true
				} else {
					r.partial = append(r.partial, part...)
				}
			}
			if i < len(parts)-1 {
				if !r.discard {
					r.consume(r.partial)
				}
				r.partial = r.partial[:0]
				if cap(r.partial) > 4096 {
					r.partial = nil
				}
				r.discard = false
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "unknown", used, e
		}
		if n == 0 {
			break
		}
	}
	if r.offset < info.Size() {
		return "unknown", used, nil
	}
	return r.state, used, nil
}

func codexSession(ctx context.Context, source Source, id, path string, metadata map[string]any) (Session, any) {
	row := map[string]any{}
	for key, value := range metadata {
		row[key] = value
	}
	if isFile(filepath.Join(source.Root, "state_5.sqlite")) {
		if db, err := openReadDB(filepath.Join(source.Root, "state_5.sqlite")); err == nil {
			func() {
				defer db.Close()
				cols, err := tableColumns(ctx, db, "threads")
				if err != nil || !cols["id"] {
					return
				}
				names := []string{}
				for _, name := range []string{"id", "title", "cwd", "rollout_path", "updated_at", "model", "source", "archived"} {
					if cols[name] {
						names = append(names, name)
					}
				}
				values := make([]any, len(names))
				refs := make([]any, len(names))
				for i := range refs {
					refs[i] = &values[i]
				}
				if err = db.QueryRowContext(ctx, "SELECT "+strings.Join(names, ",")+" FROM threads WHERE id=?", id).Scan(refs...); err == nil {
					for i, key := range names {
						row[key] = values[i]
					}
				}
			}()
		}
	}
	title := str(row["title"])
	if title == "" {
		title = str(row["name"])
	}
	if title == "" {
		title = "Codex session"
	}
	if path == "" {
		path = str(row["rollout_path"])
	}
	if path == "" {
		path = str(row["path"])
	}
	updated := number(row["updated_at"])
	if updated == 0 {
		updated = number(row["updatedAt"])
	}
	s := makeSession(source, id, title, str(row["cwd"]), path, updated)
	s.Model = str(row["model"])
	lastSettings(ctx, "Codex", path).apply(&s)
	s.Archived = boolean(row["archived"])
	kind := row["source"]
	if kind == nil {
		kind = row["sourceKind"]
	}
	return s, kind
}
