package localcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

const maxInstalledHistory = 32 << 20

// This is a new continuation identity, never recreated provider-native state.
// ProfileID is the server profile; SourceID identifies its local provider home.
type TransferInstallTarget struct {
	ProfileID string `json:"profileID"`
	SourceID  string `json:"sourceID"`
	Provider  string `json:"provider"`
	Root      string `json:"root"`
	SessionID string `json:"sessionID"`
	Purpose   string `json:"purpose"`
}
type TransferInstallInput struct {
	TransferStageInput
	Export        TransferInstallExport `json:"export"`
	Target        TransferInstallTarget `json:"target"`
	History       []byte                `json:"history"`
	HistorySHA256 string                `json:"historySHA256"`
}

// The native caller obtains this through an authenticated, revision-pinned
// export read. Matching it here prevents accidental cross-session assembly;
// this local receipt is not a substitute for server authorization.
type TransferInstallExport struct {
	ID                 string    `json:"id"`
	CampID             string    `json:"camp_id"`
	SessionID          string    `json:"session_id"`
	OwnerID            string    `json:"owner_user_id"`
	DeviceID           string    `json:"device_id"`
	WorkspaceID        string    `json:"workspace_id"`
	WorkspaceRevision  int64     `json:"workspace_revision"`
	Epoch              int64     `json:"source_epoch"`
	Revision           int64     `json:"source_revision"`
	TranscriptRevision int64     `json:"transcript_revision"`
	PermissionRevision int64     `json:"permission_revision"`
	Kind               string    `json:"kind"`
	Paths              []string  `json:"paths"`
	State              string    `json:"state"`
	SHA256             string    `json:"result_sha256"`
	Bytes              int       `json:"result_bytes"`
	ExpiresAt          time.Time `json:"expires_at"`
}
type TransferInstallReceipt struct {
	Version            int                   `json:"version"`
	Scope              string                `json:"scope"`
	TransferID         string                `json:"transferID"`
	ExportID           string                `json:"exportID"`
	Destination        string                `json:"destination"`
	Target             TransferInstallTarget `json:"target"`
	PayloadSHA256      string                `json:"payloadSHA256"`
	HistorySHA256      string                `json:"historySHA256"`
	SourceID           string                `json:"sourceID"`
	TranscriptRevision int64                 `json:"transcriptRevision"`
	Files              transfer.StageReceipt `json:"files"`
}
type TransferInstallResult struct {
	Receipt       TransferInstallReceipt `json:"receipt"`
	ReceiptSHA256 string                 `json:"receiptSHA256"`
}

// Preserve exact export bytes, including all display metadata. The projection
// below validates the identity and graph without rewriting the source archive.
type installedHistory struct {
	Version int    `json:"version"`
	Origin  string `json:"origin"`
	Session struct {
		ID                 string `json:"id"`
		CampID             string `json:"camp_id"`
		OwnerID            string `json:"owner_user_id"`
		DeviceID           string `json:"owner_device_id"`
		ProfileID          string `json:"profile_id"`
		ProjectID          string `json:"project_id"`
		Provider           string `json:"provider"`
		Location           string `json:"execution_location"`
		WorkspaceID        string `json:"execution_workspace_id"`
		Epoch              int64  `json:"execution_epoch"`
		Revision           int64  `json:"revision"`
		TranscriptRevision int64  `json:"transcript_revision"`
		PermissionRevision int64  `json:"permission_revision"`
		HistoryOnly        bool   `json:"history_only"`
	} `json:"session"`
	Conversations []struct {
		ID    string `json:"id"`
		Child *struct {
			ID                 string `json:"id"`
			ParentID           string `json:"parent_id"`
			SourceKey          string `json:"source_key"`
			SourceRevision     string `json:"source_revision"`
			Revision           int64  `json:"revision"`
			TranscriptRevision int64  `json:"transcript_revision"`
			Complete           bool   `json:"complete"`
			Removed            bool   `json:"removed"`
		} `json:"child"`
		Messages []struct {
			ID       string `json:"id"`
			Ordinal  int64  `json:"ordinal"`
			Role     string `json:"role"`
			Text     string `json:"text"`
			Model    string `json:"model"`
			Effort   string `json:"effort"`
			Speed    string `json:"speed"`
			Removed  bool   `json:"removed"`
			Revision int64  `json:"revision"`
		} `json:"messages"`
	} `json:"conversations"`
}

func installHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func installUUID(s string) bool {
	id, e := uuid.Parse(s)
	return e == nil && id != uuid.Nil && id.String() == s
}
func installDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

func validateInstalledHistory(in TransferInstallInput) (installedHistory, error) {
	var h installedHistory
	if len(in.History) > maxInstalledHistory {
		return h, transfer.ErrLimit
	}
	if !utf8.Valid(in.History) || installHash(in.History) != in.HistorySHA256 || json.Unmarshal(in.History, &h) != nil {
		return h, transfer.ErrInput
	}
	s := h.Session
	u, err := url.Parse(h.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return h, transfer.ErrInput
	}
	if h.Version != 1 || s.Location != "cloud" || s.HistoryOnly || s.Epoch <= 0 || s.Revision <= 0 || s.TranscriptRevision < 0 || s.PermissionRevision <= 0 || (s.Provider != "Codex" && s.Provider != "Claude") || s.Provider != in.Target.Provider {
		return h, transfer.ErrInput
	}
	for _, id := range []string{s.ID, s.CampID, s.OwnerID, s.DeviceID, s.ProfileID, s.WorkspaceID} {
		if !installUUID(id) {
			return h, transfer.ErrInput
		}
	}
	if s.ProjectID != "" && !installUUID(s.ProjectID) {
		return h, transfer.ErrInput
	}
	if installHash([]byte(h.Origin+"\n"+s.OwnerID+"\n"+s.CampID)) != in.Scope || len(h.Conversations) == 0 || len(h.Conversations) > 513 {
		return h, transfer.ErrInput
	}
	seen, keys := map[string]bool{}, map[string]bool{}
	for index, c := range h.Conversations {
		if !installUUID(c.ID) || seen[c.ID] || len(c.Messages) > 2000 {
			return h, transfer.ErrInput
		}
		seen[c.ID] = true
		revision := s.TranscriptRevision
		if index == 0 {
			if c.ID != s.ID || c.Child != nil {
				return h, transfer.ErrInput
			}
		} else {
			child := c.Child
			if child == nil || child.ID != c.ID || child.ParentID != s.ID || !child.Complete || child.Removed || child.Revision <= 0 || child.Revision > s.TranscriptRevision || child.TranscriptRevision < 0 || !installDigest(child.SourceKey) || !installDigest(child.SourceRevision) || keys[child.SourceKey] {
				return h, transfer.ErrInput
			}
			keys[child.SourceKey] = true
			revision = child.TranscriptRevision
		}
		ids := map[string]bool{}
		previous := int64(-1)
		size := 0
		for _, m := range c.Messages {
			if m.ID == "" || len(m.ID) > 256 || ids[m.ID] || m.Ordinal <= previous || m.Removed || m.Revision <= 0 || m.Revision > revision || len(m.Text) > 1<<20 || len(m.Model) > 200 || len(m.Effort) > 32 || len(m.Speed) > 32 || (m.Role != "user" && m.Role != "assistant" && m.Role != "system" && m.Role != "tool") {
				return h, transfer.ErrInput
			}
			ids[m.ID] = true
			previous = m.Ordinal
			size += len(m.Text)
		}
		if size > 8<<20 {
			return h, transfer.ErrLimit
		}
	}
	return h, nil
}

func (a *App) validateInstallTarget(t TransferInstallTarget, destination string) error {
	if !installUUID(t.ProfileID) || !installUUID(t.SessionID) || (t.Purpose != "planning" && t.Purpose != "implementation") || !filepath.IsAbs(t.Root) || filepath.Clean(t.Root) != t.Root || len(t.Root) > 4096 {
		return transfer.ErrInput
	}
	sources, _, err := a.sources()
	if err != nil {
		return err
	}
	found := false
	for _, s := range sources {
		if s.ID == t.SourceID && s.Root == t.Root && s.Provider == t.Provider {
			found = true
			break
		}
	}
	if !found {
		return transfer.ErrDestination
	}
	for _, p := range []string{destination, a.Config.Support} {
		canonical, err := filepath.EvalSymlinks(p)
		if err != nil && !os.IsNotExist(err) {
			return transfer.ErrDestination
		}
		if canonical == "" {
			canonical = p
		}
		for _, pair := range [][2]string{{t.Root, canonical}, {canonical, t.Root}} {
			rel, err := filepath.Rel(pair[0], pair[1])
			if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
				return transfer.ErrDestination
			}
		}
	}
	return nil
}

// Installation prepares app-owned history and reviewed files. It never modifies
// a provider profile, publishes a session or authorizes execution.
func (a *App) installTransfer(ctx context.Context, input io.Reader) (any, error) {
	const maximum = (transfer.MaxWireBytes+maxInstalledHistory+4)/3*4 + 32768
	raw, err := io.ReadAll(io.LimitReader(input, maximum+1))
	if err != nil || len(raw) > maximum {
		return nil, transfer.ErrLimit
	}
	var in TransferInstallInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, transfer.ErrInput
	}
	manifest, err := validateStageInput(in.TransferStageInput)
	if err != nil {
		return nil, err
	}
	if manifest.History != nil {
		return nil, transfer.ErrInput
	}
	h, err := validateInstalledHistory(in)
	if err != nil {
		return nil, err
	}
	e, s := in.Export, h.Session
	if !installUUID(e.ID) || !installUUID(e.DeviceID) || e.WorkspaceRevision <= 0 ||
		e.CampID != s.CampID || e.OwnerID != s.OwnerID || e.SessionID != s.ID || e.WorkspaceID != s.WorkspaceID ||
		e.Epoch != s.Epoch || e.Revision != s.Revision || e.TranscriptRevision != s.TranscriptRevision || e.PermissionRevision != s.PermissionRevision ||
		e.Kind != "capture" || e.State != "ready" || e.SHA256 != in.PayloadSHA256 || e.Bytes != len(in.Payload) || !e.ExpiresAt.After(time.Now()) || len(e.Paths) > transfer.MaxFiles {
		return nil, transfer.ErrChanged
	}
	paths := map[string]bool{}
	for _, path := range e.Paths {
		if paths[path] {
			return nil, transfer.ErrInput
		}
		paths[path] = true
	}
	if len(paths) != len(manifest.Files)+len(manifest.Excluded) {
		return nil, transfer.ErrInput
	}
	for _, file := range manifest.Files {
		if !paths[file.Path] {
			return nil, transfer.ErrInput
		}
	}
	for _, file := range manifest.Excluded {
		if !paths[file.Path] {
			return nil, transfer.ErrInput
		}
	}
	if err = a.validateInstallTarget(in.Target, in.Destination); err != nil {
		return nil, err
	}
	stager, path, err := a.openTransferStager(ctx, in.TransferStageInput)
	if err != nil {
		return nil, err
	}
	defer stager.Close()
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = cleanInstallTemps(root); err != nil {
		return nil, err
	}
	receipt := TransferInstallReceipt{Version: 1, Scope: in.Scope, TransferID: in.TransferID, ExportID: e.ID, Destination: in.Destination, Target: in.Target, PayloadSHA256: in.PayloadSHA256, HistorySHA256: in.HistorySHA256, SourceID: h.Session.ID, TranscriptRevision: h.Session.TranscriptRevision, Files: transfer.StageReceipt{ReviewSHA256: in.ReviewSHA256, BaseCommit: manifest.BaseCommit, Branch: in.Branch, Files: len(manifest.Files)}}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	completed, err := readInstallFile(root, "installation.json", 32768)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if completed != nil {
		if !bytes.Equal(completed, encoded) {
			return nil, transfer.ErrChanged
		}
		saved, e := readInstallFile(root, "history.json", maxInstalledHistory)
		if e != nil || !bytes.Equal(saved, in.History) {
			return nil, transfer.ErrChanged
		}
		intent, e := readInstallFile(root, "intent.json", 32768)
		if e != nil || !bytes.Equal(intent, encoded) {
			return nil, transfer.ErrChanged
		}
	}
	if err = bindInstallFile(root, "intent.json", encoded, 32768); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = bindInstallFile(root, "history.json", in.History, maxInstalledHistory); err != nil {
		return nil, err
	}
	staged, err := stager.Apply(ctx, manifest, in.ReviewSHA256, in.Branch)
	if err != nil {
		return nil, err
	}
	if staged != receipt.Files {
		return nil, transfer.ErrChanged
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = bindInstallFile(root, "installation.json", encoded, 32768); err != nil {
		return nil, err
	}
	return TransferInstallResult{Receipt: receipt, ReceiptSHA256: installHash(encoded)}, nil
}

func readInstallFile(root *os.Root, name string, limit int) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || st.Nlink != 1 || int(st.Uid) != os.Geteuid() || info.Size() > int64(limit) {
		return nil, transfer.ErrDestination
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if len(raw) > limit {
		return nil, transfer.ErrLimit
	}
	return raw, err
}

// Caller holds the stager's checkout and state locks throughout. Only our own
// temporary is removed; failed attempts leave other journals and work intact.
func bindInstallFile(root *os.Root, name string, data []byte, limit int) error {
	if len(data) > limit {
		return transfer.ErrLimit
	}
	old, err := readInstallFile(root, name, limit)
	if err == nil {
		if !bytes.Equal(old, data) {
			return transfer.ErrChanged
		}
		return syncInstallDirectory(root)
	}
	if !os.IsNotExist(err) {
		return err
	}
	temp := ".install-" + uuid.NewString()
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = f.Write(data)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err = root.Rename(temp, name); err != nil {
		return err
	}
	return syncInstallDirectory(root)
}

func syncInstallDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// A killed helper cannot run its deferred removal. Recover only this installer's
// private temporaries under the same state lock, without touching other files.
func cleanInstallTemps(root *os.Root) error {
	dir, err := root.Open(".")
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
				return transfer.ErrLimit
			}
			id, ok := strings.CutPrefix(entry.Name(), ".install-")
			if !ok {
				continue
			}
			if !installUUID(id) {
				return transfer.ErrDestination
			}
			info, e := root.Lstat(entry.Name())
			if e != nil {
				return e
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || st.Nlink != 1 || int(st.Uid) != os.Geteuid() {
				return transfer.ErrDestination
			}
			if e = root.Remove(entry.Name()); e != nil {
				return e
			}
		}
		if errors.Is(err, io.EOF) {
			return syncInstallDirectory(root)
		}
	}
}
