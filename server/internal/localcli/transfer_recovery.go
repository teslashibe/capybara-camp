package localcli

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type TransferFence struct {
	Key       string `json:"key"`
	HandoffID string `json:"handoffID"`
}

// Inventory exposes opaque identities only. Active helpers retain their locks;
// malformed markers remain blocked and are never automatically removed.
func (a *App) transferFences(ctx context.Context) ([]TransferFence, error) {
	out := []TransferFence{}
	directory, err := os.Open(a.Config.Support)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || !ownedByUser(info) || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("Local transfer state is not private")
	}
	names, err := directory.Readdirnames(10001)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) > 10000 {
		return nil, errors.New("Local transfer inventory is too large")
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := strings.TrimSuffix(strings.TrimPrefix(name, "run-"), ".lock")
		decoded, err := hex.DecodeString(key)
		if !strings.HasPrefix(name, "run-") || !strings.HasSuffix(name, ".lock") || err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != key {
			continue
		}
		file, err := os.OpenFile(filepath.Join(a.Config.Support, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			continue
		}
		stat, e := file.Stat()
		if e != nil || !stat.Mode().IsRegular() || !ownedByUser(stat) || stat.Mode().Perm()&0077 != 0 || stat.Size() == 0 {
			file.Close()
			continue
		}
		if syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil {
			file.Close()
			continue
		}
		id, e := readTransferFence(file)
		file.Close()
		if e != nil || id == "" {
			continue
		}
		out = append(out, TransferFence{Key: key, HandoffID: id})
		if len(out) > 512 {
			return nil, errors.New("Too many local transfers need recovery")
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
