package localcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/config"
)

type App struct {
	Config        config.Local
	commandPath   func(string, []string) (string, error)
	systemCapture func(context.Context, string, []string, time.Duration, int) (int, []byte, error)
}

func New(c config.Local) *App { return &App{Config: c} }

type Source struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Root     string `json:"root"`
	Label    string `json:"label"`
	Count    int    `json:"count"`
}

type Session struct {
	ID          string  `json:"id"`
	SessionID   string  `json:"sessionID"`
	Provider    string  `json:"provider"`
	SourceID    string  `json:"sourceID"`
	SourceLabel string  `json:"sourceLabel"`
	Root        string  `json:"root"`
	Title       string  `json:"title"`
	CWD         string  `json:"cwd"`
	Updated     float64 `json:"updated"`
	Path        string  `json:"path"`
	AccountID   string  `json:"accountID"`
	Archived    bool    `json:"archived"`
	Model       string  `json:"model"`
	IsSubagent  bool    `json:"isSubagent,omitempty"`
}

type Message struct {
	ID    string `json:"id"`
	Role  string `json:"role"`
	Text  string `json:"text"`
	Model string `json:"model,omitempty"`
}

type request struct {
	Session     Session            `json:"session"`
	Permissions PermissionSettings `json:"permissions"`
	Limit       *int               `json:"limit"`
	After       *int64             `json:"after"`
	Live        bool               `json:"live"`
}

func (a *App) Run(ctx context.Context, action string, input io.Reader) (any, error) {
	if action == "discover" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sources, _, err := a.sources()
		return map[string]any{"sources": sources}, err
	}
	if action == "scan" {
		return a.Scan(ctx)
	}
	if action == "add-source" {
		data, err := io.ReadAll(io.LimitReader(input, 1<<20+1))
		if err != nil {
			return nil, err
		}
		if len(data) > 1<<20 {
			return nil, errors.New("Profile path is too long")
		}
		return map[string]bool{"ok": true}, a.AddSource(ctx, strings.TrimSpace(string(data)))
	}
	if action == "activity" {
		data, err := io.ReadAll(io.LimitReader(input, 1<<20+1))
		if err != nil || len(data) > 1<<20 {
			return nil, errors.New("Invalid activity request")
		}
		var request ActivityRequest
		if strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
			err = json.Unmarshal(data, &request.Sources)
		} else {
			err = json.Unmarshal(data, &request)
		}
		if err != nil || request.Sources == nil {
			return nil, errors.New("Invalid activity request")
		}
		return a.Activity(ctx, request)
	}
	var r request
	decoder := json.NewDecoder(io.LimitReader(input, 1<<20+1))
	if err := decoder.Decode(&r); err != nil {
		return nil, errors.New("Invalid local helper request")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("Unexpected data after local helper request")
	}
	switch action {
	case "permissions":
		return a.permissionCatalog(ctx, r.Session)
	case "execution-context":
		return a.executionContext(ctx, r.Session, r.Permissions)
	case "transcript":
		limit := 120
		if r.Limit != nil {
			limit = *r.Limit
		}
		return a.Transcript(ctx, r.Session, limit)
	case "export-page":
		limit, after := 200, int64(-1)
		if r.Limit != nil {
			limit = *r.Limit
		}
		if r.After != nil {
			after = *r.After
		}
		return a.ExportPage(ctx, r.Session, after, limit)
	case "children":
		children, err := childrenFor(ctx, r.Session, r.Live)
		return map[string]any{"children": children}, err
	default:
		return nil, fmt.Errorf("Unsupported Go helper action: %s", action)
	}
}

func canonical(path, home string) (string, error) {
	if path == "~" {
		path = home
	} else if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return filepath.Clean(abs), nil
}

func isDir(path string) bool { stat, err := os.Stat(path); return err == nil && stat.IsDir() }
func isFile(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && stat.Mode().IsRegular()
}

func readJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 32<<20+1))
	if err = decoder.Decode(value); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return errors.New("Invalid JSON state file")
	}
	return nil
}

func saveJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".camp-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err == nil {
		err = json.NewEncoder(f).Encode(value)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	return err
}

func (a *App) AddSource(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("Choose a Codex home with sessions or a Claude folder with projects")
	}
	root, err := canonical(path, a.Config.Home)
	if err != nil {
		return err
	}
	if detectProvider(root) == "" {
		return errors.New("Choose a Codex home with sessions or a Claude folder with projects")
	}
	if err := os.MkdirAll(a.Config.Support, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(a.Config.Support, "sources.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	var roots []string
	path = filepath.Join(a.Config.Support, "sources.json")
	if err = readJSON(path, &roots); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, saved := range roots {
		if saved == root {
			return nil
		}
	}
	return saveJSON(path, append(roots, root))
}
