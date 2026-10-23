package transfer

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1" // Standard Git pack checksum, not an authorization primitive.
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CommitReceipt proves object verification only. It does not update a branch,
// stage project files, transfer execution ownership or start a provider process.
type CommitReceipt struct {
	ReviewSHA256 string `json:"review_sha256"`
	BaseCommit   string `json:"base_commit"`
	HeadCommit   string `json:"head_commit"`
	Objects      int    `json:"objects"`
}

func commitPack(a CommitArchive) ([]byte, error) {
	var pack bytes.Buffer
	pack.WriteString("PACK")
	binary.Write(&pack, binary.BigEndian, uint32(2))
	binary.Write(&pack, binary.BigEndian, uint32(len(a.Objects)))
	objects := append([]CommitObject{}, a.Objects...)
	sort.Slice(objects, func(i, j int) bool { return objects[i].ID < objects[j].ID })
	for _, o := range objects {
		kind := map[string]byte{"commit": 1, "tree": 2, "blob": 3}[o.Kind]
		if kind == 0 {
			return nil, ErrInput
		}
		size := len(o.Data)
		first := kind<<4 | byte(size&15)
		size >>= 4
		if size > 0 {
			first |= 128
		}
		pack.WriteByte(first)
		for size > 0 {
			b := byte(size & 127)
			size >>= 7
			if size > 0 {
				b |= 128
			}
			pack.WriteByte(b)
		}
		compressor := zlib.NewWriter(&pack)
		if _, err := compressor.Write(o.Data); err != nil {
			return nil, err
		}
		if err := compressor.Close(); err != nil {
			return nil, err
		}
		if pack.Len() > MaxWireBytes-20 {
			return nil, ErrLimit
		}
	}
	sum := sha1.Sum(pack.Bytes())
	pack.Write(sum[:])
	return pack.Bytes(), nil
}

// A commit importer is only for a private, clean app-owned checkout. Existing
// worktrees, shallow boundaries and grafts cannot change graph verification.
func commitDestination(ctx context.Context, dir, base string) (string, error) {
	if err := verifyRoot(ctx, dir); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !privateDirectory(info) {
		return "", ErrDestination
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", ErrDestination
	}
	raw, err := git(ctx, dir, 8192, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	metadata := strings.TrimSpace(string(raw))
	resolved, err := filepath.EvalSymlinks(metadata)
	if err != nil || resolved != filepath.Join(canonical, ".git") || strings.ContainsAny(resolved, "\n\r\x00") {
		return "", ErrDestination
	}
	for _, name := range []string{"shallow", "info/grafts", "objects/info/alternates"} {
		if _, err := os.Lstat(filepath.Join(resolved, name)); err == nil || !os.IsNotExist(err) {
			return "", ErrDestination
		}
	}
	objects, err := filepath.EvalSymlinks(filepath.Join(resolved, "objects"))
	if err != nil || objects != filepath.Join(resolved, "objects") {
		return "", ErrDestination
	}
	head, _, err := revision(ctx, dir)
	if err != nil || head != base {
		return "", ErrDestination
	}
	state, err := git(ctx, dir, 8<<20, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if len(state) != 0 {
		return "", ErrDestination
	}
	return objects, nil
}

// RestoreCommits validates in a disposable object database, reading missing
// objects only from the already-authorized base checkout. It then installs the
// exact verified objects without changing HEAD, refs, index or working files.
// The caller must serialize checkout writers throughout preparation/handoff.
func RestoreCommits(ctx context.Context, dir string, a CommitArchive, reviewed string) (CommitReceipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	digest, err := a.Digest()
	if err != nil {
		return CommitReceipt{}, err
	}
	if digest != reviewed {
		return CommitReceipt{}, ErrChanged
	}
	objects, err := commitDestination(ctx, dir, a.BaseCommit)
	if err != nil {
		return CommitReceipt{}, err
	}
	pack, err := commitPack(a)
	if err != nil {
		return CommitReceipt{}, err
	}
	quarantine, err := os.MkdirTemp(filepath.Dir(dir), ".camp-commit-review-")
	if err != nil {
		return CommitReceipt{}, ErrDestination
	}
	defer os.RemoveAll(quarantine)
	review := trusted(ctx)
	if _, err = git(review, quarantine, 1024, "init", "--bare", "--template="); err != nil {
		return CommitReceipt{}, err
	}
	if err = os.WriteFile(filepath.Join(quarantine, "objects/info/alternates"), []byte(objects+"\n"), 0600); err != nil {
		return CommitReceipt{}, ErrDestination
	}
	if _, err = gitInput(review, quarantine, 1024, pack, "index-pack", "--stdin", "--strict"); err != nil {
		return CommitReceipt{}, err
	}
	if _, err = git(review, quarantine, 64<<10, "fsck", "--strict", "--no-reflogs", "--no-dangling", a.HeadCommit); err != nil {
		return CommitReceipt{}, err
	}
	ids, err := commitObjectIDs(review, quarantine, a.BaseCommit, a.HeadCommit)
	if err != nil {
		return CommitReceipt{}, err
	}
	if len(ids) != len(a.Objects) {
		return CommitReceipt{}, ErrInput
	}
	want := make([]string, 0, len(a.Objects))
	for _, o := range a.Objects {
		want = append(want, o.ID)
	}
	sort.Strings(want)
	for i, id := range ids {
		if id != want[i] {
			return CommitReceipt{}, ErrInput
		}
	}
	if err = verifyCommitTrees(review, quarantine, a); err != nil {
		return CommitReceipt{}, err
	}
	if _, err = commitDestination(ctx, dir, a.BaseCommit); err != nil {
		return CommitReceipt{}, err
	}
	if _, err = gitInput(ctx, dir, 1024, pack, "index-pack", "--stdin", "--strict"); err != nil {
		return CommitReceipt{}, err
	}
	if _, err = commitDestination(ctx, dir, a.BaseCommit); err != nil {
		return CommitReceipt{}, err
	}
	return CommitReceipt{digest, a.BaseCommit, a.HeadCommit, len(a.Objects)}, nil
}
