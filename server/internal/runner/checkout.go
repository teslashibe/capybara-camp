package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/gitcheckout"
)

var errCheckoutUnsafe = gitcheckout.ErrUnsafe
var errCheckoutSize = gitcheckout.ErrSize
var errCheckoutGit = gitcheckout.ErrGit

func validRepository(value string) bool { return gitcheckout.ValidRepository(value) }
func validWorkspace(w camp.Workspace) bool {
	_, err := uuid.Parse(w.ID)
	_, sessionErr := uuid.Parse(w.SessionID)
	var transferErr error
	if w.TransferID != "" {
		_, transferErr = uuid.Parse(w.TransferID)
	}
	return err == nil && sessionErr == nil && transferErr == nil && validRepository(w.RepositoryName) && gitcheckout.ValidCommit(w.BaseCommit) && w.Branch == "camp/"+w.SessionID+"/"+w.ID
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

// cleanCredentials removes abandoned tokens and incomplete export writes without
// touching prepared workspaces or resumable export payloads.
func (c *checkout) cleanCredentials() error {
	entries, err := c.entries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "credential-") || strings.HasPrefix(entry.Name(), "export-temp-") {
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
	if err != nil || !gitcheckout.ValidCommit(strings.TrimSpace(string(head))) || !used && strings.TrimSpace(string(head)) != w.BaseCommit {
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
	return gitcheckout.ValidateTree(ctx, data, func(ctx context.Context, id string) ([]byte, error) { return c.git(ctx, w, "", "cat-file", "blob", id) })
}
func (c *checkout) git(ctx context.Context, w camp.Workspace, credentialFile string, args ...string) ([]byte, error) {
	return gitcheckout.Run(ctx, gitcheckout.Command{Directory: c.workdir(w), StateDir: c.dir, Helper: c.helper, CredentialFile: credentialFile, Guarded: c.guarded, ExtraConfig: c.gitConfig}, args...)
}
