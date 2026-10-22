package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type treeFile struct{ mode, oid string }

// A named buffer avoids promoting ReadFrom, which would bypass Write's cap
// when os/exec copies from the child's pipe.
type limitedOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > b.limit {
		b.overflow = true
		return 0, ErrLimit
	}
	return b.buffer.Write(p)
}

// Git reads never inherit credentials, aliases, hooks or diff/text conversion
// commands from the launching process. Only fixed read-only commands are used.
func git(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	options := []string{"--no-pager", "--literal-pathspecs", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.attributesFile=/dev/null", "-c", "color.ui=false", "-c", "credential.helper=", "-c", "protocol.allow=never"}
	command := exec.CommandContext(ctx, "/usr/bin/git", append(options, args...)...)
	command.Dir = dir
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/dev/null", "XDG_CONFIG_HOME=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "LANG=C"}
	command.WaitDelay = time.Second
	out := &limitedOutput{limit: limit}
	command.Stdout = out
	command.Stderr = io.Discard
	err := command.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if out.overflow {
		return nil, ErrLimit
	}
	if err != nil {
		return nil, ErrRepository
	}
	return out.buffer.Bytes(), nil
}

func revision(ctx context.Context, dir string) (string, string, error) {
	data, err := git(ctx, dir, 1024, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", "", err
	}
	head := strings.TrimSpace(string(data))
	if !commitPattern.MatchString(head) {
		return "", "", ErrRepository
	}
	branch, err := git(ctx, dir, 1024, "rev-parse", "--abbrev-ref=strict", "HEAD")
	if err != nil {
		return "", "", err
	}
	name := strings.TrimSpace(string(branch))
	if name == "HEAD" {
		name = ""
	}
	return head, name, nil
}

func verifyRoot(ctx context.Context, dir string) error {
	if !filepath.IsAbs(dir) {
		return ErrInput
	}
	top, err := git(ctx, dir, 8192, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil || filepath.Clean(strings.TrimSpace(string(top))) != canonical {
		return ErrRepository
	}
	return nil
}

func readFile(root *os.Root, name string) ([]byte, bool, string, error) {
	parts := strings.Split(name, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, false, "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, false, "symlink", nil
		}
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false, "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, false, "", err
	}
	if !before.Mode().IsRegular() {
		return nil, false, "unsupported", nil
	}
	if stat, ok := before.Sys().(*syscall.Stat_t); !ok || stat.Nlink > 1 {
		return nil, false, "hardlink", nil
	}
	if before.Size() > MaxFileBytes {
		return nil, false, "", ErrLimit
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, false, "", err
	}
	after, err := f.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
		return nil, false, "", ErrChanged
	}
	if len(data) > MaxFileBytes {
		return nil, false, "", ErrLimit
	}
	return data, before.Mode()&0111 != 0, "", nil
}

func Capture(ctx context.Context, dir string, selected []string) (Manifest, error) {
	if !filepath.IsAbs(dir) || len(selected) == 0 || len(selected) > MaxFiles {
		return Manifest{}, ErrInput
	}
	seen := []string{}
	for _, name := range selected {
		if !safePath(name) {
			return Manifest{}, ErrInput
		}
		for _, prior := range seen {
			if strings.EqualFold(name, prior) {
				return Manifest{}, ErrInput
			}
		}
		seen = append(seen, name)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Manifest{}, ErrRepository
	}
	defer root.Close()
	if err := verifyRoot(ctx, dir); err != nil {
		return Manifest{}, err
	}
	head, branch, err := revision(ctx, dir)
	if err != nil {
		return Manifest{}, err
	}
	data, err := git(ctx, dir, 8<<20, "ls-tree", "-rlz", "--full-tree", head)
	if err != nil {
		return Manifest{}, err
	}
	wanted := make(map[string]treeFile, len(selected))
	for _, name := range selected {
		wanted[name] = treeFile{}
	}
	for _, line := range bytes.Split(data, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		info, name, ok := bytes.Cut(line, []byte{'\t'})
		fields := strings.Fields(string(info))
		if !ok || len(fields) != 4 {
			return Manifest{}, ErrRepository
		}
		if _, ok := wanted[string(name)]; ok {
			wanted[string(name)] = treeFile{mode: fields[0], oid: fields[2]}
		}
	}
	m := Manifest{Version: 1, BaseCommit: head, Branch: branch, Files: []File{}, Excluded: []Exclusion{}}
	total := 0
	sort.Strings(seen)
	for _, name := range seen {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		if excludedPath(name) {
			m.Excluded = append(m.Excluded, Exclusion{name, "policy"})
			continue
		}
		base := wanted[name]
		if base.mode != "" && base.mode != "100644" && base.mode != "100755" {
			m.Excluded = append(m.Excluded, Exclusion{name, "unsupported"})
			continue
		}
		file := File{Path: name, BeforeExecutable: base.mode == "100755"}
		if base.oid != "" {
			body, err := git(ctx, dir, MaxFileBytes, "cat-file", "blob", base.oid)
			if err != nil {
				return Manifest{}, err
			}
			file.BeforeSHA256 = hash(body)
		}
		body, executable, reason, err := readFile(root, name)
		if errors.Is(err, os.ErrNotExist) && base.oid != "" {
			file.Deleted = true
			m.Files = append(m.Files, file)
			continue
		}
		if err != nil {
			return Manifest{}, err
		}
		if reason != "" {
			m.Excluded = append(m.Excluded, Exclusion{name, reason})
			continue
		}
		if secretPattern.Match(body) {
			m.Excluded = append(m.Excluded, Exclusion{name, "policy"})
			continue
		}
		file.Data, file.SHA256, file.Executable = body, hash(body), executable
		if file.BeforeSHA256 == file.SHA256 && file.BeforeExecutable == file.Executable {
			m.Excluded = append(m.Excluded, Exclusion{name, "unchanged"})
			continue
		}
		total += len(body)
		if total > MaxTotalBytes {
			return Manifest{}, ErrLimit
		}
		m.Files = append(m.Files, file)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	if err := VerifySource(ctx, dir, m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// VerifySource rechecks the approved source immediately before staging. It is
// not an execution handoff; callers must still fence the source and commit the
// destination's receipt before changing the assignment.
func VerifySource(ctx context.Context, dir string, m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if err := verifyRoot(ctx, dir); err != nil {
		return err
	}
	head, branch, err := revision(ctx, dir)
	if err != nil {
		return err
	}
	if head != m.BaseCommit || branch != m.Branch {
		return ErrChanged
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ErrChanged
	}
	defer root.Close()
	for _, file := range m.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, executable, reason, err := readFile(root, file.Path)
		if file.Deleted && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || reason != "" || file.Deleted || hash(body) != file.SHA256 || executable != file.Executable {
			return ErrChanged
		}
	}
	return nil
}
