package localcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/processguard"
)

// Every invocation chooses one profile. Ambient credentials and tuning must
// never override that selection, including when the app was opened in a shell.
func (a *App) runtimeEnvironment(s Session) []string {
	env := map[string]string{}
	for _, entry := range a.Config.Environment {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDECODE", "CODEX_THREAD_ID", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE", "CLAUDE_CODE_EFFORT_LEVEL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_MODEL", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		delete(env, key)
	}
	if !a.isolated {
		env["PATH"] = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:" + env["PATH"]
	}
	if s.Provider == "Codex" {
		env["CODEX_HOME"] = s.Root
	} else if s.Provider == "Claude" {
		root, _ := canonical(s.Root, a.Config.Home)
		defaultRoot, _ := canonical(filepath.Join(a.Config.Home, ".claude"), a.Config.Home)
		// Explicitly setting Claude's default directory chooses a different
		// Keychain service. Keep its normal implicit default for that profile.
		if root != defaultRoot {
			env["CLAUDE_CONFIG_DIR"] = s.Root
		}
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

func (a *App) providerCommand(path string, args []string, cwd string, write bool) (string, []string, error) {
	if !a.confined {
		return path, args[1:], nil
	}
	return processguard.Wrap(path, args, processguard.Policy{Home: a.Config.Home, Workspace: cwd, WriteWorkspace: write})
}

func executable(name string, env []string) (string, error) {
	if name != "codex" && name != "claude" {
		return "", errors.New("Unsupported provider CLI")
	}
	for _, entry := range env {
		if !strings.HasPrefix(entry, "PATH=") {
			continue
		}
		for _, dir := range filepath.SplitList(strings.TrimPrefix(entry, "PATH=")) {
			if dir == "" || !filepath.IsAbs(dir) {
				continue
			}
			path := filepath.Join(dir, name)
			if stat, err := os.Stat(path); err == nil && stat.Mode().IsRegular() && stat.Mode().Perm()&0111 != 0 {
				return path, nil
			}
		}
	}
	return "", errors.New("Install the selected provider CLI before using this profile")
}

var errProcessOutputLimit = errors.New("The provider CLI exceeded the output limit")

type boundedOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedOutput) Len() int      { return b.buffer.Len() }
func (b *boundedOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedOutput) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		_, _ = b.buffer.Write(p[:remaining])
		return remaining, errProcessOutputLimit
	}
	return b.buffer.Write(p)
}

func killGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// Run only Camp's child process group, with bounded output and time. No provider
// PID discovered in a registry or transcript is ever signalled here.
func (a *App) probe(ctx context.Context, s Session, args []string, timeout time.Duration) ([]byte, error) {
	return a.probeAt(ctx, s, args, timeout, a.Config.Home)
}

func (a *App) probeAt(ctx context.Context, s Session, args []string, timeout time.Duration, cwd string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("Missing provider command")
	}
	env := a.runtimeEnvironment(s)
	lookup := a.commandPath
	if lookup == nil {
		lookup = executable
	}
	path, err := lookup(args[0], env)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	path, commandArgs, err := a.providerCommand(path, args, cwd, false)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, path, commandArgs...)
	command.Env = env
	command.Dir = cwd
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout := &boundedOutput{limit: 2 << 20}
	stderr := &boundedOutput{limit: 64 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	command.WaitDelay = 250 * time.Millisecond
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return killGroup(command.Process.Pid)
	}
	err = command.Run()
	if command.Process != nil {
		_ = killGroup(command.Process.Pid)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, errProcessOutputLimit) || stdout.Len() >= stdout.limit || stderr.Len() >= stderr.limit {
			return nil, errProcessOutputLimit
		}
		// Stderr may contain login details. Only the caller's fixed, safe
		// operation error is shown; raw provider output stays out of logs.
		return nil, errors.New("The provider CLI could not complete the status request")
	}
	return stdout.Bytes(), nil
}
