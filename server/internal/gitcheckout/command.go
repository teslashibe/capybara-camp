package gitcheckout

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/processguard"
)

type Command struct {
	Directory      string
	StateDir       string
	Helper         string
	CredentialFile string
	Guarded        bool
	// Internal transport fixtures only. Application input never sets Git options.
	ExtraConfig []string
}

// Keep the buffer named: an embedded Buffer promotes ReadFrom and lets
// io.Copy bypass Write's output limit.
type gitOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *gitOutput) Bytes() []byte { return b.buffer.Bytes() }
func (b *gitOutput) Len() int      { return b.buffer.Len() }

func (b *gitOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		b.overflow = true
		return 0, ErrSize
	}
	return b.buffer.Write(p)
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func Run(ctx context.Context, config Command, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	options := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.attributesFile=/dev/null", "-c", "core.excludesFile=/dev/null", "-c", "submodule.recurse=false", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "http.followRedirects=false", "-c", "credential.helper=", "-c", "credential.useHttpPath=true"}
	options = append(options, config.ExtraConfig...)
	// HOME also fences libcurl's ambient .netrc lookup. Configuration flags
	// alone do not prevent that fallback to the operating-system user's home.
	env := []string{"PATH=/usr/bin:/bin", "HOME=/dev/null", "XDG_CONFIG_HOME=/dev/null", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}
	if config.CredentialFile != "" {
		options = append(options, "-c", "credential.helper=!"+shellQuote(config.Helper)+" git-credential")
		env = append(env, "CAMP_GIT_CREDENTIAL_FILE="+config.CredentialFile)
	}
	path, commandArgs := "/usr/bin/git", append(options, args...)
	if config.Guarded {
		home, err := os.MkdirTemp(config.StateDir, "git-home-")
		if err != nil {
			return nil, ErrState
		}
		defer os.RemoveAll(home)
		policy := processguard.Policy{Home: home, Workspace: config.Directory, WriteWorkspace: true}
		if config.CredentialFile != "" {
			policy.ReadFiles = []string{config.CredentialFile}
		}
		path, commandArgs, err = processguard.Wrap(path, append([]string{"git"}, commandArgs...), policy)
		if err != nil {
			return nil, err
		}
	}
	command := exec.CommandContext(ctx, path, commandArgs...)
	command.Dir = config.Directory
	command.Env = env
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process != nil {
			return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	command.WaitDelay = time.Second
	stdout, stderr := &gitOutput{limit: 8 << 20}, &gitOutput{limit: 64 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.overflow || stderr.overflow || errors.Is(err, ErrSize) {
		return nil, ErrSize
	}
	if err != nil {
		return nil, ErrGit
	}
	return stdout.Bytes(), nil
}
