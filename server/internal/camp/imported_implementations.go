package camp

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// Historical permission settings describe a prior local attempt. They never
// grant permission to execute a new request on a Mac or cloud runner.
type ImportedPermissions struct {
	Profile    string `json:"profile"`
	Sandbox    string `json:"sandbox"`
	Approval   string `json:"approval"`
	Reviewer   string `json:"reviewer"`
	ClaudeMode string `json:"claude_mode"`
}
type ImportedImplementationDetails struct {
	Model              string               `json:"model"`
	Effort             string               `json:"effort"`
	Speed              string               `json:"speed"`
	PlanDigest         string               `json:"plan_digest"`
	ContextFingerprint string               `json:"context_fingerprint"`
	InitialPrompt      string               `json:"initial_prompt"`
	Permissions        *ImportedPermissions `json:"permissions,omitempty"`
	PermissionSummary  string               `json:"permission_summary"`
}
type ImportedImplementationAttempt struct {
	ID                    string               `json:"id"`
	Prompt                string               `json:"prompt"`
	Model                 string               `json:"model"`
	Effort                string               `json:"effort"`
	Speed                 string               `json:"speed"`
	State                 string               `json:"state"`
	Error                 string               `json:"error"`
	Usage                 *PlanningUsage       `json:"usage,omitempty"`
	Permissions           *ImportedPermissions `json:"permissions,omitempty"`
	PermissionFingerprint string               `json:"permission_fingerprint,omitempty"`
}
type ImportedImplementationOrigin struct {
	SourceID           string `json:"source_id"`
	PlanVersion        int64  `json:"plan_version"`
	SessionID          string `json:"session_id"`
	TranscriptRevision int64  `json:"transcript_revision"`
	MessageCount       int    `json:"message_count"`
	Provider           string `json:"provider"`
	State              string `json:"state"`
}
type ImportedImplementationInput struct {
	ImportedImplementationOrigin
	ImportedImplementationDetails
	Attempts []ImportedImplementationAttempt `json:"attempts"`
}
type ImportedImplementation struct {
	ID           string `json:"id"`
	Ordinal      int    `json:"ordinal"`
	AttemptCount int    `json:"attempt_count"`
	ImportedImplementationOrigin
	ImportedImplementationDetails
}
type ImportedImplementationSummary struct {
	ID           string `json:"id"`
	Ordinal      int    `json:"ordinal"`
	AttemptCount int    `json:"attempt_count"`
	ImportedImplementationOrigin
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Speed  string `json:"speed"`
}
type ImportedImplementationPage struct {
	Implementations []ImportedImplementationSummary `json:"implementations"`
	Next            int                             `json:"next"`
}
type ImportedAttemptRecord struct {
	Ordinal int `json:"ordinal"`
	ImportedImplementationAttempt
}
type ImportedAttemptPage struct {
	Attempts []ImportedAttemptRecord `json:"attempts"`
	Next     int                     `json:"next"`
}

func validHistoricalPermissions(p *ImportedPermissions) bool {
	if p == nil {
		return true
	}
	for _, v := range []string{p.Profile, p.Sandbox, p.Approval, p.Reviewer, p.ClaudeMode} {
		if len(v) > 160 {
			return false
		}
	}
	return true
}
func validImportedImplementations(values []ImportedImplementationInput, versions int) bool {
	if len(values) > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range values {
		if !digest64(v.SourceID) || seen[v.SourceID] || v.PlanVersion < 1 || v.PlanVersion > int64(versions) || !validID(v.SessionID) || v.TranscriptRevision < 0 || v.MessageCount < 0 || v.MessageCount > 2000 || !oneOf(v.Provider, "Codex", "Claude") || !oneOf(v.State, "approved", "idle", "complete", "needs_review") || len(v.Model) > 200 || len(v.Effort) > 64 || len(v.Speed) > 64 || !digest64(v.PlanDigest) || !digest64(v.ContextFingerprint) || len(v.InitialPrompt) > 65536 || len(v.PermissionSummary) > 2048 || !validHistoricalPermissions(v.Permissions) || len(v.Attempts) > 200 {
			return false
		}
		seen[v.SourceID] = true
		attempts := map[string]bool{}
		for _, attempt := range v.Attempts {
			if !validID(attempt.ID) || attempts[attempt.ID] || len(attempt.Prompt) > 65536 || len(attempt.Model) > 200 || len(attempt.Effort) > 64 || len(attempt.Speed) > 64 || !oneOf(attempt.State, "completed", "cancelled", "failed", "uncertain") || len(attempt.Error) > 65536 || !validPlanningUsage(attempt.Usage) || !validHistoricalPermissions(attempt.Permissions) || attempt.PermissionFingerprint != "" && !digest64(attempt.PermissionFingerprint) {
				return false
			}
			attempts[attempt.ID] = true
		}
	}
	return true
}

func (s *Store) insertImportedImplementations(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, planID string, values []ImportedImplementationInput) error {
	for ordinal, v := range values {
		source, err := s.readSession(ctx, tx, a, campID, v.SessionID)
		if err != nil {
			return err
		}
		if source.OwnerUserID != a.UserID || source.Provider != v.Provider || v.TranscriptRevision > source.TranscriptRevision {
			return ErrDenied
		}
		var count int
		var textBytes int64
		if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(octet_length(v.text)),0) FROM messages m CROSS JOIN LATERAL(SELECT removed,text FROM message_revisions WHERE session_id=m.session_id AND local_id=m.local_id AND revision<=$2 ORDER BY revision DESC LIMIT 1) v WHERE m.session_id=$1 AND NOT v.removed`, v.SessionID, v.TranscriptRevision).Scan(&count, &textBytes); err != nil {
			return err
		}
		if count != v.MessageCount {
			return ErrRevision
		}
		if textBytes > 8<<20 {
			return ErrInput
		}
		details, err := json.Marshal(v.ImportedImplementationDetails)
		if err != nil {
			return err
		}
		id := uuid.NewString()
		if _, err = tx.Exec(ctx, `INSERT INTO planning_imported_implementations(id,camp_id,plan_id,plan_version,ordinal,source_id,session_id,transcript_revision,message_count,provider,state,details,attempt_count) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, campID, planID, v.PlanVersion, ordinal+1, v.SourceID, v.SessionID, v.TranscriptRevision, v.MessageCount, v.Provider, v.State, details, len(v.Attempts)); err != nil {
			return err
		}
		rows := make([][]any, 0, len(v.Attempts))
		for index, attempt := range v.Attempts {
			raw, err := json.Marshal(attempt)
			if err != nil {
				return err
			}
			rows = append(rows, []any{id, index + 1, raw})
		}
		if len(rows) > 0 {
			if _, err = tx.CopyFrom(ctx, pgx.Identifier{"planning_imported_attempts"}, []string{"implementation_id", "ordinal", "body"}, pgx.CopyFromRows(rows)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) readImportedImplementation(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, planID, id string) (ImportedImplementation, error) {
	var value ImportedImplementation
	if !validID(id) {
		return value, ErrDenied
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT id,ordinal,attempt_count,source_id,plan_version,session_id,transcript_revision,message_count,provider,state,details FROM planning_imported_implementations WHERE camp_id=$1 AND plan_id=$2 AND id=$3`, campID, planID, id).Scan(&value.ID, &value.Ordinal, &value.AttemptCount, &value.SourceID, &value.PlanVersion, &value.SessionID, &value.TranscriptRevision, &value.MessageCount, &value.Provider, &value.State, &raw)
	if err != nil {
		return value, accessError(err)
	}
	plan, err := s.loadPlanning(ctx, tx, a, campID, planID, value.PlanVersion)
	if err != nil {
		return value, err
	}
	if err = s.checkPlanningSources(ctx, tx, a, plan); err != nil {
		return value, err
	}
	if _, err = s.readSession(ctx, tx, a, campID, value.SessionID); err != nil {
		return value, err
	}
	err = json.Unmarshal(raw, &value.ImportedImplementationDetails)
	return value, err
}

func (s *Store) ImportedImplementations(ctx context.Context, a auth.Actor, campID, planID string, version int64, after, limit int) (ImportedImplementationPage, error) {
	out := ImportedImplementationPage{Implementations: []ImportedImplementationSummary{}}
	if version < 0 || after < 0 || limit < 1 || limit > 50 {
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
	plan, err := s.loadPlanning(ctx, tx, a, campID, planID, version)
	if err != nil {
		return out, err
	}
	if err = s.checkPlanningSources(ctx, tx, a, plan); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT i.id,i.ordinal,i.attempt_count,i.source_id,i.plan_version,i.session_id,i.transcript_revision,i.message_count,i.provider,i.state,COALESCE(i.details->>'model',''),COALESCE(i.details->>'effort',''),COALESCE(i.details->>'speed','') FROM planning_imported_implementations i JOIN sessions s ON s.id=i.session_id WHERE i.plan_id=$1 AND i.plan_version=$2 AND i.ordinal>$3 AND (s.owner_user_id=$4 OR s.shared) ORDER BY i.ordinal LIMIT $5`, planID, plan.Version.Version, after, a.UserID, limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var value ImportedImplementationSummary
		if err = rows.Scan(&value.ID, &value.Ordinal, &value.AttemptCount, &value.SourceID, &value.PlanVersion, &value.SessionID, &value.TranscriptRevision, &value.MessageCount, &value.Provider, &value.State, &value.Model, &value.Effort, &value.Speed); err != nil {
			rows.Close()
			return out, err
		}
		out.Implementations = append(out.Implementations, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	more := len(out.Implementations) > limit
	if more {
		out.Implementations = out.Implementations[:limit]
	}
	if more {
		out.Next = out.Implementations[len(out.Implementations)-1].Ordinal
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ImportedImplementation(ctx context.Context, a auth.Actor, campID, planID, id string) (ImportedImplementation, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return ImportedImplementation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return ImportedImplementation{}, err
	}
	value, err := s.readImportedImplementation(ctx, tx, a, campID, planID, id)
	if err != nil {
		return value, err
	}
	return value, tx.Commit(ctx)
}
func (s *Store) ImportedImplementationAttempts(ctx context.Context, a auth.Actor, campID, planID, id string, after, limit int) (ImportedAttemptPage, error) {
	out := ImportedAttemptPage{Attempts: []ImportedAttemptRecord{}}
	if after < 0 || limit < 1 || limit > 10 {
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
	if _, err = s.readImportedImplementation(ctx, tx, a, campID, planID, id); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT ordinal,body FROM planning_imported_attempts WHERE implementation_id=$1 AND ordinal>$2 ORDER BY ordinal LIMIT $3`, id, after, limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item ImportedAttemptRecord
		var raw []byte
		if err = rows.Scan(&item.Ordinal, &raw); err != nil {
			rows.Close()
			return out, err
		}
		if err = json.Unmarshal(raw, &item.ImportedImplementationAttempt); err != nil {
			rows.Close()
			return out, err
		}
		out.Attempts = append(out.Attempts, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Attempts) > limit {
		out.Attempts = out.Attempts[:limit]
		out.Next = out.Attempts[len(out.Attempts)-1].Ordinal
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ImportedImplementationTranscript(ctx context.Context, a auth.Actor, campID, planID, id string, before int64, limit int) (Transcript, error) {
	if before < 0 || limit < 1 || limit > 200 {
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
	value, err := s.readImportedImplementation(ctx, tx, a, campID, planID, id)
	if err != nil {
		return Transcript{}, err
	}
	out, err := readTranscriptAt(ctx, tx, value.SessionID, value.TranscriptRevision, before, limit)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
