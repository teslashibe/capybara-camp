package localcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxHistoryChildren = 512

var errChildHistory = errors.New("child history changed or is unavailable; review the parent again")

// ChildHistorySource identifies a child inside one parent/profile. Filesystem
// paths stay local; a caller cannot substitute a transcript path for this ID.
type ChildHistorySource struct {
	ID       string `json:"id"`
	LocalID  string `json:"localID"`
	Name     string `json:"name"`
	Phase    string `json:"phase"`
	Workflow string `json:"workflow"`
	Status   string `json:"status"`
	Model    string `json:"model"`
	Revision string `json:"revision"`
}

type ChildHistoryCatalog struct {
	ParentID string               `json:"parentID"`
	Revision string               `json:"revision"`
	Sources  []ChildHistorySource `json:"sources"`
}

type ChildHistoryPage struct {
	ParentID string             `json:"parentID"`
	Source   ChildHistorySource `json:"source"`
	Page     ExportPage         `json:"page"`
}

func childHistoryParent(parent Session) (Session, error) {
	if validSession(parent) != nil || parent.IsSubagent || !filepath.IsAbs(parent.Root) || len(parent.SessionID) > 256 {
		return Session{}, errChildHistory
	}
	root, err := filepath.EvalSymlinks(parent.Root)
	if err != nil {
		return Session{}, errChildHistory
	}
	parent.Root = root
	if parent.Provider == "Claude" {
		path, err := filepath.EvalSymlinks(parent.Path)
		if err != nil || !under(path, root) || path == root {
			return Session{}, errChildHistory
		}
		parent.Path = path
	}
	return parent, nil
}

func childHistoryKey(parent Session, childID string) string {
	return hash([]byte(parent.Provider + "\x00" + parent.Root + "\x00" + parent.SessionID + "\x00" + childID))
}

func childHistorySession(ctx context.Context, parent Session, child Child) (Session, error) {
	session := parent
	session.ID = childHistoryKey(parent, child.ID)
	session.SessionID = child.SessionID
	session.IsSubagent = true
	session.Path = child.Path
	if child.SessionID == "" || len(child.ID) > 512 || len(child.SessionID) > 256 {
		return Session{}, errChildHistory
	}
	// Current Codex stores can retain the full child after rotating its rollout.
	// Bind to that exact thread's database history instead of a stale file path.
	if parent.Provider == "Codex" && isFile(filepath.Join(parent.Root, "thread_history_1.sqlite")) {
		db, err := openReadDB(filepath.Join(parent.Root, "thread_history_1.sqlite"))
		if err != nil {
			return Session{}, errChildHistory
		}
		var found bool
		err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM thread_items WHERE thread_id=? AND item_type IN ('userMessage','agentMessage'))`, child.SessionID).Scan(&found)
		db.Close()
		if err != nil {
			return Session{}, errChildHistory
		}
		if found {
			session.Path = ""
			return session, nil
		}
	}
	if session.Path != "" {
		if !filepath.IsAbs(session.Path) {
			session.Path = filepath.Join(parent.Root, session.Path)
		}
		info, err := os.Lstat(session.Path)
		if err != nil || !info.Mode().IsRegular() {
			return Session{}, errChildHistory
		}
		path, err := filepath.EvalSymlinks(session.Path)
		boundary := parent.Root
		if parent.Provider == "Claude" {
			boundary = strings.TrimSuffix(parent.Path, filepath.Ext(parent.Path)) + string(filepath.Separator) + "subagents"
		}
		if err != nil || !under(path, boundary) || path == boundary {
			return Session{}, errChildHistory
		}
		session.Path = path
		if parent.Provider == "Codex" {
			matched := false
			err := readLinesN(ctx, path, true, 1, func(_ int, row map[string]any) error {
				matched = str(row["type"]) == "session_meta" && str(object(row["payload"])["id"]) == child.SessionID
				return nil
			})
			if err != nil || !matched {
				return Session{}, errChildHistory
			}
		}
	} else if parent.Provider != "Codex" {
		return Session{}, errChildHistory
	}
	return session, nil
}

func childHistorySource(ctx context.Context, parent Session, child Child) (ChildHistorySource, Session, error) {
	session, err := childHistorySession(ctx, parent, child)
	if err != nil {
		return ChildHistorySource{}, Session{}, err
	}
	revision, err := fingerprint(session)
	if err != nil || len(child.Name) > 4096 || len(child.Phase) > 1024 || len(child.Workflow) > 1024 || len(child.Model) > 512 {
		return ChildHistorySource{}, Session{}, errChildHistory
	}
	return ChildHistorySource{ID: session.ID, LocalID: child.ID, Name: child.Name, Phase: child.Phase,
		Workflow: child.Workflow, Status: child.Status, Model: child.Model, Revision: revision}, session, nil
}

func (a *App) ChildHistory(ctx context.Context, parent Session) (ChildHistoryCatalog, error) {
	parent, err := childHistoryParent(parent)
	if err != nil {
		return ChildHistoryCatalog{}, err
	}
	children, err := childrenForLimit(ctx, parent, false, maxHistoryChildren)
	if err != nil || len(children) > maxHistoryChildren {
		return ChildHistoryCatalog{}, errChildHistory
	}
	result := ChildHistoryCatalog{ParentID: childHistoryKey(parent, ""), Sources: []ChildHistorySource{}}
	seen := map[string]bool{}
	for _, child := range children {
		source, _, err := a.indexedChildSource(ctx, parent, child)
		if err != nil || seen[source.ID] {
			return ChildHistoryCatalog{}, errChildHistory
		}
		seen[source.ID] = true
		result.Sources = append(result.Sources, source)
	}
	sort.Slice(result.Sources, func(i, j int) bool { return result.Sources[i].ID < result.Sources[j].ID })
	raw, err := json.Marshal(result.Sources)
	if err != nil {
		return ChildHistoryCatalog{}, err
	}
	result.Revision = hash(append([]byte(result.ParentID+"\n"), raw...))
	return result, nil
}

func (a *App) indexedChildSource(ctx context.Context, parent Session, child Child) (ChildHistorySource, Session, error) {
	source, session, err := childHistorySource(ctx, parent, child)
	if err != nil {
		return source, session, err
	}
	page, err := a.ExportPage(ctx, session, -1, 1)
	if err != nil {
		return source, session, err
	}
	source.Revision = page.ContentRevision
	return source, session, nil
}

// resolveHistoryChild accepts an opaque parent/profile-bound source, never an
// arbitrary child path supplied by the caller.
func (a *App) resolveHistoryChild(ctx context.Context, parent Session, sourceID, revision string) (ChildHistorySource, Session, error) {
	if len(sourceID) != 64 || len(revision) != 64 {
		return ChildHistorySource{}, Session{}, errChildHistory
	}
	children, err := childrenForLimit(ctx, parent, false, maxHistoryChildren)
	if err != nil || len(children) > maxHistoryChildren {
		return ChildHistorySource{}, Session{}, errChildHistory
	}
	for _, child := range children {
		if childHistoryKey(parent, child.ID) != sourceID {
			continue
		}
		source, session, err := a.indexedChildSource(ctx, parent, child)
		if err != nil || source.Revision != revision {
			return ChildHistorySource{}, Session{}, errChildHistory
		}
		return source, session, nil
	}
	return ChildHistorySource{}, Session{}, errChildHistory
}

func (a *App) ExportChildHistory(ctx context.Context, parent Session, sourceID, revision string, after int64, limit int) (ChildHistoryPage, error) {
	if after < -1 || limit < 1 || limit > 200 {
		return ChildHistoryPage{}, errChildHistory
	}
	parent, err := childHistoryParent(parent)
	if err != nil {
		return ChildHistoryPage{}, err
	}
	source, session, err := a.resolveHistoryChild(ctx, parent, sourceID, revision)
	if err != nil {
		return ChildHistoryPage{}, err
	}
	page, err := a.ExportPage(ctx, session, after, limit)
	if err != nil {
		return ChildHistoryPage{}, err
	}
	if page.ContentRevision != revision {
		return ChildHistoryPage{}, errChildHistory
	}
	return ChildHistoryPage{ParentID: childHistoryKey(parent, ""), Source: source, Page: page}, nil
}

func (a *App) ChildTranscriptAttachment(ctx context.Context, parent Session, sourceID, revision string, request AttachmentRead) (AttachmentBody, error) {
	parent, err := childHistoryParent(parent)
	if err != nil {
		return AttachmentBody{}, err
	}
	_, session, err := a.resolveHistoryChild(ctx, parent, sourceID, revision)
	if err != nil {
		return AttachmentBody{}, err
	}
	request.Session = session
	body, err := a.TranscriptAttachment(ctx, request)
	if err != nil {
		return AttachmentBody{}, err
	}
	// Re-resolve membership and content after reading, including child removal.
	if _, _, err = a.resolveHistoryChild(ctx, parent, sourceID, revision); err != nil {
		return AttachmentBody{}, err
	}
	return body, nil
}

func (a *App) childHistoryRequest(ctx context.Context, action string, input io.Reader) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := io.ReadAll(io.LimitReader(input, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errChildHistory
	}
	var in struct {
		Session      Session `json:"session"`
		SourceID     string  `json:"sourceID"`
		Revision     string  `json:"revision"`
		After        int64   `json:"after"`
		Limit        int     `json:"limit"`
		MessageID    string  `json:"messageID"`
		AttachmentID string  `json:"attachmentID"`
		SourceDigest string  `json:"sourceDigest"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errChildHistory
	}
	if action == "child-history-catalog" {
		return a.ChildHistory(ctx, in.Session)
	}
	if action == "child-transcript-attachment" {
		return a.ChildTranscriptAttachment(ctx, in.Session, in.SourceID, in.Revision, AttachmentRead{MessageID: in.MessageID, AttachmentID: in.AttachmentID, SourceDigest: in.SourceDigest})
	}
	return a.ExportChildHistory(ctx, in.Session, in.SourceID, in.Revision, in.After, in.Limit)
}
