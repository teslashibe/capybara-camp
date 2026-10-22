package localcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

type CaptureFile struct {
	Path         string `json:"path"`
	Bytes        int    `json:"bytes"`
	Deleted      bool   `json:"deleted"`
	BeforeSHA256 string `json:"beforeSha256,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
}
type TransferCapture struct {
	Payload       []byte                   `json:"payload"`
	PayloadSHA256 string                   `json:"payloadSha256"`
	ReviewSHA256  string                   `json:"reviewSha256"`
	BaseCommit    string                   `json:"baseCommit"`
	Branch        string                   `json:"branch"`
	Files         []CaptureFile            `json:"files"`
	Excluded      []transfer.Exclusion     `json:"excluded"`
	History       *transfer.HistoryBinding `json:"history,omitempty"`
}

func (a *App) reviewTransfer(ctx context.Context, action string, input io.Reader) (any, error) {
	raw, err := io.ReadAll(io.LimitReader(input, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, transfer.ErrInput
	}
	var in struct {
		Session Session                  `json:"session"`
		Paths   []string                 `json:"paths"`
		History *transfer.HistoryBinding `json:"history"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, transfer.ErrInput
	}
	s := in.Session
	if (s.Provider != "Codex" && s.Provider != "Claude") || s.IsSubagent || !validSessionID(s.SessionID) || !filepath.IsAbs(s.CWD) {
		return nil, errors.New("Choose a local parent session to transfer")
	}
	root, err := transfer.RepositoryRoot(ctx, s.CWD)
	if err != nil {
		return nil, err
	}
	if action == "transfer-changes" {
		return transfer.Changes(ctx, root)
	}
	if in.History != nil {
		if err = a.verifyHistoryBinding(ctx, s, in.History); err != nil {
			return nil, err
		}
	}
	manifest, err := transfer.Snapshot(ctx, root, in.Paths)
	if err != nil {
		return nil, err
	}
	if in.History != nil {
		if err = a.verifyHistoryBinding(ctx, s, in.History); err != nil {
			return nil, err
		}
		manifest.History = in.History
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	if len(payload) > transfer.MaxWireBytes {
		return nil, transfer.ErrLimit
	}
	digest, err := manifest.Digest()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	out := TransferCapture{Payload: payload, PayloadSHA256: hex.EncodeToString(sum[:]), ReviewSHA256: digest, BaseCommit: manifest.BaseCommit, Branch: manifest.Branch, Files: []CaptureFile{}, Excluded: manifest.Excluded}
	out.History = manifest.History
	for _, file := range manifest.Files {
		out.Files = append(out.Files, CaptureFile{file.Path, len(file.Data), file.Deleted, file.BeforeSHA256, file.SHA256})
	}
	return out, nil
}
