package transfer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sort"
	"strings"
)

type Change struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Excluded string `json:"excluded,omitempty"`
}
type ChangeSet struct {
	BaseCommit string   `json:"baseCommit"`
	Branch     string   `json:"branch"`
	Files      []Change `json:"files"`
}

// Changes lists names and status only. File contents are read after selection.
func Changes(ctx context.Context, dir string) (ChangeSet, error) {
	if err := verifyRoot(ctx, dir); err != nil {
		return ChangeSet{}, err
	}
	head, branch, err := revision(ctx, dir)
	if err != nil {
		return ChangeSet{}, err
	}
	raw, err := git(ctx, dir, 8<<20, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return ChangeSet{}, err
	}
	out := ChangeSet{BaseCommit: head, Branch: branch, Files: []Change{}}
	paths := map[string]string{}
	records := bytes.Split(raw, []byte{0})
	for i := 0; i < len(records); i++ {
		row := records[i]
		if len(row) == 0 {
			continue
		}
		if len(row) < 4 || row[2] != ' ' {
			return ChangeSet{}, ErrRepository
		}
		code, name := string(row[:2]), string(row[3:])
		if strings.Contains(code, "U") || code == "AA" || code == "DD" {
			return ChangeSet{}, ErrChanged
		}
		kind := "modified"
		if code == "??" || strings.Contains(code, "A") {
			kind = "added"
		}
		if strings.Contains(code, "D") {
			kind = "deleted"
		}
		if strings.ContainsAny(code, "RC") {
			i++
			if i >= len(records) || len(records[i]) == 0 {
				return ChangeSet{}, ErrRepository
			}
			if strings.Contains(code, "R") {
				paths[string(records[i])] = "deleted"
			}
			kind = "added"
		}
		paths[name] = kind
		if len(paths) > 4096 {
			return ChangeSet{}, ErrLimit
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ChangeSet{}, ErrRepository
	}
	defer root.Close()
	for name, kind := range paths {
		item := Change{Path: name, Kind: kind}
		switch {
		case !safePath(name):
			item.Excluded = "unsupported path"
		case excludedPath(name):
			item.Excluded = "credentials or provider data"
		default:
			st, e := root.Lstat(name)
			if e == nil && (!st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0) {
				item.Excluded = "unsupported file"
			}
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return ChangeSet{}, e
			}
		}
		out.Files = append(out.Files, item)
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	latest, latestBranch, err := revision(ctx, dir)
	if err != nil {
		return ChangeSet{}, err
	}
	if latest != head || latestBranch != branch {
		return ChangeSet{}, ErrChanged
	}
	return out, nil
}

// Snapshot represents an explicit selection, including a deliberate empty set.
func Snapshot(ctx context.Context, dir string, selected []string) (Manifest, error) {
	if selected == nil {
		return Manifest{}, ErrInput
	}
	if len(selected) > 0 {
		return Capture(ctx, dir, selected)
	}
	if err := verifyRoot(ctx, dir); err != nil {
		return Manifest{}, err
	}
	head, branch, err := revision(ctx, dir)
	if err != nil {
		return Manifest{}, err
	}
	out := Manifest{Version: 1, BaseCommit: head, Branch: branch, Files: []File{}, Excluded: []Exclusion{}}
	if err = VerifySource(ctx, dir, out); err != nil {
		return Manifest{}, err
	}
	return out, nil
}
