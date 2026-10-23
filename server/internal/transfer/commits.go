package transfer

import (
	"bytes"
	"context"
	"crypto/sha1" // Git identity; the review also binds every object's SHA-256.
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/gitcheckout"
)

const MaxTransferCommits = 256
const MaxCommitObjects = 4096

// CommitArchive carries unpublished objects above a remotely verified ancestor.
// It contains no refs, repository configuration, hooks or credentials. Before
// import, the destination must independently fetch and authorize BaseCommit.
type CommitArchive struct {
	Version    int            `json:"version"`
	BaseCommit string         `json:"base_commit"`
	HeadCommit string         `json:"head_commit"`
	Objects    []CommitObject `json:"objects"`
}
type CommitObject struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Data []byte `json:"data"`
}

func gitObjectID(kind string, data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s %d\x00", kind, len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
func objectEdges(o CommitObject) ([]string, error) {
	var edges []string
	switch o.Kind {
	case "blob":
		return nil, nil
	case "commit":
		header, _, ok := bytes.Cut(o.Data, []byte("\n\n"))
		if !ok {
			return nil, ErrInput
		}
		lines := strings.Split(string(header), "\n")
		if len(lines) < 3 || !strings.HasPrefix(lines[0], "tree ") || !commitPattern.MatchString(strings.TrimPrefix(lines[0], "tree ")) {
			return nil, ErrInput
		}
		edges = append(edges, strings.TrimPrefix(lines[0], "tree "))
		for _, line := range lines[1:] {
			if strings.HasPrefix(line, "parent ") {
				id := strings.TrimPrefix(line, "parent ")
				if !commitPattern.MatchString(id) {
					return nil, ErrInput
				}
				edges = append(edges, id)
			}
		}
	case "tree":
		remaining := o.Data
		for len(remaining) > 0 {
			header, rest, ok := bytes.Cut(remaining, []byte{0})
			if !ok || len(rest) < 20 {
				return nil, ErrInput
			}
			mode, name, ok := strings.Cut(string(header), " ")
			if !ok || !safePath(name) || strings.Contains(name, "/") {
				return nil, ErrInput
			}
			switch mode {
			case "40000", "100644", "100755", "120000":
			default:
				return nil, ErrInput
			}
			edges = append(edges, hex.EncodeToString(rest[:20]))
			remaining = rest[20:]
		}
	default:
		return nil, ErrInput
	}
	return edges, nil
}

// Validate checks bounded bytes, identities and reachability of supplied objects.
// Missing references still need verification against the authorized destination
// base. A valid archive by itself does not authorize a checkout or execution.
func (a CommitArchive) Validate() error {
	if a.Version != 1 || !commitPattern.MatchString(a.BaseCommit) || !commitPattern.MatchString(a.HeadCommit) || a.BaseCommit == a.HeadCommit || len(a.Objects) == 0 || len(a.Objects) > MaxCommitObjects {
		return ErrInput
	}
	objects := map[string]CommitObject{}
	total, commits := 0, 0
	for _, o := range a.Objects {
		if len(o.Data) > MaxFileBytes {
			return ErrLimit
		}
		total += len(o.Data)
		if total > MaxTotalBytes {
			return ErrLimit
		}
		if !commitPattern.MatchString(o.ID) || o.ID == a.BaseCommit || gitObjectID(o.Kind, o.Data) != o.ID || secretPattern.Match(o.Data) {
			return ErrInput
		}
		if _, exists := objects[o.ID]; exists {
			return ErrInput
		}
		if _, err := objectEdges(o); err != nil {
			return err
		}
		if o.Kind == "commit" {
			commits++
		}
		objects[o.ID] = o
	}
	if commits == 0 || commits > MaxTransferCommits {
		return ErrLimit
	}
	if objects[a.HeadCommit].Kind != "commit" {
		return ErrInput
	}
	visited := map[string]bool{}
	pending := []string{a.HeadCommit}
	for len(pending) > 0 {
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if visited[id] {
			continue
		}
		o, exists := objects[id]
		if !exists {
			continue
		}
		visited[id] = true
		edges, _ := objectEdges(o)
		pending = append(pending, edges...)
	}
	if len(visited) != len(objects) {
		return ErrInput
	}
	return nil
}
func (a CommitArchive) Digest() (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	type summary struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	values := make([]summary, 0, len(a.Objects))
	for _, o := range a.Objects {
		values = append(values, summary{o.ID, o.Kind, len(o.Data), hash(o.Data)})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	raw, err := json.Marshal(struct {
		Version int       `json:"version"`
		Base    string    `json:"base"`
		Head    string    `json:"head"`
		Objects []summary `json:"objects"`
	}{a.Version, a.BaseCommit, a.HeadCommit, values})
	if err != nil {
		return "", ErrInput
	}
	return hash(append([]byte("capybara-commit-archive-v1\n"), raw...)), nil
}
func DecodeCommitArchive(raw []byte) (CommitArchive, error) {
	var a CommitArchive
	if len(raw) > MaxWireBytes {
		return a, ErrLimit
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&a) != nil || dec.Decode(new(any)) != io.EOF {
		return CommitArchive{}, ErrInput
	}
	if err := a.Validate(); err != nil {
		return CommitArchive{}, err
	}
	return a, nil
}
func commitObjectIDs(ctx context.Context, dir, base, head string) ([]string, error) {
	if _, err := git(ctx, dir, 1024, "merge-base", "--is-ancestor", base, head); err != nil {
		return nil, err
	}
	commits, err := git(ctx, dir, MaxTransferCommits*42+128, "rev-list", "--max-count=257", head, "^"+base)
	if err != nil {
		return nil, err
	}
	if len(strings.Fields(string(commits))) > MaxTransferCommits {
		return nil, ErrLimit
	}
	raw, err := git(ctx, dir, MaxCommitObjects*42+128, "rev-list", "--objects", "--no-object-names", head, "^"+base)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(raw))
	if len(ids) == 0 || len(ids) > MaxCommitObjects {
		return nil, ErrLimit
	}
	for _, id := range ids {
		if !commitPattern.MatchString(id) {
			return nil, ErrInput
		}
	}
	sort.Strings(ids)
	return ids, nil
}
func readCommitObjects(ctx context.Context, dir string, ids []string) ([]CommitObject, error) {
	raw, err := gitInput(ctx, dir, MaxTotalBytes+MaxCommitObjects*100, []byte(strings.Join(ids, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	objects := make([]CommitObject, 0, len(ids))
	for _, id := range ids {
		header, rest, ok := bytes.Cut(raw, []byte{'\n'})
		if !ok {
			return nil, ErrInput
		}
		fields := strings.Fields(string(header))
		if len(fields) != 3 || fields[0] != id {
			return nil, ErrInput
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 || size > MaxFileBytes {
			return nil, ErrLimit
		}
		if len(rest) < size+1 || rest[size] != '\n' {
			return nil, ErrInput
		}
		objects = append(objects, CommitObject{id, fields[1], bytes.Clone(rest[:size])})
		raw = rest[size+1:]
	}
	if len(raw) != 0 {
		return nil, ErrInput
	}
	return objects, nil
}

// CaptureCommits reads immutable objects. It neither fetches nor publishes refs.
// The caller chooses an ancestor that the destination can independently read.
func CaptureCommits(ctx context.Context, dir, base, head string) (CommitArchive, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if !commitPattern.MatchString(base) || !commitPattern.MatchString(head) || base == head {
		return CommitArchive{}, ErrInput
	}
	if err := verifyRoot(ctx, dir); err != nil {
		return CommitArchive{}, err
	}
	shallow, err := completeCommitGraph(ctx, dir, base)
	if err != nil {
		return CommitArchive{}, err
	}
	current, _, err := revision(ctx, dir)
	if err != nil {
		return CommitArchive{}, err
	}
	if current != head {
		return CommitArchive{}, ErrChanged
	}
	ids, err := commitObjectIDs(ctx, dir, base, head)
	if err != nil {
		return CommitArchive{}, err
	}
	objects, err := readCommitObjects(ctx, dir, ids)
	if err != nil {
		return CommitArchive{}, err
	}
	a := CommitArchive{1, base, head, objects}
	if shallow {
		present := map[string]bool{base: true}
		for _, object := range objects {
			if object.Kind == "commit" {
				present[object.ID] = true
			}
		}
		for _, object := range objects {
			if object.Kind != "commit" {
				continue
			}
			edges, e := objectEdges(object)
			if e != nil {
				return CommitArchive{}, e
			}
			for _, parent := range edges[1:] {
				if !present[parent] {
					return CommitArchive{}, ErrRepository
				}
			}
		}
	}
	if err = a.Validate(); err != nil {
		return CommitArchive{}, err
	}
	if err = verifyCommitTrees(ctx, dir, a); err != nil {
		return CommitArchive{}, err
	}
	current, _, err = revision(ctx, dir)
	if err != nil {
		return CommitArchive{}, err
	}
	if current != head {
		return CommitArchive{}, ErrChanged
	}
	afterShallow, err := completeCommitGraph(ctx, dir, base)
	if err != nil {
		return CommitArchive{}, err
	}
	if afterShallow != shallow {
		return CommitArchive{}, ErrChanged
	}
	return a, nil
}

// Linked worktrees are supported. A shallow source is accepted only at the
// authorized base boundary; its exported parent graph is checked separately.
// Destination verification still requires the complete remote ancestor history.
func completeCommitGraph(ctx context.Context, dir, base string) (bool, error) {
	gitPath := func(name string) (string, error) {
		raw, err := git(ctx, dir, 8192, "rev-parse", "--git-path", name)
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(string(raw))
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return "", ErrRepository
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(dir, value)
		}
		return value, nil
	}
	grafts, err := gitPath("info/grafts")
	if err != nil {
		return false, err
	}
	if _, err = os.Lstat(grafts); !os.IsNotExist(err) {
		return false, ErrRepository
	}
	raw, err := git(ctx, dir, 1024, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(raw)) == "false" {
		return false, nil
	}
	if strings.TrimSpace(string(raw)) != "true" {
		return false, ErrRepository
	}
	boundary, err := gitPath("shallow")
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(boundary, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false, ErrRepository
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 127 {
		return false, ErrRepository
	}
	raw, err = io.ReadAll(io.LimitReader(f, 128))
	ids := strings.Fields(string(raw))
	if err != nil || len(raw) >= 128 || len(ids) != 1 || ids[0] != base {
		return false, ErrRepository
	}
	return true, nil
}

func verifyCommitTrees(ctx context.Context, dir string, a CommitArchive) error {
	base, err := git(ctx, dir, 8<<20, "ls-tree", "-rlz", "--full-tree", a.BaseCommit)
	if err != nil {
		return err
	}
	// Unchanged excluded files are already in the authorized base, with no new
	// bytes exported. New versions refuse the archive: stripping them would alter
	// the commit identity and conceal part of its history.
	baseline := map[string]string{}
	for _, record := range bytes.Split(base, []byte{0}) {
		meta, name, ok := strings.Cut(string(record), "\t")
		if ok {
			baseline[name] = meta
		}
	}
	for _, o := range a.Objects {
		if o.Kind != "commit" {
			continue
		}
		tree, err := git(ctx, dir, 8<<20, "ls-tree", "-rlz", "--full-tree", o.ID)
		if err != nil {
			return err
		}
		if err = gitcheckout.ValidateTree(ctx, tree, func(ctx context.Context, id string) ([]byte, error) {
			return git(ctx, dir, 4096, "cat-file", "blob", id)
		}); err != nil {
			return err
		}
		for _, record := range bytes.Split(tree, []byte{0}) {
			if len(record) == 0 {
				continue
			}
			meta, name, ok := strings.Cut(string(record), "\t")
			if !ok || !safePath(name) || excludedPath(name) && baseline[name] != meta {
				return ErrInput
			}
		}
	}
	return nil
}
