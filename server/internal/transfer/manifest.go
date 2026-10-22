// Package transfer captures explicitly selected workspace changes for review.
// Capturing or decoding a manifest never changes a file or execution assignment.
package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	MaxFiles      = 256
	MaxFileBytes  = 4 << 20
	MaxTotalBytes = 32 << 20
	MaxWireBytes  = 46 << 20
)

var (
	ErrInput      = errors.New("invalid transfer selection")
	ErrLimit      = errors.New("transfer selection exceeds its size limit")
	ErrChanged    = errors.New("workspace changed; capture and review it again")
	ErrRepository = errors.New("a readable Git workspace with a pinned commit is required")
	digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
	secretPattern = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----|\b(?:sk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{24,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|xox[baprs]-[A-Za-z0-9-]{24,}|AKIA[A-Z0-9]{16})\b`)
)

type File struct {
	Path             string `json:"path"`
	BeforeSHA256     string `json:"before_sha256,omitempty"`
	BeforeExecutable bool   `json:"before_executable,omitempty"`
	SHA256           string `json:"sha256,omitempty"`
	Executable       bool   `json:"executable,omitempty"`
	Deleted          bool   `json:"deleted,omitempty"`
	Data             []byte `json:"data,omitempty"`
}

type Exclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Manifest struct {
	Version    int         `json:"version"`
	BaseCommit string      `json:"base_commit"`
	Branch     string      `json:"branch,omitempty"`
	Files      []File      `json:"files"`
	Excluded   []Exclusion `json:"excluded"`
}

func hash(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func safePath(value string) bool {
	if value == "" || len(value) > 1024 || !utf8.ValidString(value) || !norm.NFC.IsNormalString(value) || path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, `\:`) || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func excludedPath(value string) bool {
	for _, part := range strings.Split(strings.ToLower(value), "/") {
		if strings.HasPrefix(part, ".env") {
			return true
		}
		switch part {
		case ".codex", ".claude", ".ssh", ".aws", ".azure", "gcloud", ".gnupg", ".kube", ".docker", ".netrc", ".npmrc", ".pypirc", ".git-credentials", ".pgpass", "auth.json", "credentials", "credentials.json", ".credentials.json", "credentials.toml", "secrets.json", "secrets.yaml", "secrets.yml", "secrets.toml", "id_rsa", "id_ed25519":
			return true
		}
	}
	switch strings.ToLower(path.Ext(value)) {
	case ".pem", ".key", ".p12", ".pfx", ".keystore":
		return true
	}
	return false
}

func (m Manifest) Validate() error {
	if m.Version != 1 || !commitPattern.MatchString(m.BaseCommit) || len(m.Branch) > 512 || strings.ContainsFunc(m.Branch, unicode.IsControl) || len(m.Files)+len(m.Excluded) > MaxFiles {
		return ErrInput
	}
	seen := []string{}
	checkPath := func(name string) bool {
		if !safePath(name) {
			return false
		}
		for _, old := range seen {
			left, right := strings.Split(name, "/"), strings.Split(old, "/")
			common := 0
			for common < len(left) && common < len(right) && strings.EqualFold(left[common], right[common]) {
				if left[common] != right[common] {
					return false
				}
				common++
			}
			if common == len(left) || common == len(right) {
				return false
			}
		}
		seen = append(seen, name)
		return true
	}
	total := 0
	for _, f := range m.Files {
		if !checkPath(f.Path) || excludedPath(f.Path) || f.BeforeSHA256 != "" && !digestPattern.MatchString(f.BeforeSHA256) || f.BeforeSHA256 == "" && f.BeforeExecutable {
			return ErrInput
		}
		if f.Deleted {
			if f.BeforeSHA256 == "" || len(f.Data) != 0 || f.SHA256 != "" || f.Executable {
				return ErrInput
			}
		} else {
			if len(f.Data) > MaxFileBytes {
				return ErrLimit
			}
			if !digestPattern.MatchString(f.SHA256) || hash(f.Data) != f.SHA256 || secretPattern.Match(f.Data) {
				return ErrInput
			}
			if f.BeforeSHA256 == f.SHA256 && f.BeforeExecutable == f.Executable {
				return ErrInput
			}
			total += len(f.Data)
		}
	}
	if total > MaxTotalBytes {
		return ErrLimit
	}
	for _, e := range m.Excluded {
		if !checkPath(e.Path) {
			return ErrInput
		}
		switch e.Reason {
		case "policy", "symlink", "hardlink", "unsupported", "unchanged":
		default:
			return ErrInput
		}
	}
	return nil
}

// Digest binds the reviewed paths, content, executable bits and exclusions.
// File bodies are represented by verified hashes so the result is independent
// of JSON formatting and never depends on a local filesystem path or timestamp.
func (m Manifest) Digest() (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	copy := m
	copy.Files = append([]File{}, m.Files...)
	copy.Excluded = append([]Exclusion{}, m.Excluded...)
	for i := range copy.Files {
		copy.Files[i].Data = nil
	}
	sort.Slice(copy.Files, func(i, j int) bool { return copy.Files[i].Path < copy.Files[j].Path })
	sort.Slice(copy.Excluded, func(i, j int) bool { return copy.Excluded[i].Path < copy.Excluded[j].Path })
	data, err := json.Marshal(copy)
	if err != nil {
		return "", ErrInput
	}
	return hash(append([]byte("capybara-transfer-v1\n"), data...)), nil
}

func Decode(data []byte) (Manifest, error) {
	var m Manifest
	if len(data) > MaxWireBytes {
		return m, ErrLimit
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF {
		return Manifest{}, ErrInput
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
