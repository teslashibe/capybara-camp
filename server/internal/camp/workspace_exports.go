package camp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

type WorkspaceExportInput struct {
	Kind                       string   `json:"kind"`
	Paths                      []string `json:"paths"`
	ExpectedEpoch              int64    `json:"expected_epoch"`
	ExpectedRevision           int64    `json:"expected_revision"`
	ExpectedTranscriptRevision int64    `json:"expected_transcript_revision"`
	ExpectedPermissionRevision int64    `json:"expected_permission_revision"`
}
type WorkspaceExport struct {
	ID                 string    `json:"id"`
	CampID             string    `json:"camp_id"`
	SessionID          string    `json:"session_id"`
	OwnerUserID        string    `json:"owner_user_id"`
	DeviceID           string    `json:"device_id"`
	WorkspaceID        string    `json:"workspace_id"`
	WorkspaceRevision  int64     `json:"workspace_revision"`
	SourceEpoch        int64     `json:"source_epoch"`
	SourceRevision     int64     `json:"source_revision"`
	TranscriptRevision int64     `json:"transcript_revision"`
	PermissionRevision int64     `json:"permission_revision"`
	Kind               string    `json:"kind"`
	Paths              []string  `json:"paths"`
	State              string    `json:"state"`
	ClaimID            string    `json:"claim_id,omitempty"`
	ResultSHA256       string    `json:"result_sha256"`
	ResultBytes        int       `json:"result_bytes"`
	Uploaded           []int     `json:"uploaded_chunks"`
	ExpiresAt          time.Time `json:"expires_at"`
	grant              string
}
type RunnerWorkspaceExport struct {
	Export    WorkspaceExport `json:"export"`
	Workspace Workspace       `json:"workspace"`
}

func exportPaths(paths []string, maximum int) bool {
	if len(paths) > maximum {
		return false
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || p == "." || len(p) > 1024 || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, `\:`) || strings.ContainsFunc(p, unicode.IsControl) || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}
func readWorkspaceExport(ctx context.Context, tx pgx.Tx, id string) (WorkspaceExport, error) {
	var e WorkspaceExport
	var paths []byte
	if !validID(id) {
		return e, ErrDenied
	}
	err := tx.QueryRow(ctx, `SELECT id,camp_id,session_id,owner_user_id,device_id,workspace_id,workspace_revision,source_epoch,source_revision,transcript_revision,permission_revision,kind,paths,state,COALESCE(claim_id::text,''),COALESCE(grant_id::text,''),result_sha256,result_bytes,expires_at FROM workspace_exports WHERE id=$1 FOR UPDATE`, id).Scan(&e.ID, &e.CampID, &e.SessionID, &e.OwnerUserID, &e.DeviceID, &e.WorkspaceID, &e.WorkspaceRevision, &e.SourceEpoch, &e.SourceRevision, &e.TranscriptRevision, &e.PermissionRevision, &e.Kind, &paths, &e.State, &e.ClaimID, &e.grant, &e.ResultSHA256, &e.ResultBytes, &e.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrDenied
	}
	if err != nil {
		return e, err
	}
	if err = json.Unmarshal(paths, &e.Paths); err != nil {
		return e, err
	}
	e.Uploaded = []int{}
	rows, err := tx.Query(ctx, `SELECT ordinal FROM workspace_export_chunks WHERE export_id=$1 ORDER BY ordinal`, id)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	for rows.Next() {
		var i int
		if err = rows.Scan(&i); err != nil {
			return e, err
		}
		e.Uploaded = append(e.Uploaded, i)
	}
	return e, rows.Err()
}
func (s *Store) expireWorkspaceExports(ctx context.Context, tx pgx.Tx, owner string) error {
	_, err := tx.Exec(ctx, `UPDATE workspace_exports SET state='expired' WHERE owner_user_id=$1 AND expires_at<=clock_timestamp() AND state IN ('pending','claimed','ready')`, owner)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM workspace_export_chunks c USING workspace_exports e WHERE c.export_id=e.id AND e.owner_user_id=$1 AND e.state IN ('expired','cancelled','failed')`, owner)
	return err
}
func exportSource(ctx context.Context, tx pgx.Tx, session Session, e WorkspaceExport) error {
	if session.ExecutionLocation != "cloud" || session.ExecutionWorkspaceID != e.WorkspaceID {
		return ErrRevision
	}
	return transferSource(ctx, tx, session, Transfer{SourceEpoch: e.SourceEpoch, SourceLocation: "cloud", SourceWorkspaceID: e.WorkspaceID, SourceRevision: e.SourceRevision, SourceTranscriptRevision: e.TranscriptRevision, SourcePermissionRevision: e.PermissionRevision})
}
func (s *Store) emitWorkspaceExport(ctx context.Context, tx pgx.Tx, e WorkspaceExport) error {
	a := auth.Actor{UserID: e.OwnerUserID, DeviceID: e.DeviceID}
	if err := emit(ctx, tx, a, e.CampID, "workspace.export", map[string]any{"id": e.ID, "session_id": e.SessionID, "state": e.State}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE events SET recipient_user_id=$2,session_id=$3 WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, e.CampID, e.OwnerUserID, e.SessionID)
	return err
}
func (g *GitStore) CreateWorkspaceExport(ctx context.Context, a auth.Actor, campID, sessionID, key string, in WorkspaceExportInput) (WorkspaceExport, error) {
	var e WorkspaceExport
	if !oneOf(in.Kind, "inspect", "capture") || !exportPaths(in.Paths, 256) || in.Kind == "inspect" && len(in.Paths) != 0 || in.Kind == "capture" && in.Paths == nil {
		return e, ErrInput
	}
	s := g.store
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return e, err
	}
	defer tx.Rollback(ctx)
	session, err := s.transferOwner(ctx, tx, a, campID, sessionID, true)
	if err != nil {
		return e, err
	}
	if err = s.expireWorkspaceExports(ctx, tx, a.UserID); err != nil {
		return e, err
	}
	if hit, err := s.beginCommand(ctx, tx, a, key, "workspace.export:"+sessionID, in, &e); err != nil {
		return e, err
	} else if hit {
		e, err = readWorkspaceExport(ctx, tx, e.ID)
		if err != nil {
			return e, err
		}
		return e, tx.Commit(ctx)
	}
	e = WorkspaceExport{ID: uuid.NewString(), CampID: campID, SessionID: sessionID, OwnerUserID: a.UserID, DeviceID: a.DeviceID, WorkspaceID: session.ExecutionWorkspaceID, SourceEpoch: in.ExpectedEpoch, SourceRevision: in.ExpectedRevision, TranscriptRevision: in.ExpectedTranscriptRevision, PermissionRevision: in.ExpectedPermissionRevision, Kind: in.Kind, Paths: in.Paths, State: "pending"}
	if err = exportSource(ctx, tx, session, e); err != nil {
		return e, err
	}
	if err = requireUnheldSession(ctx, tx, sessionID); err != nil {
		return e, err
	}
	w, err := readWorkspace(ctx, tx, a, campID, e.WorkspaceID)
	if err != nil {
		return e, err
	}
	if w.State != "ready" {
		return e, ErrTransition
	}
	authority, err := g.workspaceAuthority(ctx, tx, a, campID, sessionID, WorkspaceInput{ProviderAccountID: w.ProviderAccountID, GitConnectionID: w.GitConnectionID})
	if err != nil {
		return e, err
	}
	if authority.git.Epoch != w.GitEpoch || authority.account.Epoch != w.ProviderEpoch || authority.project.Revision != w.ProjectRevision {
		return e, ErrRevision
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM workspace_exports WHERE owner_user_id=$1 AND state IN ('pending','claimed','ready')`, a.UserID).Scan(&count); err != nil {
		return e, err
	}
	if count >= 2 {
		return e, auth.ErrRateLimited
	}
	e.WorkspaceRevision = w.Revision
	if e.Paths == nil {
		e.Paths = []string{}
	}
	paths, _ := json.Marshal(e.Paths)
	_, err = tx.Exec(ctx, `INSERT INTO workspace_exports(id,camp_id,session_id,owner_user_id,device_id,workspace_id,workspace_revision,source_epoch,source_revision,transcript_revision,permission_revision,kind,paths) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, e.ID, campID, sessionID, a.UserID, a.DeviceID, e.WorkspaceID, w.Revision, e.SourceEpoch, e.SourceRevision, e.TranscriptRevision, e.PermissionRevision, e.Kind, paths)
	if err != nil {
		return e, err
	}
	e, err = readWorkspaceExport(ctx, tx, e.ID)
	if err != nil {
		return e, err
	}
	if err = s.emitWorkspaceExport(ctx, tx, e); err != nil {
		return e, err
	}
	if err = s.finishCommand(ctx, tx, a, key, e); err != nil {
		return e, err
	}
	return e, tx.Commit(ctx)
}
func (s *Store) ownerWorkspaceExport(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, sessionID, id string, write bool) (WorkspaceExport, error) {
	var e WorkspaceExport
	if _, err := s.transferOwner(ctx, tx, a, campID, sessionID, write); err != nil {
		return e, err
	}
	if err := s.expireWorkspaceExports(ctx, tx, a.UserID); err != nil {
		return e, err
	}
	e, err := readWorkspaceExport(ctx, tx, id)
	if err != nil {
		return e, err
	}
	if e.OwnerUserID != a.UserID || e.CampID != campID || e.SessionID != sessionID {
		return e, ErrDenied
	}
	return e, nil
}
func (s *Store) WorkspaceExport(ctx context.Context, a auth.Actor, campID, sessionID, id string, cancel bool) (WorkspaceExport, error) {
	var e WorkspaceExport
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return e, err
	}
	defer tx.Rollback(ctx)
	e, err = s.ownerWorkspaceExport(ctx, tx, a, campID, sessionID, id, cancel)
	if err != nil {
		return e, err
	}
	if cancel && !oneOf(e.State, "cancelled", "expired", "failed") {
		_, err = tx.Exec(ctx, `UPDATE workspace_exports SET state='cancelled' WHERE id=$1`, e.ID)
		if err != nil {
			return e, err
		}
		e.State = "cancelled"
		if err = s.expireWorkspaceExports(ctx, tx, a.UserID); err != nil {
			return e, err
		}
		e.Uploaded = []int{}
		if err = s.emitWorkspaceExport(ctx, tx, e); err != nil {
			return e, err
		}
	}
	return e, tx.Commit(ctx)
}
func exportChunkBinding(e WorkspaceExport, i int) string {
	return fmt.Sprintf("workspace-export:v1:%s:%s:%s:%d", e.OwnerUserID, e.CampID, e.ID, i)
}
func (s *Store) exportChunk(ctx context.Context, tx pgx.Tx, e WorkspaceExport, index int) (TransferChunk, error) {
	var out TransferChunk
	var encrypted []byte
	if index < 0 || index >= (e.ResultBytes+TransferChunkBytes-1)/TransferChunkBytes {
		return out, ErrInput
	}
	err := tx.QueryRow(ctx, `SELECT sha256,encrypted_data FROM workspace_export_chunks WHERE export_id=$1 AND ordinal=$2`, e.ID, index).Scan(&out.SHA256, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrTransition
	}
	if err != nil {
		return out, err
	}
	out.Data, err = s.openProvider(encrypted, exportChunkBinding(e, index))
	if err != nil {
		return TransferChunk{}, err
	}
	if len(out.Data) != min(TransferChunkBytes, e.ResultBytes-index*TransferChunkBytes) || transferDigest(out.Data) != out.SHA256 {
		return TransferChunk{}, ErrInput
	}
	return out, nil
}
func (g *GitStore) WorkspaceExportChunk(ctx context.Context, a auth.Actor, campID, sessionID, id string, index int) (TransferChunk, error) {
	s := g.store
	var out TransferChunk
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	e, err := s.ownerWorkspaceExport(ctx, tx, a, campID, sessionID, id, false)
	if err != nil {
		return out, err
	}
	if e.State != "ready" {
		return out, ErrTransition
	}
	// Recheck current owner access and pinned source before exposing file bytes.
	session, err := s.readSession(ctx, tx, a, campID, sessionID)
	if err != nil {
		return out, err
	}
	if err = exportSource(ctx, tx, session, e); err != nil {
		return out, err
	}
	w, err := readWorkspace(ctx, tx, a, campID, e.WorkspaceID)
	if err != nil {
		return out, err
	}
	if w.State != "ready" || w.Revision != e.WorkspaceRevision {
		return out, ErrRevision
	}
	authority, err := g.workspaceAuthority(ctx, tx, a, campID, sessionID, WorkspaceInput{ProviderAccountID: w.ProviderAccountID, GitConnectionID: w.GitConnectionID})
	if err != nil {
		return out, err
	}
	if authority.git.Epoch != w.GitEpoch || authority.account.Epoch != w.ProviderEpoch || authority.project.ID != w.ProjectID || authority.project.Revision != w.ProjectRevision {
		return out, ErrRevision
	}
	out, err = s.exportChunk(ctx, tx, e, index)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
