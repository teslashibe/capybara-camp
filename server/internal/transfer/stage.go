package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

var ErrDestination = errors.New("destination changed or is not an isolated checkout")
var ErrStageBusy = errors.New("destination staging is already running")

// StageReceipt records file verification only. It never grants execution or
// replaces the server's source fence and assignment transaction.
type StageReceipt struct {
	ReviewSHA256 string `json:"review_sha256"`
	BaseCommit   string `json:"base_commit"`
	Branch       string `json:"branch"`
	Files        int    `json:"files"`
}

type stageRecord struct {
	Version int          `json:"version"`
	Root    string       `json:"root"`
	Inode   string       `json:"inode"`
	Receipt StageReceipt `json:"receipt"`
	Ready   bool         `json:"ready"`
}

// Stager owns one private checkout and a separate private journal directory.
// The caller must keep provider processes and other writers out of the checkout
// until handoff. The lock excludes other cooperating stagers, not arbitrary
// processes running as the same OS user.
type Stager struct {
	mu            sync.Mutex
	dir           string
	root          *os.Root
	state         *os.Root
	lock          *os.File
	workspaceLock *os.File
	inode         string
	closed        bool
}

func privateDirectory(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0077 == 0 && int(st.Uid) == os.Geteuid()
}

func rootIdentity(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}

func nestedDirectory(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func OpenStager(dir, stateDir string) (*Stager, error) {
	if !filepath.IsAbs(dir) || !filepath.IsAbs(stateDir) {
		return nil, ErrInput
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, ErrDestination
	}
	stateCanonical, err := filepath.EvalSymlinks(stateDir)
	if err != nil || nestedDirectory(canonical, stateCanonical) || nestedDirectory(stateCanonical, canonical) {
		return nil, ErrDestination
	}
	info, err := os.Lstat(dir)
	if err != nil || !privateDirectory(info) {
		return nil, ErrDestination
	}
	stateInfo, err := os.Lstat(stateDir)
	if err != nil || !privateDirectory(stateInfo) || info.Sys().(*syscall.Stat_t).Dev != stateInfo.Sys().(*syscall.Stat_t).Dev {
		return nil, ErrDestination
	}
	dir, stateDir = canonical, stateCanonical
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	state, err := os.OpenRoot(stateDir)
	if err != nil {
		root.Close()
		return nil, err
	}
	workspaceLock, err := root.Open(".")
	if err == nil {
		err = syscall.Flock(int(workspaceLock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		if workspaceLock != nil {
			workspaceLock.Close()
		}
		state.Close()
		root.Close()
		return nil, fmt.Errorf("%w: checkout lock: %v", ErrDestination, err)
	}
	lock, err := state.OpenFile("stage.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err == nil {
		var lockInfo os.FileInfo
		lockInfo, err = lock.Stat()
		if err == nil && (!lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0077 != 0 || lockInfo.Sys().(*syscall.Stat_t).Nlink != 1 || int(lockInfo.Sys().(*syscall.Stat_t).Uid) != os.Geteuid()) {
			err = ErrDestination
		}
		if err == nil {
			err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		}
	}
	if err != nil {
		if lock != nil {
			lock.Close()
		}
		state.Close()
		workspaceLock.Close()
		root.Close()
		return nil, ErrDestination
	}
	return &Stager{dir: canonical, root: root, state: state, lock: lock, workspaceLock: workspaceLock, inode: rootIdentity(info)}, nil
}

func (s *Stager) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.lock.Close(), s.workspaceLock.Close(), s.state.Close(), s.root.Close())
}

func (s *Stager) verifyRoot(ctx context.Context, base, branch string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed {
		return ErrDestination
	}
	pathInfo, err := os.Lstat(s.dir)
	if err != nil || !privateDirectory(pathInfo) || rootIdentity(pathInfo) != s.inode {
		return ErrDestination
	}
	openedInfo, err := s.root.Stat(".")
	if err != nil || !os.SameFile(pathInfo, openedInfo) || verifyRoot(ctx, s.dir) != nil {
		return ErrDestination
	}
	head, actualBranch, err := revision(ctx, s.dir)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || head != base || actualBranch != branch {
		return ErrDestination
	}
	return nil
}

func (s *Stager) load() (*stageRecord, error) {
	f, err := s.state.OpenFile("stage.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrDestination
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 || info.Sys().(*syscall.Stat_t).Nlink != 1 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		return nil, ErrDestination
	}
	var record stageRecord
	decoder := json.NewDecoder(io.LimitReader(f, 16385))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF || record.Version != 1 || record.Root != s.dir || record.Inode != s.inode {
		return nil, ErrDestination
	}
	return &record, nil
}

func syncRoot(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (s *Stager) temporary(data []byte, executable bool) (string, error) {
	name := "stage-temp-" + uuid.NewString()
	f, err := s.state.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if err == nil && executable {
		err = f.Chmod(0700)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		s.state.Remove(name)
		return "", err
	}
	return name, nil
}

func (s *Stager) save(record stageRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > 16384 {
		return ErrLimit
	}
	name, err := s.temporary(data, false)
	if err != nil {
		return err
	}
	defer s.state.Remove(name)
	if err = s.state.Rename(name, "stage.json"); err != nil {
		return err
	}
	return syncRoot(s.state)
}

func (s *Stager) cleanTemporary() error {
	dir, err := s.state.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	for count := 0; ; {
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, entry := range entries {
			count++
			if count > 1024 {
				return ErrLimit
			}
			id, ok := strings.CutPrefix(entry.Name(), "stage-temp-")
			if !ok {
				continue
			}
			if _, err := uuid.Parse(id); err != nil {
				return ErrDestination
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 {
				return ErrDestination
			}
			if err := s.state.Remove(entry.Name()); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return syncRoot(s.state)
		}
	}
}

// Changed or unexpected staged/unstaged/untracked files are never discarded.
func (s *Stager) changedPaths(ctx context.Context, m Manifest, resume bool) error {
	staged, err := git(ctx, s.dir, 8<<20, "diff", "--no-ext-diff", "--no-textconv", "--cached", "--name-only", "--no-renames", "-z", "--")
	if err != nil || len(staged) != 0 {
		return ErrDestination
	}
	allowed := map[string]bool{}
	if resume {
		for _, file := range m.Files {
			allowed[file.Path] = true
		}
	}
	commands := [][]string{{"diff", "--no-ext-diff", "--no-textconv", "--name-only", "--no-renames", "-z", "HEAD", "--"}, {"ls-files", "--others", "--exclude-standard", "-z"}, {"ls-files", "--others", "--ignored", "--exclude-standard", "-z"}}
	for _, args := range commands {
		data, err := git(ctx, s.dir, 8<<20, args...)
		if err != nil {
			return err
		}
		for _, name := range bytes.Split(data, []byte{0}) {
			if len(name) > 0 && !allowed[string(name)] {
				return ErrDestination
			}
		}
	}
	return nil
}

func (s *Stager) verifyBase(ctx context.Context, m Manifest) error {
	data, err := git(ctx, s.dir, 8<<20, "ls-tree", "-rlz", "--full-tree", m.BaseCommit)
	if err != nil {
		return err
	}
	entries := map[string]treeFile{}
	for _, line := range bytes.Split(data, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		meta, name, ok := bytes.Cut(line, []byte{'\t'})
		fields := strings.Fields(string(meta))
		if !ok || len(fields) != 4 {
			return ErrRepository
		}
		entries[string(name)] = treeFile{mode: fields[0], oid: fields[2]}
	}
	for _, file := range m.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		base, exists := entries[file.Path]
		if !exists {
			if file.BeforeSHA256 != "" {
				return ErrDestination
			}
			continue
		}
		if file.BeforeSHA256 == "" || !oneOfFileMode(base.mode) || file.BeforeExecutable != (base.mode == "100755") {
			return ErrDestination
		}
		body, err := git(ctx, s.dir, MaxFileBytes, "cat-file", "blob", base.oid)
		if err != nil {
			return err
		}
		if hash(body) != file.BeforeSHA256 || secretPattern.Match(body) {
			return ErrDestination
		}
	}
	return nil
}

func oneOfFileMode(mode string) bool { return mode == "100644" || mode == "100755" }

// Open each directory without following links and reject case aliases on both
// case-sensitive and case-insensitive filesystems.
func (s *Stager) parent(ctx context.Context, name string, create bool) (*os.Root, string, error) {
	parts := strings.Split(name, "/")
	root, err := s.root.OpenRoot(".")
	if err != nil {
		return nil, "", err
	}
	for i, part := range parts {
		if err := ctx.Err(); err != nil {
			root.Close()
			return nil, "", err
		}
		dir, err := root.Open(".")
		if err != nil {
			root.Close()
			return nil, "", err
		}
		for count := 0; ; {
			if err := ctx.Err(); err != nil {
				dir.Close()
				root.Close()
				return nil, "", err
			}
			entries, err := dir.ReadDir(128)
			if err != nil && !errors.Is(err, io.EOF) {
				dir.Close()
				root.Close()
				return nil, "", err
			}
			for _, entry := range entries {
				count++
				if count > 20000 {
					dir.Close()
					root.Close()
					return nil, "", ErrLimit
				}
				if entry.Name() != part && strings.EqualFold(entry.Name(), part) {
					dir.Close()
					root.Close()
					return nil, "", ErrDestination
				}
			}
			if errors.Is(err, io.EOF) {
				break
			}
		}
		dir.Close()
		if i == len(parts)-1 {
			return root, part, nil
		}
		info, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) && create {
			if err = root.Mkdir(part, 0700); err == nil {
				err = syncRoot(root)
			}
			if err == nil {
				info, err = root.Lstat(part)
			}
		}
		if err != nil {
			root.Close()
			return nil, "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, "", ErrDestination
		}
		next, err := root.OpenRoot(part)
		root.Close()
		if err != nil {
			return nil, "", err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, "", ErrDestination
		}
		root = next
	}
	root.Close()
	return nil, "", ErrDestination
}

func (s *Stager) fileState(ctx context.Context, file File, resume bool) (bool, error) {
	parent, _, err := s.parent(ctx, file.Path, false)
	if parent != nil {
		parent.Close()
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, ErrDestination
	}
	data, executable, reason, err := readFile(s.root, file.Path)
	if errors.Is(err, os.ErrNotExist) {
		if resume && file.Deleted {
			return true, nil
		}
		if file.BeforeSHA256 != "" {
			return false, ErrDestination
		}
		return false, nil
	}
	if err != nil || reason != "" {
		return false, ErrDestination
	}
	actual := hash(data)
	if resume && !file.Deleted && actual == file.SHA256 && executable == file.Executable {
		return true, nil
	}
	if actual != file.BeforeSHA256 || executable != file.BeforeExecutable {
		return false, ErrDestination
	}
	return false, nil
}

func (s *Stager) applyFile(ctx context.Context, file File) error {
	done, err := s.fileState(ctx, file, true)
	if err != nil || done {
		return err
	}
	parent, name, err := s.parent(ctx, file.Path, !file.Deleted)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err = ctx.Err(); err != nil {
		return err
	}
	if file.Deleted {
		if err = parent.Remove(name); err != nil {
			return err
		}
		return syncRoot(parent)
	}
	temporary, err := s.temporary(file.Data, file.Executable)
	if err != nil {
		return err
	}
	defer s.state.Remove(temporary)
	from, err := s.state.Open(".")
	if err != nil {
		return err
	}
	defer from.Close()
	to, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer to.Close()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = unix.Renameat(int(from.Fd()), temporary, int(to.Fd()), name); err != nil {
		return err
	}
	return errors.Join(syncRoot(parent), syncRoot(s.state))
}

// Apply accepts a clean destination once, then resumes that same reviewed
// payload after interruption. Conflicting bytes are retained and reported;
// partial successful file writes remain journaled for retry, never rolled back
// over newer work. Every file is verified again before a receipt is returned.
func (s *Stager) Apply(ctx context.Context, m Manifest, reviewed, branch string) (StageReceipt, error) {
	var empty StageReceipt
	if !s.mu.TryLock() {
		return empty, ErrStageBusy
	}
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	digest, err := m.Digest()
	if err != nil {
		return empty, err
	}
	if reviewed != digest {
		return empty, ErrInput
	}
	if err = s.verifyRoot(ctx, m.BaseCommit, branch); err != nil {
		return empty, err
	}
	receipt := StageReceipt{ReviewSHA256: reviewed, BaseCommit: m.BaseCommit, Branch: branch, Files: len(m.Files)}
	record, err := s.load()
	if err != nil {
		return empty, err
	}
	resume := record != nil
	if resume && record.Receipt != receipt {
		return empty, ErrDestination
	}
	if err = s.cleanTemporary(); err != nil {
		return empty, err
	}
	if err = s.verifyBase(ctx, m); err != nil {
		return empty, err
	}
	if err = s.changedPaths(ctx, m, resume); err != nil {
		return empty, err
	}
	for _, file := range m.Files {
		done, err := s.fileState(ctx, file, resume)
		if err != nil {
			return empty, err
		}
		if resume && record.Ready && !done {
			return empty, ErrDestination
		}
	}
	if !resume {
		record = &stageRecord{Version: 1, Root: s.dir, Inode: s.inode, Receipt: receipt}
		if err = s.save(*record); err != nil {
			return empty, err
		}
	}
	for _, file := range m.Files {
		if err = s.applyFile(ctx, file); err != nil {
			return empty, err
		}
	}
	if err = s.verifyRoot(ctx, m.BaseCommit, branch); err != nil {
		return empty, err
	}
	if err = s.changedPaths(ctx, m, true); err != nil {
		return empty, err
	}
	for _, file := range m.Files {
		done, err := s.fileState(ctx, file, true)
		if err != nil {
			return empty, err
		}
		if !done {
			return empty, ErrDestination
		}
	}
	if !record.Ready {
		record.Ready = true
		if err = s.save(*record); err != nil {
			return empty, err
		}
	}
	return receipt, nil
}
