package localcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func (a *App) assertAvailable(ctx context.Context, session Session) error {
	if session.IsSubagent {
		return errors.New("Subagents are read-only in Camp. Continue their parent session")
	}
	if session.Provider == "Claude" {
		return a.assertClaudeAvailable(ctx, session)
	}
	return a.assertCodexAvailable(ctx, session)
}

func (a *App) assertCodexAvailable(ctx context.Context, session Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !isDir(filepath.Join(session.Root, "sessions")) && !isFile(filepath.Join(session.Root, "state_5.sqlite")) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	unavailable := errors.New("Codex session ownership could not be verified. Retry after its runtime is available")
	busy := errors.New("This session is open in Codex. Close it there before sending from Camp")
	source := Source{ID: "Codex:" + session.Root, Provider: "Codex", Root: session.Root}
	writers, err := a.codexWriters(ctx, []Source{source})
	if err != nil {
		return unavailable
	}
	budget := 8 << 20
	for _, writer := range writers {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if session.Path != "" {
			path, err := filepath.EvalSymlinks(session.Path)
			if err == nil && path == writer.Path {
				return busy
			}
		}
		file, err := os.OpenFile(writer.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return unavailable
		}
		info, err := file.Stat()
		var stat *syscall.Stat_t
		if info != nil {
			stat, _ = info.Sys().(*syscall.Stat_t)
		}
		if err != nil || !info.Mode().IsRegular() || stat == nil || uint64(stat.Dev) != writer.Device || uint64(stat.Ino) != writer.Inode {
			file.Close()
			return unavailable
		}
		limit := min(budget, codexFrameLimit+1)
		if limit < 1 {
			file.Close()
			return unavailable
		}
		line, readErr := bufio.NewReaderSize(io.LimitReader(file, int64(limit)), limit).ReadBytes('\n')
		file.Close()
		budget -= len(line)
		if len(line) > codexFrameLimit || readErr != nil && readErr != io.EOF {
			return unavailable
		}
		var metadata map[string]any
		if json.Unmarshal(bytes.TrimSpace(line), &metadata) != nil {
			return unavailable
		}
		if str(metadata["type"]) == "session_meta" && str(object(metadata["payload"])["id"]) == session.SessionID {
			return busy
		}
	}
	if _, err := os.Lstat(filepath.Join(session.Root, "app-server-control/app-server-control.sock")); err == nil {
		daemon, err := openCodexDaemon(ctx, session.Root)
		if err != nil {
			return unavailable
		}
		defer daemon.close()
		stopClose := context.AfterFunc(ctx, daemon.close)
		defer stopClose()
		ids, err := daemon.loadedIDs(ctx)
		if err != nil {
			return unavailable
		}
		for _, id := range ids {
			if id == session.SessionID {
				return busy
			}
		}
	} else if !os.IsNotExist(err) {
		return unavailable
	}
	return ctx.Err()
}
