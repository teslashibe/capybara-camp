package camp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/cortex"
)

type MemoryObservationInput struct {
	ProjectID  string    `json:"project_id"`
	Visibility string    `json:"visibility"`
	Title      string    `json:"title"`
	Source     SourceRef `json:"source"`
}
type MemoryObservation struct {
	ID            string    `json:"id"`
	SpaceID       string    `json:"space_id"`
	ProjectID     string    `json:"project_id"`
	Visibility    string    `json:"visibility"`
	CreatorUserID string    `json:"creator_user_id"`
	Title         string    `json:"title"`
	Content       string    `json:"content"`
	ContentHash   string    `json:"content_hash"`
	Source        SourceRef `json:"source"`
	CreatedAt     time.Time `json:"created_at"`
	Stored        bool      `json:"stored"`
}
type MemoryObservationPage struct {
	Observations []MemoryObservationSummary `json:"observations"`
	Next         string                     `json:"next"`
}
type MemoryObservationSummary struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Visibility   string    `json:"visibility"`
	Title        string    `json:"title"`
	ContentHash  string    `json:"content_hash"`
	SessionID    string    `json:"session_id"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	Stored       bool      `json:"stored"`
}

func (s *Store) memorySpace(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, projectID, visibility string, create bool) (string, error) {
	if !validID(projectID) || !oneOf(visibility, "private", "project") {
		return "", ErrInput
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT NOT archived FROM projects WHERE camp_id=$1 AND id=$2`, campID, projectID).Scan(&active); err != nil {
		return "", accessError(err)
	}
	if create && !active {
		return "", ErrTransition
	}
	owner := ""
	if visibility == "private" {
		owner = a.UserID
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM memory_spaces WHERE camp_id=$1 AND project_id=$2 AND owner_user_id IS NOT DISTINCT FROM NULLIF($3,'')::uuid`, campID, projectID, owner).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) && create {
		id = uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO memory_spaces(id,camp_id,project_id,owner_user_id) VALUES($1,$2,$3,NULLIF($4,'')::uuid)`, id, campID, projectID, owner)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

const memoryObservationColumns = `o.id,o.space_id,p.project_id,CASE WHEN p.owner_user_id IS NULL THEN 'project' ELSE 'private' END,o.creator_user_id,o.title,o.content,o.content_hash,o.source_session_id,o.transcript_revision,o.permission_revision,o.message_ids,o.created_at,o.engine_saved_at IS NOT NULL`

func scanMemoryObservation(row rowScanner) (MemoryObservation, error) {
	var value MemoryObservation
	err := row.Scan(&value.ID, &value.SpaceID, &value.ProjectID, &value.Visibility, &value.CreatorUserID, &value.Title, &value.Content, &value.ContentHash, &value.Source.SessionID, &value.Source.TranscriptRevision, &value.Source.PermissionRevision, &value.Source.MessageIDs, &value.CreatedAt, &value.Stored)
	return value, err
}
func (s *Store) memoryObservation(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, id string) (MemoryObservation, error) {
	if !validID(id) {
		return MemoryObservation{}, ErrDenied
	}
	v, err := scanMemoryObservation(tx.QueryRow(ctx, `SELECT `+memoryObservationColumns+` FROM memory_observations o JOIN memory_spaces p ON p.id=o.space_id WHERE o.camp_id=$1 AND o.id=$2 AND (p.owner_user_id IS NULL OR p.owner_user_id=$3)`, campID, id, a.UserID))
	if err != nil {
		return v, accessError(err)
	}
	if err = s.memorySource(ctx, tx, a, campID, v); err != nil {
		return MemoryObservation{}, err
	}
	return v, nil
}
func (s *Store) memorySource(ctx context.Context, tx pgx.Tx, a auth.Actor, campID string, value MemoryObservation) error {
	source, err := s.readSession(ctx, tx, a, campID, value.Source.SessionID)
	if err != nil {
		return err
	}
	if source.TranscriptRevision < value.Source.TranscriptRevision {
		return ErrDenied
	}
	if value.Visibility == "project" && !source.Shared {
		return ErrDenied
	}
	if source.PermissionRevision != value.Source.PermissionRevision && !(value.Visibility == "private" && source.OwnerUserID == a.UserID) {
		return ErrDenied
	}
	return nil
}

func (s *Store) SaveMemoryObservation(ctx context.Context, a auth.Actor, campID, key string, input MemoryObservationInput) (MemoryObservation, error) {
	if s.memory == nil {
		return MemoryObservation{}, ErrTransition
	}
	if !validID(input.Source.SessionID) || input.Source.TranscriptRevision < 1 || input.Source.PermissionRevision < 1 {
		return MemoryObservation{}, ErrInput
	}
	input.Title = strings.TrimSpace(input.Title)
	if len(input.Title) > 500 {
		return MemoryObservation{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MemoryObservation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return MemoryObservation{}, err
	}
	var saved MemoryObservation
	if hit, e := s.beginCommand(ctx, tx, a, key, "memory.observe:"+campID, input, &saved); e != nil {
		return saved, e
	} else if hit {
		saved, err = s.memoryObservation(ctx, tx, a, campID, saved.ID)
		if err != nil {
			return saved, err
		}
		if err = tx.Commit(ctx); err != nil {
			return saved, err
		}
		return s.syncMemoryObservation(ctx, a, campID, saved)
	}
	source, err := s.readSession(ctx, tx, a, campID, input.Source.SessionID)
	if err != nil {
		return saved, err
	}
	if source.PermissionRevision != input.Source.PermissionRevision || input.Source.TranscriptRevision > source.TranscriptRevision {
		return saved, ErrRevision
	}
	if input.Visibility == "project" && !source.Shared {
		return saved, ErrDenied
	}
	space, err := s.memorySpace(ctx, tx, a, campID, input.ProjectID, input.Visibility, true)
	if err != nil {
		return saved, err
	}
	messages, err := readContextMessages(ctx, tx, input.Source)
	if err != nil {
		return saved, err
	}
	for _, m := range messages {
		if len(m.Text) > 48<<10 {
			return saved, ErrInput
		}
	}
	content, err := json.Marshal(messages)
	if err != nil || len(content) > 256<<10 {
		return saved, ErrInput
	}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	if input.Title == "" {
		input.Title = source.Title
	}
	err = tx.QueryRow(ctx, `SELECT id FROM memory_observations WHERE space_id=$1 AND source_session_id=$2 AND transcript_revision=$3 AND permission_revision=$4 AND content_hash=$5`, space, source.ID, input.Source.TranscriptRevision, input.Source.PermissionRevision, hash).Scan(&saved.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM memory_observations WHERE space_id=$1`, space).Scan(&count); err != nil {
			return saved, err
		}
		if count >= 10000 {
			return saved, ErrTransition
		}
		saved.ID = uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO memory_observations(id,camp_id,space_id,creator_user_id,source_session_id,transcript_revision,permission_revision,message_ids,title,content,content_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, saved.ID, campID, space, a.UserID, source.ID, input.Source.TranscriptRevision, input.Source.PermissionRevision, input.Source.MessageIDs, input.Title, string(content), hash)
		if err != nil {
			return saved, err
		}
		owner := ""
		if input.Visibility == "private" {
			owner = a.UserID
		}
		if err = emitMemoryObservation(ctx, tx, a, campID, saved.ID, input.ProjectID, source.ID, owner, false); err != nil {
			return saved, err
		}
	} else if err != nil {
		return saved, err
	}
	saved, err = s.memoryObservation(ctx, tx, a, campID, saved.ID)
	if err != nil {
		return saved, err
	}
	if err = s.finishCommand(ctx, tx, a, key, saved); err != nil {
		return saved, err
	}
	if err = tx.Commit(ctx); err != nil {
		return saved, err
	}
	return s.syncMemoryObservation(ctx, a, campID, saved)
}

func (s *Store) syncMemoryObservation(ctx context.Context, a auth.Actor, campID string, value MemoryObservation) (MemoryObservation, error) {
	if value.Stored {
		return value, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return value, nil
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return MemoryObservation{}, err
	}
	current, err := s.memoryObservation(ctx, tx, a, campID, value.ID)
	if err != nil {
		return MemoryObservation{}, err
	}
	if current.Stored {
		return current, nil
	}
	// Cortex has its own lifecycle locks. Release Camp's policy lock before
	// entering it, then recheck permissions before publishing completion.
	if err = tx.Commit(ctx); err != nil {
		return current, nil
	}
	err = s.memory.Observe(ctx, []cortex.Observation{{ID: current.ID, UserID: current.SpaceID, Source: "camp", Content: current.Content, Refs: map[string]any{"session_id": current.Source.SessionID, "transcript_revision": current.Source.TranscriptRevision, "message_ids": current.Source.MessageIDs}, At: current.CreatedAt, IdempotencyKey: current.ID}})
	if err != nil {
		return current, nil
	}
	tx, err = s.pool.Begin(ctx)
	if err != nil {
		return current, nil
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return MemoryObservation{}, err
	}
	current, err = s.memoryObservation(ctx, tx, a, campID, current.ID)
	if err != nil {
		return MemoryObservation{}, err
	}
	if current.Stored {
		return current, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE memory_observations SET engine_saved_at=now() WHERE id=$1`, current.ID); err != nil {
		return current, nil
	}
	owner := ""
	if current.Visibility == "private" {
		owner = current.CreatorUserID
	}
	if err = emitMemoryObservation(ctx, tx, a, campID, current.ID, current.ProjectID, current.Source.SessionID, owner, true); err != nil {
		return current, nil
	}
	if err = tx.Commit(ctx); err != nil {
		return current, nil
	}
	current.Stored = true
	return current, nil
}

func emitMemoryObservation(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, id, projectID, sourceID, owner string, stored bool) error {
	if err := emit(ctx, tx, a, campID, "memory.changed", map[string]any{"observation_id": id, "project_id": projectID, "session_id": sourceID, "stored": stored}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE events SET session_id=$2,recipient_user_id=NULLIF($3,'')::uuid,memory_observation_id=$4 WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, campID, sourceID, owner, id)
	return err
}

func (s *Store) MemoryObservation(ctx context.Context, a auth.Actor, campID, id string) (MemoryObservation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MemoryObservation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return MemoryObservation{}, err
	}
	return s.memoryObservation(ctx, tx, a, campID, id)
}

func (s *Store) MemoryObservations(ctx context.Context, a auth.Actor, campID, projectID, visibility, after string, limit int) (MemoryObservationPage, error) {
	page := MemoryObservationPage{Observations: []MemoryObservationSummary{}}
	if limit < 1 || limit > 100 || after != "" && !validID(after) {
		return page, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return page, err
	}
	space, err := s.memorySpace(ctx, tx, a, campID, projectID, visibility, false)
	if err != nil || space == "" {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT o.id,p.project_id,CASE WHEN p.owner_user_id IS NULL THEN 'project' ELSE 'private' END,o.title,o.content_hash,o.source_session_id,cardinality(o.message_ids),o.created_at,o.engine_saved_at IS NOT NULL FROM memory_observations o JOIN memory_spaces p ON p.id=o.space_id JOIN sessions s ON s.id=o.source_session_id WHERE o.space_id=$1 AND (s.shared OR s.owner_user_id=$2) AND (p.owner_user_id IS NOT NULL OR s.shared) AND (s.permission_revision=o.permission_revision OR p.owner_user_id=$2 AND s.owner_user_id=$2) AND s.transcript_revision>=o.transcript_revision AND NOT EXISTS(SELECT 1 FROM session_children c WHERE c.session_id=s.id) AND ($3='' OR o.id>NULLIF($3,'')::uuid) ORDER BY o.id LIMIT $4`, space, a.UserID, after, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var v MemoryObservationSummary
		err := rows.Scan(&v.ID, &v.ProjectID, &v.Visibility, &v.Title, &v.ContentHash, &v.SessionID, &v.MessageCount, &v.CreatedAt, &v.Stored)
		if err != nil {
			return page, err
		}
		if len(page.Observations) == limit {
			page.Next = page.Observations[len(page.Observations)-1].ID
			break
		}
		page.Observations = append(page.Observations, v)
	}
	return page, rows.Err()
}
