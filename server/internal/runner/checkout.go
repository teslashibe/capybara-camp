package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/processguard"
)

var errCheckoutUnsafe = errors.New("Checkout contains unsupported or unsafe paths")
var errCheckoutSize = errors.New("Checkout exceeds its size limit")
var errCheckoutGit = errors.New("Git checkout did not complete")
var repoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)
var objectID = regexp.MustCompile(`^[a-f0-9]{40}$`)

func validRepository(value string) bool {
	parts := strings.Split(value, "/")
	return repoName.MatchString(value) && len(parts) == 2 && parts[1] != "." && parts[1] != ".."
}
func validWorkspace(w camp.Workspace) bool {
	_, err := uuid.Parse(w.ID)
	_, sessionErr := uuid.Parse(w.SessionID)
	var transferErr error
	if w.TransferID != "" {
		_, transferErr = uuid.Parse(w.TransferID)
	}
	return err == nil && sessionErr == nil && transferErr == nil && validRepository(w.RepositoryName) && objectID.MatchString(w.BaseCommit) && w.Branch == "camp/"+w.SessionID+"/"+w.ID
}

type checkout struct {
	dir    string
	root   *os.Root
	lock   *os.File
	helper string
	// Set only by local transport tests; production always uses GitHub HTTPS.
	remote    func(camp.Workspace) string
	gitConfig []string
	guarded   bool
}

func openCheckout(dir string) (*checkout, error) {
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || !ownedPrivate(st) {
		return nil, ErrState
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrState
	}
	f, err := r.OpenFile("checkout.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		r.Close()
		return nil, ErrState
	}
	st, err = f.Stat()
	if err != nil || !st.Mode().IsRegular() || !ownedPrivate(st) || syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		r.Close()
		return nil, ErrState
	}
	helper, err := os.Executable()
	if err != nil {
		f.Close()
		r.Close()
		return nil, ErrState
	}
	return &checkout{dir: dir, root: r, lock: f, helper: helper}, nil
}
func (c *checkout) close()                          { _ = c.lock.Close(); _ = c.root.Close() }
func (c *checkout) workdir(w camp.Workspace) string { return filepath.Join(c.dir, "workspace-"+w.ID) }

func (c *checkout) remove(w camp.Workspace) error {
	if !validWorkspace(w) {
		return ErrState
	}
	return c.removeUnused(w.ID)
}

func (c *checkout) removeUnused(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrState
	}
	if _, err := c.root.Lstat("used-workspace-" + id + ".json"); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	if _, err := c.root.Lstat("transfer-workspace-" + id + ".json"); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	return c.root.RemoveAll("workspace-" + id)
}

func (c *checkout) entries() ([]os.DirEntry, error) {
	f, err := c.root.Open(".")
	if err != nil {
		return nil, ErrState
	}
	defer f.Close()
	entries, err := f.ReadDir(1025)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 1024 {
		return nil, ErrState
	}
	return entries, nil
}

// cleanCredentials removes abandoned tokens without touching prepared workspaces.
func (c *checkout) cleanCredentials() error {
	entries, err := c.entries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "credential-") {
			if err = c.root.RemoveAll(entry.Name()); err != nil {
				return ErrState
			}
		}
	}
	return nil
}

func (c *checkout) keepWorkspace(id string) error {
	entries, err := c.entries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := strings.TrimPrefix(entry.Name(), "workspace-")
		if name == entry.Name() || name == id {
			continue
		}
		if _, err = uuid.Parse(name); err != nil {
			continue
		}
		if _, err = c.root.Lstat("used-workspace-" + name + ".json"); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrState
		}
		if err = c.removeUnused(name); err != nil {
			return ErrState
		}
	}
	return c.cleanCredentials()
}

func (c *checkout) cleanAll() error { return c.keepWorkspace("") }

func (c *checkout) prepare(ctx context.Context, w camp.Workspace, token camp.RunnerWorkspaceToken) (result error) {
	if !validWorkspace(w) || token.WorkspaceID != w.ID || !validGitToken(token.Token) || !token.ExpiresAt.After(time.Now()) {
		return ErrState
	}
	if err := c.remove(w); err != nil {
		return err
	}
	if err := c.root.Mkdir("workspace-"+w.ID, 0700); err != nil {
		return ErrState
	}
	credentialName := "credential-" + uuid.NewString()
	credentialURL := "https://github.com/" + w.RepositoryName + ".git"
	data, _ := json.Marshal(gitCredential{URL: credentialURL, Token: token.Token, ExpiresAt: token.ExpiresAt})
	f, err := c.root.OpenFile(credentialName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrState
	}
	defer func() {
		if err := c.root.Remove(credentialName); err != nil && result == nil {
			result = ErrState
		}
	}()
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrState
	}
	credentialFile := filepath.Join(c.dir, credentialName)
	if _, err = c.git(ctx, w, "", "init", "--template="); err != nil {
		return err
	}
	remote := credentialURL
	if c.remote != nil {
		remote = c.remote(w)
	}
	if _, err = c.git(ctx, w, "", "remote", "add", "origin", remote); err != nil {
		return err
	}
	if _, err = c.git(ctx, w, credentialFile, "fetch", "--no-tags", "--depth=1", "origin", w.BaseCommit); err != nil {
		return err
	}
	tree, err := c.git(ctx, w, "", "ls-tree", "-rlz", "--full-tree", w.BaseCommit)
	if err != nil {
		return err
	}
	if err = c.validateTree(ctx, w, tree); err != nil {
		return err
	}
	if _, err = c.git(ctx, w, "", "checkout", "--no-recurse-submodules", "--no-track", "-b", w.Branch, w.BaseCommit); err != nil {
		return err
	}
	return c.verify(ctx, w)
}

func (c *checkout) verify(ctx context.Context, w camp.Workspace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validWorkspace(w) {
		return ErrState
	}
	st, err := c.root.Lstat("workspace-" + w.ID)
	if err != nil || !st.IsDir() || !ownedPrivate(st) {
		return ErrState
	}
	used := false
	if _, err = c.root.Lstat("used-workspace-" + w.ID + ".json"); err == nil {
		var saved camp.Workspace
		if c.readJSON("used-workspace-"+w.ID+".json", &saved) != nil || !sameWorkspace(saved, w) {
			return ErrState
		}
		used = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	head, err := c.git(ctx, w, "", "rev-parse", "--verify", "HEAD")
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !objectID.MatchString(strings.TrimSpace(string(head))) || !used && strings.TrimSpace(string(head)) != w.BaseCommit {
		return ErrState
	}
	branch, err := c.git(ctx, w, "", "symbolic-ref", "--short", "HEAD")
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || strings.TrimSpace(string(branch)) != w.Branch {
		return ErrState
	}
	if used {
		_, err = c.git(ctx, w, "", "merge-base", "--is-ancestor", w.BaseCommit, "HEAD")
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if w.TransferID != "" {
		held, err := c.transferHeld(w)
		if err != nil {
			return err
		}
		if held {
			return c.verifyTransfer(ctx, w)
		}
	}
	status, err := c.git(ctx, w, "", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || len(status) != 0 {
		return ErrState
	}
	return nil
}

func (c *checkout) validateTree(ctx context.Context, w camp.Workspace, data []byte) error {
	var total int64
	count := 0
	seen := map[string]bool{}
	links := map[string]string{}
	for _, record := range bytes.Split(data, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		count++
		if count > 20000 {
			return errCheckoutSize
		}
		meta, name, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 || !safeTreePath(name) || seen[strings.ToLower(name)] {
			return errCheckoutUnsafe
		}
		seen[strings.ToLower(name)] = true
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 {
			return errCheckoutUnsafe
		}
		if size > 32<<20 {
			return errCheckoutSize
		}
		total += size
		if total > 512<<20 {
			return errCheckoutSize
		}
		if fields[1] != "blob" || !objectID.MatchString(fields[2]) {
			return errCheckoutUnsafe
		}
		switch fields[0] {
		case "100644", "100755":
		case "120000":
			if size > 4096 {
				return errCheckoutUnsafe
			}
			target, err := c.git(ctx, w, "", "cat-file", "blob", fields[2])
			if err != nil {
				return err
			}
			value := string(target)
			if value == "" || path.IsAbs(value) || strings.Contains(value, "\\") || strings.ContainsFunc(value, unicode.IsControl) {
				return errCheckoutUnsafe
			}
			links[name] = value
		default:
			return errCheckoutUnsafe
		}
	}
	for name := range links {
		if !safeSymlink(name, links) {
			return errCheckoutUnsafe
		}
	}
	return nil
}

// Resolve the complete link graph before checkout. Cleaning each target
// lexically is insufficient: d/link -> ../x changes the meaning of later ..
// segments in another link that traverses d/link.
func safeSymlink(name string, links map[string]string) bool {
	pending := strings.Split(path.Dir(name)+"/"+links[name], "/")
	var resolved []string
	follows := 0
	for steps := 0; len(pending) > 0; steps++ {
		if steps >= 8192 {
			return false
		}
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return false
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}
		candidate := strings.Join(append(append([]string{}, resolved...), part), "/")
		if target, ok := links[candidate]; ok {
			follows++
			if follows > 40 {
				return false
			}
			pending = append(strings.Split(target, "/"), pending...)
		} else {
			resolved = append(resolved, part)
		}
	}
	return safeTreePath(strings.Join(resolved, "/"))
}

func safeTreePath(value string) bool {
	if value == "" || len(value) > 4096 || path.IsAbs(value) || path.Clean(value) != value || strings.Contains(value, "\\") || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." || strings.EqualFold(part, ".git") || strings.Contains(part, ":") {
			return false
		}
	}
	return true
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
		return 0, errCheckoutSize
	}
	return b.buffer.Write(p)
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (c *checkout) git(ctx context.Context, w camp.Workspace, credentialFile string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	options := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.attributesFile=/dev/null", "-c", "core.excludesFile=/dev/null", "-c", "submodule.recurse=false", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "http.followRedirects=false", "-c", "credential.helper=", "-c", "credential.useHttpPath=true"}
	options = append(options, c.gitConfig...)
	// HOME also fences libcurl's ambient .netrc lookup. Configuration flags
	// alone do not prevent that fallback to the operating-system user's home.
	env := []string{"PATH=/usr/bin:/bin", "HOME=/dev/null", "XDG_CONFIG_HOME=/dev/null", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}
	if credentialFile != "" {
		options = append(options, "-c", "credential.helper=!"+shellQuote(c.helper)+" git-credential")
		env = append(env, "CAMP_GIT_CREDENTIAL_FILE="+credentialFile)
	}
	path, commandArgs := "/usr/bin/git", append(options, args...)
	if c.guarded {
		home, err := os.MkdirTemp(c.dir, "git-home-")
		if err != nil {
			return nil, ErrState
		}
		defer os.RemoveAll(home)
		policy := processguard.Policy{Home: home, Workspace: c.workdir(w), WriteWorkspace: true}
		if credentialFile != "" {
			policy.ReadFiles = []string{credentialFile}
		}
		path, commandArgs, err = processguard.Wrap(path, append([]string{"git"}, commandArgs...), policy)
		if err != nil {
			return nil, err
		}
	}
	command := exec.CommandContext(ctx, path, commandArgs...)
	command.Dir = c.workdir(w)
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
	if stdout.overflow || stderr.overflow || errors.Is(err, errCheckoutSize) {
		return nil, errCheckoutSize
	}
	if err != nil {
		return nil, errCheckoutGit
	}
	return stdout.Bytes(), nil
}
