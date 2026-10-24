package camp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

type ControlInput struct {
	ID          string         `json:"id"`
	ApprovalSeq *int64         `json:"approval_seq,omitempty"`
	Control     map[string]any `json:"control"`
}
type RunnerControl struct {
	ID      string         `json:"id"`
	Control map[string]any `json:"control"`
}

type ControlStatus struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
}

// A successful submission is not proof that the provider accepted the input.
// Expose the saved provider acknowledgement separately so native retries can
// keep the same control ID across reconnects and app restarts.
func (s *Store) ControlStatus(ctx context.Context, a auth.Actor, campID, requestID, id string) (ControlStatus, error) {
	out := ControlStatus{ID: id, RequestID: requestID}
	if !validID(id) || !validID(requestID) {
		return out, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return out, err
	}
	r, err := loadRequest(ctx, tx, campID, requestID)
	if err != nil {
		return out, err
	}
	if r.OwnerUserID != a.UserID || (r.ExecutionLocation != "cloud" && r.Kind != "implementation" && !r.LocalContinuation) {
		return out, ErrDenied
	}
	if _, err = s.requestAccess(ctx, tx, r); err != nil {
		return out, err
	}
	var storedState, resolution string
	err = tx.QueryRow(ctx, `SELECT kind,state,resolution FROM request_controls WHERE id=$1 AND request_id=$2`, id, requestID).Scan(&out.Kind, &storedState, &resolution)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrDenied
	}
	if err != nil {
		return out, err
	}
	if resolution != "" {
		out.State = resolution
	} else {
		out.State = "pending"
		if storedState == "resolved" || oneOf(r.State, "completed", "failed", "cancelled", "outcome_unknown") {
			out.State = "unconfirmed"
		}
	}
	return out, nil
}

func (s *Store) cryptControl(id string, raw []byte, seal bool) ([]byte, error) {
	cipher, err := s.receiptCipher()
	if err != nil {
		return nil, err
	}
	if seal {
		nonce := make([]byte, cipher.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			return nil, err
		}
		return cipher.Seal(nonce, nonce, raw, []byte("control:"+id)), nil
	}
	if len(raw) < cipher.NonceSize() {
		return nil, ErrInput
	}
	n := cipher.NonceSize()
	return cipher.Open(nil, raw[:n], raw[n:], []byte("control:"+id))
}

func (s *Store) SendControl(ctx context.Context, a auth.Actor, campID, id string, in ControlInput) (RunnerControl, error) {
	out := RunnerControl{ID: in.ID}
	if !validID(in.ID) {
		return out, ErrInput
	}
	kind, _ := in.Control["type"].(string)
	if !oneOf(kind, "steer", "approval") {
		return out, ErrInput
	}
	for k := range in.Control {
		if !strings.Contains(",type,id,text,allow,decision,answers,", ","+k+",") {
			return out, ErrInput
		}
	}
	raw, err := json.Marshal(in.Control)
	if err != nil || len(raw) > 96<<10 {
		return out, ErrInput
	}
	bound, _ := json.Marshal(in)
	hash := sha256.Sum256(bound)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return out, err
	}
	r, err := loadRequest(ctx, tx, campID, id)
	if err != nil {
		return out, err
	}
	if r.OwnerUserID != a.UserID || (r.ExecutionLocation != "cloud" && r.Kind != "implementation" && !r.LocalContinuation) {
		return out, ErrDenied
	}
	if _, err = s.requestAccess(ctx, tx, r); err != nil {
		return out, err
	}
	var stored []byte
	var requestID string
	err = tx.QueryRow(ctx, `SELECT request_id,input_hash FROM request_controls WHERE id=$1`, in.ID).Scan(&requestID, &stored)
	if err == nil {
		if requestID != id || subtle.ConstantTimeCompare(stored, hash[:]) != 1 {
			return out, ErrConflict
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	live, err := hasLiveLease(ctx, tx, id)
	if err != nil {
		return out, err
	}
	if !live || r.State != "started" {
		return out, ErrTransition
	}
	providerID, _ := in.Control["id"].(string)
	if kind == "steer" {
		text, _ := in.Control["text"].(string)
		if in.ApprovalSeq != nil || providerID != in.ID || strings.TrimSpace(text) == "" || len(text) > 65536 || r.Kind == "planning" {
			return out, ErrInput
		}
	} else {
		if in.ApprovalSeq == nil || *in.ApprovalSeq < 1 {
			return out, ErrInput
		}
		var event []byte
		if err = tx.QueryRow(ctx, `SELECT payload FROM request_stream_events WHERE request_id=$1 AND seq=$2`, id, *in.ApprovalSeq).Scan(&event); err != nil {
			return out, ErrDenied
		}
		var approval map[string]any
		_ = json.Unmarshal(event, &approval)
		if approval["type"] != "approval" || approval["id"] != providerID {
			return out, ErrDenied
		}
		var resolved bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM request_stream_events WHERE request_id=$1 AND seq>$2 AND payload->>'type'='approvalResolved' AND payload->>'requestID'=$3)`, id, *in.ApprovalSeq, providerID).Scan(&resolved); err != nil {
			return out, err
		}
		if resolved {
			return out, ErrTransition
		}
		allow, ok := in.Control["allow"].(bool)
		if !ok || allow && approval["canApprove"] != true {
			return out, ErrInput
		}
		var prior bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM request_controls WHERE request_id=$1 AND approval_seq=$2)`, id, *in.ApprovalSeq).Scan(&prior); err != nil {
			return out, err
		}
		if prior {
			return out, ErrConflict
		}
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM request_controls WHERE request_id=$1`, id).Scan(&count); err != nil {
		return out, err
	}
	if count >= 64 {
		return out, ErrInput
	}
	encrypted, err := s.cryptControl(in.ID, raw, true)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO request_controls(id,request_id,approval_seq,provider_id,kind,encrypted_input,input_hash) VALUES($1,$2,$3,$4,$5,$6,$7)`, in.ID, id, in.ApprovalSeq, providerID, kind, encrypted, hash[:]); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (g *GitStore) RunnerControls(ctx context.Context, token, workspaceID, id string) ([]RunnerControl, error) {
	out := []RunnerControl{}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	r, err := g.lockedRunnerRequest(ctx, tx, token, workspaceID, id)
	if err != nil {
		return out, err
	}
	return g.store.requestControls(ctx, tx, r)
}

func (s *Store) LocalImplementationControls(ctx context.Context, a auth.Actor, campID, id, lease string) ([]RunnerControl, error) {
	if !validID(lease) {
		return nil, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return nil, err
	}
	r, err := loadRequest(ctx, tx, campID, id)
	if err != nil {
		return nil, err
	}
	if (r.Kind != "implementation" && !r.LocalContinuation) || r.ExecutionLocation != "local" || r.OwnerUserID != a.UserID || r.OwnerDeviceID != a.DeviceID || subtle.ConstantTimeCompare([]byte(r.LeaseToken), []byte(lease)) != 1 {
		return nil, ErrDenied
	}
	return s.requestControls(ctx, tx, r)
}

func (s *Store) requestControls(ctx context.Context, tx pgx.Tx, r Request) ([]RunnerControl, error) {
	out := []RunnerControl{}
	if r.State != "started" {
		return out, nil
	}
	live, err := hasLiveLease(ctx, tx, r.ID)
	if err != nil || !live {
		return out, ErrTransition
	}
	if _, err = s.requestAccess(ctx, tx, r); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT id,encrypted_input FROM request_controls WHERE request_id=$1 AND state='pending' ORDER BY created_at,id LIMIT 16`, r.ID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var value RunnerControl
		var encrypted []byte
		if err = rows.Scan(&value.ID, &encrypted); err != nil {
			return nil, err
		}
		raw, e := s.cryptControl(value.ID, encrypted, false)
		if e != nil {
			return nil, e
		}
		if json.Unmarshal(raw, &value.Control) != nil {
			return nil, ErrInput
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *Store) settleControl(ctx context.Context, tx pgx.Tx, r Request, event map[string]any) error {
	kind, _ := event["type"].(string)
	if !oneOf(kind, "steerAccepted", "steerRejected", "approvalResolved", "approvalRejected") {
		return nil
	}
	providerID, _ := event["requestID"].(string)
	rows, err := tx.Query(ctx, `SELECT id,kind,encrypted_input FROM request_controls WHERE request_id=$1 AND provider_id=$2 AND state='pending'`, r.ID, providerID)
	if err != nil {
		return err
	}
	type item struct {
		id, kind string
		raw      []byte
	}
	var items []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.kind, &i.raw); err != nil {
			rows.Close()
			return err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range items {
		if i.kind == "steer" && !oneOf(kind, "steerAccepted", "steerRejected") || i.kind == "approval" && !oneOf(kind, "approvalResolved", "approvalRejected") {
			continue
		}
		text := ""
		if i.kind == "steer" && kind == "steerAccepted" {
			plain, e := s.cryptControl(i.id, i.raw, false)
			if e != nil {
				return e
			}
			var data map[string]any
			if json.Unmarshal(plain, &data) != nil {
				return ErrInput
			}
			text, _ = data["text"].(string)
		}
		resolution := "rejected"
		if oneOf(kind, "steerAccepted", "approvalResolved") {
			resolution = "accepted"
		}
		if _, err = tx.Exec(ctx, `UPDATE request_controls SET state='resolved',encrypted_input=NULL,accepted_text=$2,resolution=$3 WHERE id=$1`, i.id, text, resolution); err != nil {
			return err
		}
		if kind == "approvalRejected" {
			if _, err = tx.Exec(ctx, `UPDATE request_controls SET approval_seq=NULL WHERE id=$1`, i.id); err != nil {
				return err
			}
		}
	}
	return nil
}
