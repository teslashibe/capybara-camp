package camp

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func runnerHistorySources(ctx context.Context, tx pgx.Tx, parent Session, revision int64) ([]localcli.HistorySource, error) {
	root := localcli.HistorySource{ID: "camp:" + parent.ID, Title: parent.Title, Revision: strconv.FormatInt(revision, 10)}
	out := []localcli.HistorySource{root}
	rows, err := tx.Query(ctx, `SELECT c.session_id,v.name FROM session_children c
 CROSS JOIN LATERAL(SELECT name,complete,removed FROM session_child_revisions WHERE parent_session_id=c.parent_session_id AND child_session_id=c.session_id AND revision<=$2 ORDER BY revision DESC LIMIT 1) v
 CROSS JOIN LATERAL(SELECT removed FROM session_child_revisions WHERE parent_session_id=c.parent_session_id AND child_session_id=c.session_id ORDER BY revision DESC LIMIT 1) latest
 WHERE c.parent_session_id=$1 AND v.complete AND NOT v.removed AND NOT latest.removed ORDER BY c.session_id LIMIT 513`, parent.ID, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, title string
		if err = rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out = append(out, localcli.HistorySource{ID: root.ID + "/child:" + id, Title: title, Revision: root.Revision, ParentID: root.ID})
		if len(out) > 513 {
			return nil, ErrInput
		}
	}
	return out, rows.Err()
}

// RunnerHistory returns the original conversation at the request's pinned
// revision. It shares execution authorization with turns and controls. A model
// cannot use this operation to discover sessions or send work.
func (g *GitStore) RunnerHistory(ctx context.Context, token, workspaceID, requestID string, in localcli.HistoryRead) (localcli.HistoryPage, error) {
	var out localcli.HistoryPage
	if in.Offset < 0 || in.TextOffset < 0 || in.TextOffset > 8<<20 || in.Limit < 1 || in.Limit > 120 {
		return out, ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	r, err := g.lockedRunnerRequest(ctx, tx, token, workspaceID, requestID)
	if err != nil {
		return out, err
	}
	live, err := hasLiveLease(ctx, tx, requestID)
	if err != nil {
		return out, err
	}
	if !live || !oneOf(r.State, "claimed", "started") || r.ContextRevision < 1 {
		return out, ErrTransition
	}
	if _, err = g.store.requestAccess(ctx, tx, r); err != nil {
		return out, err
	}
	session, err := g.store.readSession(ctx, tx, auth.Actor{UserID: r.OwnerUserID}, r.CampID, r.SessionID)
	if err != nil {
		return out, err
	}
	storageID, storageRevision, title := r.SessionID, r.ContextRevision, session.Title
	if in.SessionID != "camp:"+r.SessionID {
		childID, ok := strings.CutPrefix(in.SessionID, "camp:"+r.SessionID+"/child:")
		if !ok || !validID(childID) {
			return out, ErrDenied
		}
		current, e := readChildHistory(ctx, tx, r.SessionID, childID, session.TranscriptRevision)
		if e != nil {
			return out, e
		}
		if current.Removed {
			return out, ErrDenied
		}
		child, e := readChildHistory(ctx, tx, r.SessionID, childID, r.ContextRevision)
		if e != nil {
			return out, e
		}
		if child.Removed || !child.Complete {
			return out, ErrDenied
		}
		storageID, storageRevision, title = child.ID, child.TranscriptRevision, child.Name
	}
	out, err = readHistoryPage(ctx, tx, storageID, storageRevision, in, title, strconv.FormatInt(r.ContextRevision, 10))
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func readHistoryPage(ctx context.Context, tx pgx.Tx, storageID string, storageRevision int64, in localcli.HistoryRead, title, revision string) (localcli.HistoryPage, error) {
	out := localcli.HistoryPage{SessionID: in.SessionID, Title: title, Revision: revision, Messages: []localcli.HistoryMessage{}}
	// Limit each SQL value as well as the resulting page. Byte offsets are
	// independent of database locale; returned chunks end on UTF-8 boundaries.
	rows, err := tx.Query(ctx, `SELECT m.local_id,m.ordinal,v.role,
 substring(convert_to(v.text,'UTF8') FROM (CASE WHEN m.ordinal=$3 THEN $4 ELSE 0 END)+1 FOR 16388),
 octet_length(convert_to(v.text,'UTF8'))
 FROM messages m CROSS JOIN LATERAL (
 SELECT role,text,removed FROM message_revisions WHERE session_id=m.session_id AND local_id=m.local_id AND revision<=$2 ORDER BY revision DESC LIMIT 1
 ) v WHERE m.session_id=$1 AND m.ordinal >= $3 AND NOT v.removed ORDER BY m.ordinal LIMIT $5`, storageID, storageRevision, in.Offset, in.TextOffset, min(in.Limit, 20)+1)
	if err != nil {
		return out, err
	}
	total := 0
	for rows.Next() {
		var m localcli.HistoryMessage
		var raw []byte
		var length int
		if err = rows.Scan(&m.ID, &m.Ordinal, &m.Role, &raw, &length); err != nil {
			rows.Close()
			return out, err
		}
		if m.Ordinal == in.Offset {
			m.TextOffset = in.TextOffset
		} else if len(out.Messages) == 0 && in.TextOffset != 0 {
			rows.Close()
			return out, ErrCursor
		}
		if m.TextOffset > length {
			rows.Close()
			return out, ErrCursor
		}
		if len(raw) > 0 && !utf8.RuneStart(raw[0]) {
			rows.Close()
			return out, ErrCursor
		}
		n := min(len(raw), 16384)
		for n < len(raw) && !utf8.RuneStart(raw[n]) {
			n--
		}
		if !utf8.Valid(raw[:n]) {
			rows.Close()
			return out, ErrInput
		}
		m.Text = string(raw[:n])
		if len(out.Messages) >= min(in.Limit, 20) || total+len(m.Text) > 48<<10 {
			next := m.Ordinal
			out.NextOffset = &next
			break
		}
		m.Complete = m.TextOffset+len(m.Text) >= length
		total += len(m.Text)
		out.Messages = append(out.Messages, m)
		if !m.Complete {
			next := m.Ordinal
			out.NextOffset = &next
			out.NextTextOffset = m.TextOffset + len(m.Text)
			break
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Messages) == 0 && in.TextOffset != 0 {
		return out, ErrCursor
	}
	return out, nil
}
