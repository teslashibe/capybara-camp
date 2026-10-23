package camp

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// MaxChildHistories bounds every child row a parent has ever published,
// including removed tombstones, because ChildPublication returns all of them.
const MaxChildHistories = 512

var ErrChildCapacity = errors.New("child history limit reached")

type ChildHistoryInput struct {
	SourceKey                  string         `json:"source_key"`
	SourceRevision             string         `json:"source_revision"`
	Name                       string         `json:"name"`
	Phase                      string         `json:"phase"`
	Workflow                   string         `json:"workflow"`
	State                      string         `json:"state"`
	Model                      string         `json:"model"`
	Complete                   bool           `json:"complete"`
	Removed                    bool           `json:"removed"`
	ExpectedParentRevision     int64          `json:"expected_parent_revision"`
	ExpectedTranscriptRevision int64          `json:"expected_transcript_revision"`
	Messages                   []MessageInput `json:"messages"`
}

type ChildHistory struct {
	ID                 string `json:"id"`
	ParentID           string `json:"parent_id"`
	SourceKey          string `json:"source_key"`
	SourceRevision     string `json:"source_revision"`
	Name               string `json:"name"`
	Phase              string `json:"phase"`
	Workflow           string `json:"workflow"`
	State              string `json:"state"`
	Model              string `json:"model"`
	Complete           bool   `json:"complete"`
	Removed            bool   `json:"removed"`
	Revision           int64  `json:"revision"`
	TranscriptRevision int64  `json:"transcript_revision"`
}

type ChildHistoryList struct {
	ParentRevision     int64          `json:"parent_revision"`
	PermissionRevision int64          `json:"permission_revision"`
	Children           []ChildHistory `json:"children"`
}

type ChildTranscript struct {
	ParentRevision     int64        `json:"parent_revision"`
	PermissionRevision int64        `json:"permission_revision"`
	Child              ChildHistory `json:"child"`
	Transcript         Transcript   `json:"transcript"`
}

const childHistoryColumns = `c.session_id,c.parent_session_id,c.source_key,v.source_revision,v.name,v.phase,v.workflow,v.state,v.model,v.complete,v.removed,v.revision,v.transcript_revision`

func scanChildHistory(row rowScanner) (ChildHistory, error) {
	var c ChildHistory
	err := row.Scan(&c.ID, &c.ParentID, &c.SourceKey, &c.SourceRevision, &c.Name, &c.Phase, &c.Workflow, &c.State, &c.Model, &c.Complete, &c.Removed, &c.Revision, &c.TranscriptRevision)
	return c, err
}

func readChildHistory(ctx context.Context, tx pgx.Tx, parentID, id string, revision int64) (ChildHistory, error) {
	c, err := scanChildHistory(tx.QueryRow(ctx, `SELECT `+childHistoryColumns+` FROM session_children c
 CROSS JOIN LATERAL(SELECT * FROM session_child_revisions WHERE parent_session_id=c.parent_session_id AND child_session_id=c.session_id AND revision<=$3 ORDER BY revision DESC LIMIT 1) v
 WHERE c.parent_session_id=$1 AND c.session_id=$2`, parentID, id, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrDenied
	}
	return c, err
}

func digest64(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == value
}

// WriteChildHistory shares the parent's revision clock and permission boundary.
// Child rows reuse the transcript store but are never standalone runnable sessions.
func (s *Store) WriteChildHistory(ctx context.Context, a auth.Actor, campID, parentID, id, command string, in ChildHistoryInput) (ChildHistory, error) {
	in.Name = strings.TrimSpace(in.Name)
	if !validID(id) || id == parentID || !digest64(in.SourceKey) || !digest64(in.SourceRevision) || len(in.Name) < 1 || len(in.Name) > 500 || len(in.Phase) > 128 || len(in.Workflow) > 256 || len(in.Model) > 200 ||
		!oneOf(in.State, "unknown", "running", "done", "failed", "stopped", "cancelled") || in.ExpectedParentRevision < 0 || in.ExpectedTranscriptRevision < 0 || in.Removed && (len(in.Messages) != 0 || !in.Complete) {
		return ChildHistory{}, ErrInput
	}
	if err := validateTranscriptMessages(in.Messages, true); err != nil {
		return ChildHistory{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ChildHistory{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return ChildHistory{}, err
	}
	parent, err := s.readSession(ctx, tx, a, campID, parentID)
	if err != nil {
		return ChildHistory{}, err
	}
	if parent.OwnerUserID != a.UserID || parent.OwnerDeviceID != a.DeviceID {
		return ChildHistory{}, ErrDenied
	}
	if parent.ExecutionLocation != "local" {
		return ChildHistory{}, ErrTransition
	}
	var result ChildHistory
	if hit, err := s.beginCommand(ctx, tx, a, command, "child.transcript:"+parentID+":"+id, in, &result); err != nil {
		return result, err
	} else if hit {
		return result, nil
	}
	if parent.TranscriptRevision != in.ExpectedParentRevision {
		return result, ErrRevision
	}
	old, err := readChildHistory(ctx, tx, parentID, id, parent.TranscriptRevision)
	if errors.Is(err, ErrDenied) {
		if in.ExpectedTranscriptRevision != 0 || in.Removed {
			return result, ErrRevision
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM session_children WHERE parent_session_id=$1`, parentID).Scan(&count); err != nil {
			return result, err
		}
		if count >= MaxChildHistories {
			return result, ErrChildCapacity
		}
		_, err = tx.Exec(ctx, `INSERT INTO sessions(id,camp_id,project_id,profile_id,owner_user_id,owner_device_id,local_session_id,title,model,state,stage,shared,allow_requests)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'idle','Done',false,false)`, id, campID, parent.ProjectID, parent.ProfileID, a.UserID, a.DeviceID, "child:"+parentID+":"+in.SourceKey, in.Name, in.Model)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO session_children(session_id,camp_id,parent_session_id,source_key) VALUES($1,$2,$3,$4)`, id, campID, parentID, in.SourceKey)
		}
		if err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return result, ErrConflict
			}
			return result, err
		}
		old = ChildHistory{ID: id, ParentID: parentID, SourceKey: in.SourceKey}
	} else if err != nil {
		return result, err
	}
	if old.SourceKey != in.SourceKey || old.TranscriptRevision != in.ExpectedTranscriptRevision {
		return result, ErrRevision
	}
	transcriptRevision := old.TranscriptRevision
	if len(in.Messages) > 0 {
		transcriptRevision++
		if err = appendTranscriptMessages(ctx, tx, id, transcriptRevision, in.Messages); err != nil {
			return result, err
		}
	}
	revision := parent.TranscriptRevision + 1
	_, err = tx.Exec(ctx, `INSERT INTO session_child_revisions(parent_session_id,child_session_id,revision,transcript_revision,source_revision,name,phase,workflow,state,model,complete,removed)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, parentID, id, revision, transcriptRevision, in.SourceRevision, in.Name, in.Phase, in.Workflow, in.State, in.Model, in.Complete, in.Removed)
	if err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET transcript_revision=$2 WHERE id=$1`, id, transcriptRevision); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET transcript_revision=$2,updated_at=now() WHERE id=$1`, parentID, revision); err != nil {
		return result, err
	}
	parent.TranscriptRevision = revision
	if err = emitSession(ctx, tx, a, campID, "children.updated", parent, false); err != nil {
		return result, err
	}
	result, err = readChildHistory(ctx, tx, parentID, id, revision)
	if err != nil {
		return result, err
	}
	if err = s.finishCommand(ctx, tx, a, command, result); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) ChildHistories(ctx context.Context, a auth.Actor, campID, parentID string, revision int64) (ChildHistoryList, error) {
	return s.childHistories(ctx, a, campID, parentID, revision, false)
}

// ChildPublication includes tombstones so the source device can reconcile a
// removed child after losing its local journal. Readers use ChildHistories.
func (s *Store) ChildPublication(ctx context.Context, a auth.Actor, campID, parentID string) (ChildHistoryList, error) {
	return s.childHistories(ctx, a, campID, parentID, 0, true)
}

func (s *Store) childHistories(ctx context.Context, a auth.Actor, campID, parentID string, revision int64, publication bool) (ChildHistoryList, error) {
	return retryRead(ctx, func() (ChildHistoryList, error) {
		var out ChildHistoryList
		if revision < 0 {
			return out, ErrInput
		}
		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			return out, err
		}
		defer tx.Rollback(ctx)
		if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
			return out, err
		}
		parent, err := s.readSession(ctx, tx, a, campID, parentID)
		if err != nil {
			return out, err
		}
		if publication && (parent.OwnerUserID != a.UserID || parent.OwnerDeviceID != a.DeviceID) {
			return out, ErrDenied
		}
		at := revision
		if at == 0 {
			at = parent.TranscriptRevision
		}
		if at > parent.TranscriptRevision {
			return out, ErrRevision
		}
		out = ChildHistoryList{ParentRevision: at, PermissionRevision: parent.PermissionRevision, Children: []ChildHistory{}}
		rows, err := tx.Query(ctx, `SELECT `+childHistoryColumns+` FROM session_children c
 CROSS JOIN LATERAL(SELECT * FROM session_child_revisions WHERE parent_session_id=c.parent_session_id AND child_session_id=c.session_id AND revision<=$2 ORDER BY revision DESC LIMIT 1) v
 WHERE c.parent_session_id=$1 AND ($3 OR NOT v.removed) ORDER BY c.session_id`, parentID, at, publication)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			c, e := scanChildHistory(rows)
			if e != nil {
				rows.Close()
				return out, e
			}
			out.Children = append(out.Children, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	})
}

func (s *Store) ChildTranscript(ctx context.Context, a auth.Actor, campID, parentID, id string, revision, before int64, limit int) (ChildTranscript, error) {
	return s.childTranscript(ctx, a, campID, parentID, id, revision, before, limit, "")
}

func (s *Store) ChildMessage(ctx context.Context, a auth.Actor, campID, parentID, id string, revision int64, messageID string) (ChildTranscript, error) {
	if messageID == "" || len(messageID) > 256 || revision < 1 {
		return ChildTranscript{}, ErrInput
	}
	return s.childTranscript(ctx, a, campID, parentID, id, revision, 0, 1, messageID)
}

func (s *Store) childTranscript(ctx context.Context, a auth.Actor, campID, parentID, id string, revision, before int64, limit int, messageID string) (ChildTranscript, error) {
	return retryRead(ctx, func() (ChildTranscript, error) {
		var out ChildTranscript
		if !validID(id) || revision < 0 || before < 0 || limit < 1 || limit > 200 {
			return out, ErrInput
		}
		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			return out, err
		}
		defer tx.Rollback(ctx)
		if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
			return out, err
		}
		parent, err := s.readSession(ctx, tx, a, campID, parentID)
		if err != nil {
			return out, err
		}
		at := revision
		if at == 0 {
			at = parent.TranscriptRevision
		}
		if at > parent.TranscriptRevision {
			return out, ErrRevision
		}
		current, err := readChildHistory(ctx, tx, parentID, id, parent.TranscriptRevision)
		if err != nil {
			return out, err
		}
		if current.Removed {
			return out, ErrDenied
		}
		child, err := readChildHistory(ctx, tx, parentID, id, at)
		if err != nil {
			return out, err
		}
		if !child.Complete || child.Removed {
			return out, ErrTransition
		}
		var transcript Transcript
		if messageID == "" {
			transcript, err = readTranscriptAt(ctx, tx, id, child.TranscriptRevision, before, limit)
		} else {
			var messages []Message
			messages, err = readContextMessagesBudget(ctx, tx, SourceRef{SessionID: id, TranscriptRevision: child.TranscriptRevision, MessageIDs: []string{messageID}}, 1<<20)
			transcript = Transcript{Revision: child.TranscriptRevision, Messages: messages}
		}
		if err != nil {
			return out, err
		}
		out = ChildTranscript{ParentRevision: at, PermissionRevision: parent.PermissionRevision, Child: child, Transcript: transcript}
		return out, tx.Commit(ctx)
	})
}
