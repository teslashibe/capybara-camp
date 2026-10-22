package camp

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

type ProfileInput struct {
	LocalID  string `json:"local_id"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
}
type Profile struct {
	ID            string `json:"id"`
	OwnerUserID   string `json:"owner_user_id"`
	OwnerDeviceID string `json:"owner_device_id"`
	Provider      string `json:"provider"`
	LocalID       string `json:"local_id"`
	Label         string `json:"label"`
}
type SessionInput struct {
	ID               string `json:"id"`
	ProjectID        string `json:"project_id"`
	ProfileID        string `json:"profile_id"`
	LocalSessionID   string `json:"local_session_id"`
	Title            string `json:"title"`
	Model            string `json:"model"`
	State            string `json:"state"`
	Stage            string `json:"stage"`
	Shared           bool   `json:"shared"`
	AllowRequests    bool   `json:"allow_requests"`
	ExternalOwner    bool   `json:"external_owner"`
	HistoryOnly      bool   `json:"history_only,omitempty"`
	ExpectedRevision int64  `json:"expected_revision"`
}
type Session struct {
	ID                   string    `json:"id"`
	CampID               string    `json:"camp_id"`
	ProjectID            string    `json:"project_id"`
	ProfileID            string    `json:"profile_id"`
	Provider             string    `json:"provider"`
	ProfileLabel         string    `json:"profile_label"`
	OwnerUserID          string    `json:"owner_user_id"`
	OwnerDeviceID        string    `json:"owner_device_id"`
	LocalSessionID       string    `json:"local_session_id"`
	Title                string    `json:"title"`
	Model                string    `json:"model"`
	State                string    `json:"state"`
	Stage                string    `json:"stage"`
	Shared               bool      `json:"shared"`
	AllowRequests        bool      `json:"allow_requests"`
	ExternalOwner        bool      `json:"external_owner"`
	Revision             int64     `json:"revision"`
	TranscriptRevision   int64     `json:"transcript_revision"`
	PermissionRevision   int64     `json:"permission_revision"`
	UpdatedAt            time.Time `json:"updated_at"`
	ExecutionLocation    string    `json:"execution_location"`
	ExecutionEpoch       int64     `json:"execution_epoch"`
	ExecutionWorkspaceID string    `json:"execution_workspace_id,omitempty"`
	ActivityRevision     int64     `json:"activity_revision"`
	HistoryOnly          bool      `json:"history_only,omitempty"`
}
type MessageInput struct {
	ID      string `json:"id"`
	Ordinal int64  `json:"ordinal"`
	Role    string `json:"role"`
	Text    string `json:"text"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Speed   string `json:"speed,omitempty"`
	Removed bool   `json:"removed"`
}
type Message struct {
	MessageInput
	Revision int64 `json:"revision"`
}
type TranscriptInput struct {
	ExpectedRevision int64          `json:"expected_revision"`
	Messages         []MessageInput `json:"messages"`
}
type Transcript struct {
	Revision int64     `json:"revision"`
	Messages []Message `json:"messages"`
	Before   int64     `json:"before"`
	HasMore  bool      `json:"has_more"`
}

var ErrRevision = errors.New("the record changed; reload before saving")

func (s *Store) RegisterProfile(ctx context.Context, a auth.Actor, requestID string, input ProfileInput) (Profile, error) {
	input.Label = strings.TrimSpace(input.Label)
	hash, err := hex.DecodeString(input.LocalID)
	if err != nil || len(hash) != 32 || len(input.Label) == 0 || len(input.Label) > 120 || (input.Provider != "Codex" && input.Provider != "Claude") {
		return Profile{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Profile{}, err
	}
	defer tx.Rollback(ctx)
	p := Profile{}
	if hit, err := s.beginCommand(ctx, tx, a, requestID, "profile.register:"+a.DeviceID, input, &p); err != nil {
		return p, err
	} else if hit {
		return p, nil
	}
	p = Profile{ID: uuid.NewString(), OwnerUserID: a.UserID, OwnerDeviceID: a.DeviceID, LocalID: input.LocalID, Provider: input.Provider, Label: input.Label}
	err = tx.QueryRow(ctx, `INSERT INTO profiles(id,owner_user_id,owner_device_id,provider,local_id,label) VALUES($1,$2,$3,$4,$5,$6)
	 ON CONFLICT(owner_device_id,local_id) DO UPDATE SET label=EXCLUDED.label WHERE profiles.provider=EXCLUDED.provider RETURNING id`, p.ID, a.UserID, a.DeviceID, p.Provider, p.LocalID, p.Label).Scan(&p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrConflict
	}
	if err != nil {
		return p, err
	}
	if err = s.finishCommand(ctx, tx, a, requestID, p); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

const sessionColumns = `s.id,s.camp_id,s.project_id,s.profile_id,p.provider,p.label,s.owner_user_id,s.owner_device_id,s.local_session_id,
 s.title,s.model,s.state,s.stage,s.shared,s.allow_requests,s.external_owner,s.revision,s.transcript_revision,s.permission_revision,s.updated_at,s.execution_location,s.execution_epoch,COALESCE(s.execution_workspace_id::text,''),s.activity_revision,s.history_only`

type rowScanner interface{ Scan(...any) error }

func scanSession(row rowScanner, extra ...any) (Session, error) {
	var s Session
	columns := []any{&s.ID, &s.CampID, &s.ProjectID, &s.ProfileID, &s.Provider, &s.ProfileLabel, &s.OwnerUserID, &s.OwnerDeviceID, &s.LocalSessionID, &s.Title, &s.Model, &s.State, &s.Stage, &s.Shared, &s.AllowRequests, &s.ExternalOwner, &s.Revision, &s.TranscriptRevision, &s.PermissionRevision, &s.UpdatedAt, &s.ExecutionLocation, &s.ExecutionEpoch, &s.ExecutionWorkspaceID, &s.ActivityRevision, &s.HistoryOnly}
	err := row.Scan(append(columns, extra...)...)
	return s, err
}
func (s *Store) readSession(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, a auth.Actor, campID, id string) (Session, error) {
	if !validID(campID) || !validID(id) {
		return Session{}, ErrDenied
	}
	value, err := scanSession(q.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions s JOIN profiles p ON p.id=s.profile_id
	 JOIN memberships m ON m.camp_id=s.camp_id AND m.user_id=$1 WHERE s.camp_id=$2 AND s.id=$3 AND (s.owner_user_id=$1 OR s.shared)
 AND NOT EXISTS(SELECT 1 FROM session_children child WHERE child.session_id=s.id)`, a.UserID, campID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrDenied
	}
	return value, err
}
func (s *Store) Session(ctx context.Context, a auth.Actor, campID, id string) (Session, error) {
	return s.readSession(ctx, s.pool, a, campID, id)
}

// Native clients publish only explicitly selected sessions. A private initial
// row can fence a delayed first share after the user withdraws it offline.
func (s *Store) PublishSession(ctx context.Context, a auth.Actor, campID, requestID string, input SessionInput) (Session, error) {
	input.Title = strings.TrimSpace(input.Title)
	if !validID(input.ID) || !validID(input.ProjectID) || !validID(input.ProfileID) || len(input.Title) == 0 || len(input.Title) > 500 || len(input.Model) > 200 || input.ExpectedRevision < 0 || len(input.LocalSessionID) == 0 || len(input.LocalSessionID) > 128 || strings.ContainsAny(input.LocalSessionID, "/\\") {
		return Session{}, ErrInput
	}
	if !oneOf(input.State, "idle", "running", "waiting", "done", "unknown") || !oneOf(input.Stage, "Ready", "In progress", "Needs you", "Done") {
		return Session{}, ErrInput
	}
	if input.HistoryOnly && (input.AllowRequests || input.ExternalOwner || !oneOf(input.State, "idle", "done", "unknown") || input.Stage == "In progress") {
		return Session{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return Session{}, err
	}
	var owned bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM profiles WHERE id=$1 AND owner_user_id=$2 AND owner_device_id=$3) AND EXISTS(SELECT 1 FROM projects WHERE id=$4 AND camp_id=$5 AND (NOT archived OR EXISTS(SELECT 1 FROM sessions WHERE id=$6 AND camp_id=$5 AND project_id=$4)))`, input.ProfileID, a.UserID, a.DeviceID, input.ProjectID, campID, input.ID).Scan(&owned)
	if err != nil {
		return Session{}, err
	}
	if !owned {
		return Session{}, ErrDenied
	}
	result := Session{}
	if hit, err := s.beginCommand(ctx, tx, a, requestID, "session.publish:"+campID, input, &result); err != nil {
		return result, err
	} else if hit {
		return result, nil
	}
	previous, err := s.readSession(ctx, tx, a, campID, input.ID)
	isNew := errors.Is(err, ErrDenied)
	if err != nil && !isNew {
		return result, err
	}
	if isNew {
		if input.ExpectedRevision != 0 {
			return result, ErrRevision
		}
		_, err = tx.Exec(ctx, `INSERT INTO sessions(id,camp_id,project_id,profile_id,owner_user_id,owner_device_id,local_session_id,title,model,state,stage,shared,allow_requests,external_owner,history_only)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT DO NOTHING`, input.ID, campID, input.ProjectID, input.ProfileID, a.UserID, a.DeviceID, input.LocalSessionID, input.Title, input.Model, input.State, input.Stage, input.Shared, input.AllowRequests, input.ExternalOwner, input.HistoryOnly)
		if err != nil {
			return result, err
		}
	} else {
		if previous.OwnerUserID != a.UserID || previous.OwnerDeviceID != a.DeviceID || previous.ProfileID != input.ProfileID || previous.LocalSessionID != input.LocalSessionID {
			return result, ErrDenied
		}
		if previous.HistoryOnly != input.HistoryOnly {
			return result, ErrTransition
		}
		if input.ExpectedRevision != previous.Revision {
			return result, ErrRevision
		}
		if previous.ExecutionLocation == "cloud" {
			if input.Shared == previous.Shared && input.AllowRequests == previous.AllowRequests {
				if input.Title != previous.Title || input.Model != previous.Model || input.State != previous.State || input.Stage != previous.Stage || input.ProjectID != previous.ProjectID || input.ExternalOwner != previous.ExternalOwner {
					return result, ErrTransition
				}
				if err = s.finishCommand(ctx, tx, a, requestID, previous); err != nil {
					return result, err
				}
				return previous, tx.Commit(ctx)
			}
			// Sharing can always be revoked even if the Mac's runtime copy is old.
			input.Title, input.Model, input.State, input.Stage, input.ProjectID, input.ExternalOwner = previous.Title, previous.Model, previous.State, previous.Stage, previous.ProjectID, previous.ExternalOwner
		}
		_, err = tx.Exec(ctx, `UPDATE sessions SET permission_revision=permission_revision+CASE WHEN shared IS DISTINCT FROM $7 OR allow_requests IS DISTINCT FROM $8 THEN 1 ELSE 0 END,project_id=$2,title=$3,model=$4,state=$5,stage=$6,shared=$7,allow_requests=$8,external_owner=$9,revision=revision+1,updated_at=now() WHERE id=$1`, input.ID, input.ProjectID, input.Title, input.Model, input.State, input.Stage, input.Shared, input.AllowRequests, input.ExternalOwner)
		if err != nil {
			return result, err
		}
	}
	result, err = s.readSession(ctx, tx, a, campID, input.ID)
	if err != nil {
		return result, ErrConflict
	}
	if isNew && result.OwnerDeviceID != a.DeviceID {
		return Session{}, ErrDenied
	}
	if !isNew && result.PermissionRevision != previous.PermissionRevision {
		if err = s.invalidateSessionRequests(ctx, tx, a, campID, result.ID); err != nil {
			return result, err
		}
	}
	kind := "session.updated"
	if !input.Shared {
		kind = "session.revoked"
	}
	if err = emitSession(ctx, tx, a, campID, kind, result, previous.Shared && !input.Shared); err != nil {
		return result, err
	}
	if err = s.finishCommand(ctx, tx, a, requestID, result); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func emitSession(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, kind string, session Session, tombstone bool) error {
	if err := emit(ctx, tx, a, campID, kind, map[string]any{"session_id": session.ID, "revision": session.Revision, "transcript_revision": session.TranscriptRevision, "activity_revision": session.ActivityRevision}); err != nil {
		return err
	}
	var cursor int64
	if err := tx.QueryRow(ctx, `SELECT event_cursor FROM camps WHERE id=$1`, campID).Scan(&cursor); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE events SET session_id=$3 WHERE camp_id=$1 AND cursor=$2`, campID, cursor, session.ID); err != nil {
		return err
	}
	if tombstone {
		_, err := tx.Exec(ctx, `INSERT INTO event_recipients(camp_id,cursor,user_id) SELECT $1,$2,user_id FROM memberships WHERE camp_id=$1`, campID, cursor)
		return err
	}
	return nil
}

type SessionPage struct {
	Sessions []Session `json:"sessions"`
	Next     string    `json:"next"`
}

func (s *Store) ListSessions(ctx context.Context, a auth.Actor, campID, query, cursor string, limit int) (SessionPage, error) {
	if !validID(campID) || limit < 1 || limit > 200 || len(query) > 200 {
		return SessionPage{}, ErrInput
	}
	before := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	beforeID := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	if cursor != "" {
		if len(cursor) > 160 {
			return SessionPage{}, ErrInput
		}
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return SessionPage{}, ErrInput
		}
		at, id, ok := strings.Cut(string(decoded), "|")
		if !ok || !validID(id) {
			return SessionPage{}, ErrInput
		}
		before, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return SessionPage{}, ErrInput
		}
		beforeID = id
	}
	rows, err := s.pool.Query(ctx, `SELECT `+sessionColumns+` FROM sessions s JOIN profiles p ON p.id=s.profile_id JOIN memberships m ON m.camp_id=s.camp_id AND m.user_id=$1
 JOIN projects j ON j.id=s.project_id JOIN users u ON u.id=s.owner_user_id
 WHERE s.camp_id=$2 AND (s.owner_user_id=$1 OR s.shared) AND (s.updated_at,s.id)<($3,$4)
 AND NOT EXISTS(SELECT 1 FROM session_children child WHERE child.session_id=s.id)
 AND ($5='' OR strpos(lower(s.title||' '||p.provider||' '||p.label||' '||j.name||' '||u.display_name),lower($5))>0)
 ORDER BY s.updated_at DESC,s.id DESC LIMIT $6`, a.UserID, campID, before, beforeID, query, limit+1)
	if err != nil {
		return SessionPage{}, err
	}
	defer rows.Close()
	out := SessionPage{Sessions: []Session{}}
	for rows.Next() {
		value, err := scanSession(rows)
		if err != nil {
			return out, err
		}
		out.Sessions = append(out.Sessions, value)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	var member bool
	if err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE camp_id=$1 AND user_id=$2)`, campID, a.UserID).Scan(&member); err != nil {
		return out, err
	}
	if !member {
		return out, ErrDenied
	}
	if len(out.Sessions) > limit {
		out.Sessions = out.Sessions[:limit]
		last := out.Sessions[limit-1]
		out.Next = base64.RawURLEncoding.EncodeToString([]byte(last.UpdatedAt.Format(time.RFC3339Nano) + "|" + last.ID))
	}
	return out, nil
}

func (s *Store) WriteTranscript(ctx context.Context, a auth.Actor, campID, id, requestID string, input TranscriptInput) (int64, error) {
	if input.ExpectedRevision < 0 || len(input.Messages) == 0 || len(input.Messages) > 200 {
		return 0, ErrInput
	}
	if err := validateTranscriptMessages(input.Messages, false); err != nil {
		return 0, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return 0, err
	}
	session, err := s.readSession(ctx, tx, a, campID, id)
	if err != nil {
		return 0, err
	}
	// A human owner can finish a read-only copy after reconnecting on another
	// device. Live transcript publication retains its device ownership check.
	if session.OwnerUserID != a.UserID || (session.OwnerDeviceID != a.DeviceID && !session.HistoryOnly) {
		return 0, ErrDenied
	}
	if session.ExecutionLocation != "local" {
		return 0, ErrTransition
	}
	var result int64
	if hit, err := s.beginCommand(ctx, tx, a, requestID, "transcript.write:"+id, input, &result); err != nil {
		return 0, err
	} else if hit {
		return result, nil
	}
	if input.ExpectedRevision != session.TranscriptRevision {
		return 0, ErrRevision
	}
	result = session.TranscriptRevision + 1
	if err = appendTranscriptMessages(ctx, tx, id, result, input.Messages); err != nil {
		return 0, err
	}

	err = tx.QueryRow(ctx, `UPDATE sessions SET transcript_revision=$2,updated_at=now() WHERE id=$1 RETURNING revision`, id, result).Scan(&session.Revision)
	if err != nil {
		return 0, err
	}
	session.TranscriptRevision = result
	if err = emitSession(ctx, tx, a, campID, "transcript.updated", session, false); err != nil {
		return 0, err
	}
	if err = s.finishCommand(ctx, tx, a, requestID, result); err != nil {
		return 0, err
	}
	return result, tx.Commit(ctx)
}
func (s *Store) Transcript(ctx context.Context, a auth.Actor, campID, id string, revision, before int64, limit int) (Transcript, error) {
	return retryRead(ctx, func() (Transcript, error) { return s.transcriptOnce(ctx, a, campID, id, revision, before, limit) })
}
func (s *Store) transcriptOnce(ctx context.Context, a auth.Actor, campID, id string, revision, before int64, limit int) (Transcript, error) {
	if limit < 1 || limit > 200 || revision < 0 || before < 0 {
		return Transcript{}, ErrInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return Transcript{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return Transcript{}, err
	}
	session, err := s.readSession(ctx, tx, a, campID, id)
	if err != nil {
		return Transcript{}, err
	}
	if revision == 0 {
		revision = session.TranscriptRevision
	}
	if revision > session.TranscriptRevision {
		return Transcript{}, ErrRevision
	}
	out, err := readTranscriptAt(ctx, tx, id, revision, before, limit)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
