// Package gitcheckout owns the Git transport and tree checks shared by local
// transfer preparation and isolated cloud checkouts. It has no account store.
package gitcheckout

import (
	"bytes"
	"context"
	"errors"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var ErrUnsafe = errors.New("Checkout contains unsupported or unsafe paths")
var ErrSize = errors.New("Checkout exceeds its size limit")
var ErrGit = errors.New("Git checkout did not complete")
var ErrState = errors.New("Checkout state is unavailable or unsafe")
var objectID = regexp.MustCompile(`^[a-f0-9]{40}$`)
var repoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)

func ValidCommit(value string) bool { return objectID.MatchString(value) }
func ValidRepository(value string) bool {
	parts := strings.Split(value, "/")
	return repoName.MatchString(value) && len(parts) == 2 && parts[1] != "." && parts[1] != ".."
}
func ValidateTree(ctx context.Context, data []byte, blob func(context.Context, string) ([]byte, error)) error {
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
			return ErrSize
		}
		meta, name, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 || !safeTreePath(name) || seen[strings.ToLower(name)] {
			return ErrUnsafe
		}
		seen[strings.ToLower(name)] = true
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 {
			return ErrUnsafe
		}
		if size > 32<<20 {
			return ErrSize
		}
		total += size
		if total > 512<<20 {
			return ErrSize
		}
		if fields[1] != "blob" || !objectID.MatchString(fields[2]) {
			return ErrUnsafe
		}
		switch fields[0] {
		case "100644", "100755":
		case "120000":
			if size > 4096 {
				return ErrUnsafe
			}
			target, err := blob(ctx, fields[2])
			if err != nil {
				return err
			}
			value := string(target)
			if value == "" || path.IsAbs(value) || strings.Contains(value, "\\") || strings.ContainsFunc(value, unicode.IsControl) {
				return ErrUnsafe
			}
			links[name] = value
		default:
			return ErrUnsafe
		}
	}
	for name := range links {
		if !safeSymlink(name, links) {
			return ErrUnsafe
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
