package camp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

const TransferChunkBytes = 256 << 10

var transferHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

type TransferInput struct {
	TargetLocation             string `json:"target_location"`
	TargetAccountID            string `json:"target_account_id,omitempty"`
	ExpectedEpoch              int64  `json:"expected_epoch"`
	ExpectedRevision           int64  `json:"expected_revision"`
	ExpectedTranscriptRevision int64  `json:"expected_transcript_revision"`
	ExpectedPermissionRevision int64  `json:"expected_permission_revision"`
	PayloadSHA256              string `json:"payload_sha256"`
	ReviewSHA256               string `json:"review_sha256"`
	PayloadBytes               int    `json:"payload_bytes"`
}

type Transfer struct {
	ID                       string    `json:"id"`
	CampID                   string    `json:"camp_id"`
	SessionID                string    `json:"session_id"`
	OwnerUserID              string    `json:"owner_user_id"`
	SourceLocation           string    `json:"source_location"`
	SourceWorkspaceID        string    `json:"source_workspace_id,omitempty"`
	SourceEpoch              int64     `json:"source_epoch"`
	SourceRevision           int64     `json:"source_revision"`
	SourceTranscriptRevision int64     `json:"source_transcript_revision"`
	SourcePermissionRevision int64     `json:"source_permission_revision"`
	TargetLocation           string    `json:"target_location"`
	TargetAccountID          string    `json:"target_account_id,omitempty"`
	TargetAccountEpoch       int64     `json:"target_account_epoch,omitempty"`
	TargetDeviceID           string    `json:"target_device_id,omitempty"`
	PayloadSHA256            string    `json:"payload_sha256"`
	ReviewSHA256             string    `json:"review_sha256"`
	BaseCommit               string    `json:"base_commit,omitempty"`
	SourceBranch             string    `json:"source_branch,omitempty"`
	FileCount                int       `json:"file_count"`
	PayloadBytes             int       `json:"payload_bytes"`
	UploadedBytes            int       `json:"uploaded_bytes"`
	Uploaded                 []int     `json:"uploaded_chunks"`
	ChunkBytes               int       `json:"chunk_bytes"`
	State                    string    `json:"state"`
	Revision                 int64     `json:"revision"`
	ExpiresAt                time.Time `json:"expires_at"`
}
type TransferChunk struct {
	Data   []byte `json:"data"`
	SHA256 string `json:"sha256"`
}

func transferDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func transferBinding(t Transfer, ordinal int) string {
	return fmt.Sprintf("transfer:v1:%s:%s:%s:%d", t.OwnerUserID, t.CampID, t.ID, ordinal)
}

func (s *Store) transferOwner(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, sessionID string, write bool) (Session, error) {
	if err := s.cloudEnabled(); err != nil {
		return Session{}, err
	}
	if _, err := s.lock(ctx, tx, a, campID, write); err != nil {
		return Session{}, err
	}
	value, err := s.readSession(ctx, tx, a, campID, sessionID)
	if err != nil {
		return Session{}, err
	}
	if value.OwnerUserID != a.UserID {
		return Session{}, ErrDenied
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, a.UserID); err != nil {
		return Session{}, err
	}
	return value, nil
}

func transferSource(ctx context.Context, tx pgx.Tx, session Session, t Transfer) error {
	if session.ExecutionEpoch != t.SourceEpoch || session.ExecutionLocation != t.SourceLocation || session.ExecutionWorkspaceID != t.SourceWorkspaceID || session.Revision != t.SourceRevision || session.TranscriptRevision != t.SourceTranscriptRevision || session.PermissionRevision != t.SourcePermissionRevision {
		return ErrRevision
	}
	if session.ExternalOwner || !(oneOf(session.State, "idle", "done") || session.ExecutionLocation == "cloud" && session.State == "waiting") {
		return ErrTransition
	}
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM prompt_requests WHERE session_id=$1 AND state NOT IN ('completed','failed','cancelled'))`, session.ID).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return ErrTransition
	}
	return nil
}

func (s *Store) expireTransfers(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `UPDATE session_transfers SET state='expired',uploaded_bytes=0,revision=revision+1,updated_at=now() WHERE owner_user_id=$1 AND state IN ('uploading','ready') AND expires_at<=now()`, userID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM transfer_chunks c USING session_transfers t WHERE c.transfer_id=t.id AND t.owner_user_id=$1 AND t.state='expired'`, userID)
	return err
}

func readTransfer(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, sessionID, id string) (Transfer, error) {
	var t Transfer
	if !validID(id) {
		return t, ErrDenied
	}
	err := tx.QueryRow(ctx, `SELECT id,camp_id,session_id,owner_user_id,source_location,COALESCE(source_workspace_id::text,''),source_epoch,source_revision,source_transcript_revision,source_permission_revision,target_location,COALESCE(target_account_id::text,''),COALESCE(target_account_epoch,0),COALESCE(target_device_id::text,''),payload_sha256,review_sha256,payload_bytes,uploaded_bytes,state,revision,expires_at,COALESCE(base_commit,''),COALESCE(source_branch,''),COALESCE(file_count,0) FROM session_transfers WHERE id=$1 AND camp_id=$2 AND session_id=$3 AND owner_user_id=$4 FOR UPDATE`, id, campID, sessionID, a.UserID).Scan(&t.ID, &t.CampID, &t.SessionID, &t.OwnerUserID, &t.SourceLocation, &t.SourceWorkspaceID, &t.SourceEpoch, &t.SourceRevision, &t.SourceTranscriptRevision, &t.SourcePermissionRevision, &t.TargetLocation, &t.TargetAccountID, &t.TargetAccountEpoch, &t.TargetDeviceID, &t.PayloadSHA256, &t.ReviewSHA256, &t.PayloadBytes, &t.UploadedBytes, &t.State, &t.Revision, &t.ExpiresAt, &t.BaseCommit, &t.SourceBranch, &t.FileCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrDenied
	}
	if err != nil {
		return t, err
	}
	t.Uploaded = []int{}
	t.ChunkBytes = TransferChunkBytes
	rows, err := tx.Query(ctx, `SELECT ordinal FROM transfer_chunks WHERE transfer_id=$1 ORDER BY ordinal`, t.ID)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	for rows.Next() {
		var i int
		if err = rows.Scan(&i); err != nil {
			return t, err
		}
		t.Uploaded = append(t.Uploaded, i)
	}
	return t, rows.Err()
}

func (s *Store) transferTarget(ctx context.Context, tx pgx.Tx, a auth.Actor, session Session, t Transfer) (int64, error) {
	if t.TargetLocation == "local" {
		if t.TargetDeviceID != a.DeviceID {
			return 0, ErrDenied
		}
		return 0, nil
	}
	account, err := s.lockAccount(ctx, tx, a.UserID, t.TargetAccountID)
	if err != nil {
		return 0, err
	}
	if account.Provider != session.Provider || account.State != "connected" || account.Location != "cloud" || account.CredentialExpiresAt != nil && !account.CredentialExpiresAt.After(time.Now()) {
		return 0, ErrTransition
	}
	if t.TargetAccountEpoch != 0 && t.TargetAccountEpoch != account.Epoch {
		return 0, ErrRevision
	}
	return account.Epoch, nil
}

func (s *Store) emitTransfer(ctx context.Context, tx pgx.Tx, a auth.Actor, t Transfer) error {
	if err := emit(ctx, tx, a, t.CampID, "transfer.updated", map[string]any{"id": t.ID, "session_id": t.SessionID, "state": t.State, "revision": t.Revision}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE events SET recipient_user_id=$2,session_id=$3 WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, t.CampID, a.UserID, t.SessionID)
	return err
}

func (s *Store) CreateTransfer(ctx context.Context, a auth.Actor, campID, sessionID, key string, in TransferInput) (Transfer, error) {
	if !oneOf(in.TargetLocation, "local", "cloud") || in.TargetLocation == "cloud" && !validID(in.TargetAccountID) || in.TargetLocation == "local" && in.TargetAccountID != "" || !transferHash.MatchString(in.PayloadSHA256) || !transferHash.MatchString(in.ReviewSHA256) || in.PayloadBytes < 2 || in.PayloadBytes > transfer.MaxWireBytes {
		return Transfer{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback(ctx)
	session, err := s.transferOwner(ctx, tx, a, campID, sessionID, true)
	if err != nil {
		return Transfer{}, err
	}
	if err = s.expireTransfers(ctx, tx, a.UserID); err != nil {
		return Transfer{}, err
	}
	var t Transfer
	if hit, e := s.beginCommand(ctx, tx, a, key, "transfer.create:"+sessionID, in, &t); e != nil {
		return t, e
	} else if hit {
		t, err = readTransfer(ctx, tx, a, campID, sessionID, t.ID)
		if err != nil {
			return t, err
		}
		return t, tx.Commit(ctx)
	}
	t = Transfer{ID: uuid.NewString(), CampID: campID, SessionID: sessionID, OwnerUserID: a.UserID, SourceLocation: session.ExecutionLocation, SourceWorkspaceID: session.ExecutionWorkspaceID, SourceEpoch: in.ExpectedEpoch, SourceRevision: in.ExpectedRevision, SourceTranscriptRevision: in.ExpectedTranscriptRevision, SourcePermissionRevision: in.ExpectedPermissionRevision, TargetLocation: in.TargetLocation, TargetAccountID: in.TargetAccountID, PayloadSHA256: in.PayloadSHA256, ReviewSHA256: in.ReviewSHA256, PayloadBytes: in.PayloadBytes}
	if err = transferSource(ctx, tx, session, t); err != nil {
		return t, err
	}
	if t.TargetLocation == "local" {
		t.TargetDeviceID = a.DeviceID
	}
	t.TargetAccountEpoch, err = s.transferTarget(ctx, tx, a, session, t)
	if err != nil {
		return t, err
	}
	var active int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM session_transfers WHERE owner_user_id=$1 AND state IN ('uploading','ready') AND expires_at>now()`, a.UserID).Scan(&active); err != nil {
		return t, err
	}
	if active >= 2 {
		return t, ErrTransition
	}
	_, err = tx.Exec(ctx, `INSERT INTO session_transfers(id,camp_id,session_id,owner_user_id,source_location,source_workspace_id,source_epoch,source_revision,source_transcript_revision,source_permission_revision,target_location,target_account_id,target_account_epoch,target_device_id,payload_sha256,review_sha256,payload_bytes) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8,$9,$10,$11,NULLIF($12,'')::uuid,NULLIF($13,0),NULLIF($14,'')::uuid,$15,$16,$17)`, t.ID, t.CampID, t.SessionID, t.OwnerUserID, t.SourceLocation, t.SourceWorkspaceID, t.SourceEpoch, t.SourceRevision, t.SourceTranscriptRevision, t.SourcePermissionRevision, t.TargetLocation, t.TargetAccountID, t.TargetAccountEpoch, t.TargetDeviceID, t.PayloadSHA256, t.ReviewSHA256, t.PayloadBytes)
	if err != nil {
		return t, err
	}
	t, err = readTransfer(ctx, tx, a, campID, sessionID, t.ID)
	if err != nil {
		return t, err
	}
	if err = s.emitTransfer(ctx, tx, a, t); err != nil {
		return t, err
	}
	if err = s.finishCommand(ctx, tx, a, key, t); err != nil {
		return t, err
	}
	return t, tx.Commit(ctx)
}

func (s *Store) Transfer(ctx context.Context, a auth.Actor, campID, sessionID, id string) (Transfer, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.transferOwner(ctx, tx, a, campID, sessionID, false); err != nil {
		return Transfer{}, err
	}
	if err = s.expireTransfers(ctx, tx, a.UserID); err != nil {
		return Transfer{}, err
	}
	t, err := readTransfer(ctx, tx, a, campID, sessionID, id)
	if err != nil {
		return t, err
	}
	return t, tx.Commit(ctx)
}

func (s *Store) ListTransfers(ctx context.Context, a auth.Actor, campID, sessionID string) ([]Transfer, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.transferOwner(ctx, tx, a, campID, sessionID, false); err != nil {
		return nil, err
	}
	if err = s.expireTransfers(ctx, tx, a.UserID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM session_transfers WHERE camp_id=$1 AND session_id=$2 AND owner_user_id=$3 ORDER BY created_at DESC,id DESC LIMIT 25`, campID, sessionID, a.UserID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]Transfer, 0, len(ids))
	for _, id := range ids {
		value, err := readTransfer(ctx, tx, a, campID, sessionID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, tx.Commit(ctx)
}

func (s *Store) PutTransferChunk(ctx context.Context, a auth.Actor, campID, sessionID, id string, index int, in TransferChunk) (Transfer, error) {
	if index < 0 || !transferHash.MatchString(in.SHA256) || len(in.Data) == 0 || len(in.Data) > TransferChunkBytes || transferDigest(in.Data) != in.SHA256 {
		return Transfer{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.transferOwner(ctx, tx, a, campID, sessionID, true); err != nil {
		return Transfer{}, err
	}
	if err = s.expireTransfers(ctx, tx, a.UserID); err != nil {
		return Transfer{}, err
	}
	t, err := readTransfer(ctx, tx, a, campID, sessionID, id)
	if err != nil {
		return t, err
	}
	if !oneOf(t.State, "uploading", "ready") {
		return t, ErrTransition
	}
	if index >= (t.PayloadBytes+TransferChunkBytes-1)/TransferChunkBytes {
		return t, ErrInput
	}
	size := min(TransferChunkBytes, t.PayloadBytes-index*TransferChunkBytes)
	if size != len(in.Data) {
		return t, ErrInput
	}
	var old string
	err = tx.QueryRow(ctx, `SELECT sha256 FROM transfer_chunks WHERE transfer_id=$1 AND ordinal=$2`, id, index).Scan(&old)
	if err == nil {
		if old != in.SHA256 {
			return t, ErrConflict
		}
		return t, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return t, err
	}
	if t.State != "uploading" {
		return t, ErrTransition
	}
	encrypted, err := s.sealProvider(in.Data, transferBinding(t, index))
	if err != nil {
		return t, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO transfer_chunks(transfer_id,ordinal,sha256,size,encrypted_data) VALUES($1,$2,$3,$4,$5)`, id, index, in.SHA256, len(in.Data), encrypted); err != nil {
		return t, err
	}
	if _, err = tx.Exec(ctx, `UPDATE session_transfers SET uploaded_bytes=uploaded_bytes+$2,revision=revision+1,updated_at=now() WHERE id=$1`, id, len(in.Data)); err != nil {
		return t, err
	}
	t, err = readTransfer(ctx, tx, a, campID, sessionID, id)
	if err != nil {
		return t, err
	}
	return t, tx.Commit(ctx)
}

func (s *Store) CompleteTransfer(ctx context.Context, a auth.Actor, campID, sessionID, id string) (Transfer, error) {
	select {
	case s.transferSlots <- struct{}{}:
		defer func() { <-s.transferSlots }()
	case <-ctx.Done():
		return Transfer{}, ctx.Err()
	default:
		return Transfer{}, auth.ErrRateLimited
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback(ctx)
	session, err := s.transferOwner(ctx, tx, a, campID, sessionID, true)
	if err != nil {
		return Transfer{}, err
	}
	if err = s.expireTransfers(ctx, tx, a.UserID); err != nil {
		return Transfer{}, err
	}
	t, err := readTransfer(ctx, tx, a, campID, sessionID, id)
	if err != nil {
		return t, err
	}
	if t.State == "ready" && t.BaseCommit != "" {
		return t, tx.Commit(ctx)
	}
	if !oneOf(t.State, "uploading", "ready") || t.UploadedBytes != t.PayloadBytes {
		return t, ErrTransition
	}
	if err = transferSource(ctx, tx, session, t); err != nil {
		return t, err
	}
	if _, err = s.transferTarget(ctx, tx, a, session, t); err != nil {
		return t, err
	}
	rows, err := tx.Query(ctx, `SELECT ordinal,sha256,encrypted_data FROM transfer_chunks WHERE transfer_id=$1 ORDER BY ordinal`, id)
	if err != nil {
		return t, err
	}
	var payload bytes.Buffer
	payload.Grow(t.PayloadBytes)
	ordinal := 0
	for rows.Next() {
		var index int
		var hash string
		var encrypted []byte
		if err = rows.Scan(&index, &hash, &encrypted); err != nil {
			rows.Close()
			return t, err
		}
		if index != ordinal {
			rows.Close()
			return t, ErrInput
		}
		raw, e := s.openProvider(encrypted, transferBinding(t, index))
		if e != nil || transferDigest(raw) != hash || payload.Len()+len(raw) > t.PayloadBytes {
			rows.Close()
			return t, ErrInput
		}
		payload.Write(raw)
		ordinal++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return t, err
	}
	if payload.Len() != t.PayloadBytes || transferDigest(payload.Bytes()) != t.PayloadSHA256 {
		return t, ErrInput
	}
	manifest, err := transfer.Decode(payload.Bytes())
	if err != nil {
		return t, ErrInput
	}
	review, err := manifest.Digest()
	if err != nil || review != t.ReviewSHA256 {
		return t, ErrInput
	}
	if _, err = tx.Exec(ctx, `UPDATE session_transfers SET state='ready',base_commit=$2,source_branch=$3,file_count=$4,revision=revision+1,updated_at=now() WHERE id=$1`, id, manifest.BaseCommit, manifest.Branch, len(manifest.Files)); err != nil {
		return t, err
	}
	t, err = readTransfer(ctx, tx, a, campID, sessionID, id)
	if err != nil {
		return t, err
	}
	if err = s.emitTransfer(ctx, tx, a, t); err != nil {
		return t, err
	}
	return t, tx.Commit(ctx)
}

func (s *Store) CancelTransfer(ctx context.Context, a auth.Actor, campID, sessionID, id string) (Transfer, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.transferOwner(ctx, tx, a, campID, sessionID, true); err != nil {
		return Transfer{}, err
	}
	t, err := readTransfer(ctx, tx, a, campID, sessionID, id)
	if err != nil {
		return t, err
	}
	if oneOf(t.State, "cancelled", "expired") {
		return t, tx.Commit(ctx)
	}
	if err = s.cancelTransferWorkspaces(ctx, tx, a, t); err != nil {
		return t, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM transfer_chunks WHERE transfer_id=$1`, id); err != nil {
		return t, err
	}
	if _, err = tx.Exec(ctx, `UPDATE session_transfers SET state='cancelled',uploaded_bytes=0,revision=revision+1,updated_at=now() WHERE id=$1`, id); err != nil {
		return t, err
	}
	t, err = readTransfer(ctx, tx, a, campID, sessionID, id)
	if err != nil {
		return t, err
	}
	if err = s.emitTransfer(ctx, tx, a, t); err != nil {
		return t, err
	}
	return t, tx.Commit(ctx)
}

func (s *Store) TransferChunk(ctx context.Context, a auth.Actor, campID, sessionID, id string, index int) (TransferChunk, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TransferChunk{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.transferOwner(ctx, tx, a, campID, sessionID, false); err != nil {
		return TransferChunk{}, err
	}
	if err = s.expireTransfers(ctx, tx, a.UserID); err != nil {
		return TransferChunk{}, err
	}
	t, err := readTransfer(ctx, tx, a, campID, sessionID, id)
	if err != nil {
		return TransferChunk{}, err
	}
	if t.State != "ready" {
		return TransferChunk{}, ErrTransition
	}
	out, err := s.readTransferChunk(ctx, tx, t, index)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
