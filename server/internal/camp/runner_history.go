package camp

import (
	"context"
	"strconv"
	"unicode/utf8"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

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
	if in.SessionID != "camp:"+r.SessionID {
		return out, ErrDenied
	}
	if _, err = g.store.requestAccess(ctx, tx, r); err != nil {
		return out, err
	}
	session, err := g.store.readSession(ctx, tx, auth.Actor{UserID: r.OwnerUserID}, r.CampID, r.SessionID)
	if err != nil {
		return out, err
	}
	out = localcli.HistoryPage{SessionID: in.SessionID, Title: session.Title, Revision: strconv.FormatInt(r.ContextRevision, 10), Messages: []localcli.HistoryMessage{}}
	// Limit each SQL value as well as the resulting page. Byte offsets are
	// independent of database locale; returned chunks end on UTF-8 boundaries.
	rows, err := tx.Query(ctx, `SELECT m.local_id,m.ordinal,v.role,
 substring(convert_to(v.text,'UTF8') FROM (CASE WHEN m.ordinal=$3 THEN $4 ELSE 0 END)+1 FOR 16388),
 octet_length(convert_to(v.text,'UTF8'))
 FROM messages m CROSS JOIN LATERAL (
 SELECT role,text,removed FROM message_revisions WHERE session_id=m.session_id AND local_id=m.local_id AND revision<=$2 ORDER BY revision DESC LIMIT 1
 ) v WHERE m.session_id=$1 AND m.ordinal >= $3 AND NOT v.removed ORDER BY m.ordinal LIMIT $5`, r.SessionID, r.ContextRevision, in.Offset, in.TextOffset, min(in.Limit, 20)+1)
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
	return out, tx.Commit(ctx)
}
