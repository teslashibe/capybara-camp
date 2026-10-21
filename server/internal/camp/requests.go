package camp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

var ErrTransition = errors.New("request state has changed")

type SourceRef struct {
	SessionID          string `json:"session_id"`
	TranscriptRevision int64  `json:"transcript_revision"`
	PermissionRevision int64  `json:"permission_revision"`
}
type RequestInput struct {
	Prompt  string      `json:"prompt"`
	Model   string      `json:"model"`
	Effort  string      `json:"effort"`
	Speed   string      `json:"speed"`
	Sources []SourceRef `json:"sources"`
}
type Request struct {
	ID                 string      `json:"id"`
	CampID             string      `json:"camp_id"`
	SessionID          string      `json:"session_id"`
	RequesterUserID    string      `json:"requester_user_id"`
	OwnerUserID        string      `json:"owner_user_id"`
	OwnerDeviceID      string      `json:"owner_device_id"`
	PermissionRevision int64       `json:"permission_revision"`
	Revision           int64       `json:"revision"`
	Prompt             string      `json:"prompt"`
	Model              string      `json:"model"`
	Effort             string      `json:"effort"`
	Speed              string      `json:"speed"`
	State              string      `json:"state"`
	LeaseToken         string      `json:"lease_token,omitempty"`
	LeaseUntil         *time.Time  `json:"lease_until,omitempty"`
	CreatedAt          time.Time   `json:"created_at"`
	Evidence           string      `json:"evidence,omitempty"`
	Reason             string      `json:"reason,omitempty"`
	Sources            []SourceRef `json:"sources"`
}
type RequestAction struct {
	Action     string `json:"action"`
	LeaseToken string `json:"lease_token,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
}

const requestColumns = `r.id,r.camp_id,r.session_id,r.requester_user_id,r.owner_user_id,r.owner_device_id,r.permission_revision,r.revision,r.prompt,r.model,r.effort,r.speed,r.state,
 COALESCE(r.lease_token::text,''),r.lease_until,r.created_at,r.outcome_evidence,r.reason`

func scanRequest(row rowScanner) (Request, error) {
	r := Request{Sources: []SourceRef{}}
	err := row.Scan(&r.ID, &r.CampID, &r.SessionID, &r.RequesterUserID, &r.OwnerUserID, &r.OwnerDeviceID, &r.PermissionRevision, &r.Revision, &r.Prompt, &r.Model, &r.Effort, &r.Speed, &r.State, &r.LeaseToken, &r.LeaseUntil, &r.CreatedAt, &r.Evidence, &r.Reason)
	return r, err
}
func loadRequest(ctx context.Context, tx pgx.Tx, campID, id string) (Request, error) {
	if !validID(id) {
		return Request{}, ErrDenied
	}
	r, err := scanRequest(tx.QueryRow(ctx, `SELECT `+requestColumns+` FROM prompt_requests r WHERE r.camp_id=$1 AND r.id=$2 FOR UPDATE`, campID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrDenied
	}
	if err != nil {
		return r, err
	}
	rows, err := tx.Query(ctx, `SELECT session_id,transcript_revision,permission_revision FROM request_sources WHERE request_id=$1 ORDER BY session_id`, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var source SourceRef
		if err = rows.Scan(&source.SessionID, &source.TranscriptRevision, &source.PermissionRevision); err != nil {
			return r, err
		}
		r.Sources = append(r.Sources, source)
	}
	return r, rows.Err()
}

func (s *Store) requestAccess(ctx context.Context, tx pgx.Tx, r Request) (Session, error) {
	requester := auth.Actor{UserID: r.RequesterUserID}
	session, err := s.readSession(ctx, tx, requester, r.CampID, r.SessionID)
	if err != nil {
		return session, err
	}
	var role string
	if err = tx.QueryRow(ctx, `SELECT role FROM memberships WHERE camp_id=$1 AND user_id=$2`, r.CampID, r.RequesterUserID).Scan(&role); err != nil {
		return session, accessError(err)
	}
	if role == "viewer" || (session.OwnerUserID != r.RequesterUserID && !session.AllowRequests) || session.PermissionRevision != r.PermissionRevision {
		return session, ErrDenied
	}
	if session.OwnerUserID != r.OwnerUserID || session.OwnerDeviceID != r.OwnerDeviceID {
		return session, ErrDenied
	}
	owner := auth.Actor{UserID: r.OwnerUserID}
	if _, err = s.readSession(ctx, tx, owner, r.CampID, r.SessionID); err != nil {
		return session, err
	}
	if err = tx.QueryRow(ctx, `SELECT role FROM memberships WHERE camp_id=$1 AND user_id=$2`, r.CampID, r.OwnerUserID).Scan(&role); err != nil {
		return session, accessError(err)
	}
	if role == "viewer" {
		return session, ErrDenied
	}
	for _, ref := range r.Sources {
		source, err := s.readSession(ctx, tx, requester, r.CampID, ref.SessionID)
		if err != nil {
			return session, err
		}
		if _, err = s.readSession(ctx, tx, owner, r.CampID, ref.SessionID); err != nil {
			return session, err
		}
		if source.PermissionRevision != ref.PermissionRevision || source.TranscriptRevision < ref.TranscriptRevision || (session.Shared && !source.Shared) {
			return session, ErrDenied
		}
	}
	return session, nil
}

func (s *Store) CreateRequest(ctx context.Context, a auth.Actor, campID, sessionID, key string, input RequestInput) (Request, error) {
	input.Prompt = strings.TrimSpace(input.Prompt)
	if input.Speed == "" {
		input.Speed = "standard"
	}
	if len(input.Prompt) == 0 || len(input.Prompt) > 65536 || len(input.Model) > 200 || len(input.Sources) > 20 || !oneOf(input.Effort, "", "low", "medium", "high", "xhigh", "max", "ultra") || !oneOf(input.Speed, "standard", "fast", "ultrafast") {
		return Request{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return Request{}, err
	}
	session, err := s.readSession(ctx, tx, a, campID, sessionID)
	if err != nil {
		return Request{}, err
	}
	if session.Provider == "Claude" && input.Speed == "ultrafast" {
		return Request{}, ErrInput
	}
	r := Request{}
	if hit, err := s.beginCommand(ctx, tx, a, key, "request.create:"+sessionID, input, &r); err != nil {
		return r, err
	} else if hit {
		current, err := loadRequest(ctx, tx, campID, r.ID)
		current.LeaseToken = ""
		return current, err
	}
	r = Request{ID: uuid.NewString(), CampID: campID, SessionID: sessionID, RequesterUserID: a.UserID, OwnerUserID: session.OwnerUserID, OwnerDeviceID: session.OwnerDeviceID, PermissionRevision: session.PermissionRevision, Revision: 1, Prompt: input.Prompt, Model: input.Model, Effort: input.Effort, Speed: input.Speed, State: "pending_approval", Sources: []SourceRef{}}
	if r.Model == "" {
		r.Model = session.Model
	}
	if a.UserID == session.OwnerUserID {
		r.State = "queued"
	}
	seen := map[string]bool{}
	for _, ref := range input.Sources {
		if seen[ref.SessionID] || ref.TranscriptRevision < 1 {
			return r, ErrInput
		}
		seen[ref.SessionID] = true
		source, err := s.readSession(ctx, tx, a, campID, ref.SessionID)
		if err != nil {
			return r, err
		}
		ref.PermissionRevision = source.PermissionRevision
		r.Sources = append(r.Sources, ref)
	}
	if _, err = s.requestAccess(ctx, tx, r); err != nil {
		return Request{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO prompt_requests(id,camp_id,session_id,requester_user_id,owner_user_id,owner_device_id,permission_revision,prompt,model,effort,speed,state)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING created_at`, r.ID, campID, sessionID, a.UserID, r.OwnerUserID, r.OwnerDeviceID, r.PermissionRevision, r.Prompt, r.Model, r.Effort, r.Speed, r.State).Scan(&r.CreatedAt)
	if err != nil {
		return Request{}, err
	}
	for _, ref := range r.Sources {
		if _, err = tx.Exec(ctx, `INSERT INTO request_sources(request_id,session_id,transcript_revision,permission_revision) VALUES($1,$2,$3,$4)`, r.ID, ref.SessionID, ref.TranscriptRevision, ref.PermissionRevision); err != nil {
			return Request{}, err
		}
	}
	if err = emitRequest(ctx, tx, a, r, "request.created", false); err != nil {
		return r, err
	}
	if err = s.finishCommand(ctx, tx, a, key, r); err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}

func (s *Store) Request(ctx context.Context, a auth.Actor, campID, id string) (Request, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return Request{}, err
	}
	r, err := loadRequest(ctx, tx, campID, id)
	if err != nil {
		return r, err
	}
	if r.RequesterUserID != a.UserID && r.OwnerUserID != a.UserID {
		return Request{}, ErrDenied
	}
	if _, err = s.readSession(ctx, tx, a, campID, r.SessionID); err != nil {
		return Request{}, err
	}
	r.LeaseToken = ""
	return r, tx.Commit(ctx)
}

func (s *Store) ListRequests(ctx context.Context, a auth.Actor, campID string) ([]Request, error) {
	if !validID(campID) {
		return nil, ErrDenied
	}
	rows, err := s.pool.Query(ctx, `SELECT `+requestColumns+` FROM prompt_requests r JOIN sessions s ON s.id=r.session_id
	 JOIN memberships m ON m.camp_id=r.camp_id AND m.user_id=$1 WHERE r.camp_id=$2 AND (r.requester_user_id=$1 OR r.owner_user_id=$1)
	 AND (s.shared OR s.owner_user_id=$1) ORDER BY r.created_at DESC,r.id DESC LIMIT 100`, a.UserID, campID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		r.LeaseToken = ""
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	var member bool
	if err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE camp_id=$1 AND user_id=$2)`, campID, a.UserID).Scan(&member); err != nil {
		return nil, err
	}
	if !member {
		return nil, ErrDenied
	}
	return out, nil
}

func emitRequest(ctx context.Context, tx pgx.Tx, a auth.Actor, r Request, kind string, automatic bool) error {
	if err := emit(ctx, tx, a, r.CampID, kind, map[string]any{"request_id": r.ID, "session_id": r.SessionID, "state": r.State, "revision": r.Revision, "reason": r.Reason, "automatic": automatic}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE events SET session_id=$2 WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, r.CampID, r.SessionID)
	return err
}

// ClaimRequest never starts a provider. The worker must durably mark its local
// attempt and acknowledge Start before invoking the CLI. An expired Start is
// held as unknown, never automatically put back on the queue.
func (s *Store) ClaimRequest(ctx context.Context, a auth.Actor, campID, key string) (*Request, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockWorker(ctx, tx, a); err != nil {
		return nil, err
	}
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return nil, err
	}
	if err = s.reapCamp(ctx, tx, campID); err != nil {
		return nil, err
	}
	var result *Request
	if hit, err := s.beginCommand(ctx, tx, a, key, "request.claim:"+campID, a.DeviceID, &result); err != nil {
		return nil, err
	} else if hit {
		if result == nil {
			return nil, nil
		}
		current, err := loadRequest(ctx, tx, campID, result.ID)
		if err != nil {
			return nil, err
		}
		valid, err := hasLiveLease(ctx, tx, current.ID)
		if err != nil {
			return nil, err
		}
		if current.State != "claimed" || current.LeaseToken != result.LeaseToken || !valid {
			return nil, ErrTransition
		}
		if _, err = s.requestAccess(ctx, tx, current); err != nil {
			return nil, err
		}
		return &current, nil
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT r.id FROM prompt_requests r JOIN sessions s ON s.id=r.session_id
	 WHERE r.camp_id=$1 AND r.owner_device_id=$2 AND r.state='queued' AND NOT s.external_owner
	 AND NOT EXISTS(SELECT 1 FROM prompt_requests running WHERE running.session_id=r.session_id AND running.state IN ('claimed','started','cancel_requested','outcome_unknown'))
	 ORDER BY r.created_at,r.id LIMIT 1 FOR UPDATE OF r SKIP LOCKED`, campID, a.DeviceID).Scan(&id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		r, err := loadRequest(ctx, tx, campID, id)
		if err != nil {
			return nil, err
		}
		if _, err = s.requestAccess(ctx, tx, r); err != nil {
			if !errors.Is(err, ErrDenied) {
				return nil, err
			}
			r.State = "cancelled"
			r.Reason = "permission_changed"
			if _, err = tx.Exec(ctx, `UPDATE prompt_requests SET revision=revision+1,state='cancelled',reason=$2,finished_at=now() WHERE id=$1`, r.ID, r.Reason); err != nil {
				return nil, err
			}
			r.Revision++
			if err = emitRequest(ctx, tx, a, r, "request.cancelled", true); err != nil {
				return nil, err
			}
		} else {
			r.State = "claimed"
			r.LeaseToken = uuid.NewString()
			r.Reason = ""
			err = tx.QueryRow(ctx, `UPDATE prompt_requests SET revision=revision+1,state='claimed',reason='',lease_token=$2,lease_until=clock_timestamp()+interval '60 seconds' WHERE id=$1 RETURNING lease_until,revision`, r.ID, r.LeaseToken).Scan(&r.LeaseUntil, &r.Revision)
			if err != nil {
				return nil, err
			}
			if err = emitRequest(ctx, tx, a, r, "request.claimed", false); err != nil {
				return nil, err
			}
			result = &r
		}
	}
	if err = s.finishCommand(ctx, tx, a, key, result); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) ActOnRequest(ctx context.Context, a auth.Actor, campID, id, key string, input RequestAction) (Request, error) {
	if !oneOf(input.Action, "approve", "start", "heartbeat", "cancel", "finish", "reconcile") || len(input.Evidence) > 1024 {
		return Request{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockWorker(ctx, tx, a); err != nil {
		return Request{}, err
	}
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return Request{}, err
	}
	if err = s.reapCamp(ctx, tx, campID); err != nil {
		return Request{}, err
	}
	r, err := loadRequest(ctx, tx, campID, id)
	if err != nil {
		return r, err
	}
	if r.RequesterUserID != a.UserID && r.OwnerUserID != a.UserID {
		return Request{}, ErrDenied
	}
	if _, err = s.readSession(ctx, tx, a, campID, r.SessionID); err != nil {
		return Request{}, err
	}
	leaseValid := false
	if r.LeaseToken != "" && input.LeaseToken == r.LeaseToken {
		leaseValid, err = hasLiveLease(ctx, tx, id)
		if err != nil {
			return r, err
		}
	}
	var prior Request
	if hit, err := s.beginCommand(ctx, tx, a, key, "request.action:"+id, input, &prior); err != nil {
		return r, err
	} else if hit {
		if oneOf(input.Action, "start", "heartbeat") && (r.State != prior.State || !leaseValid) {
			return r, ErrTransition
		}
		if input.Action == "start" {
			if _, err = s.requestAccess(ctx, tx, r); err != nil {
				return r, err
			}
		}
		r.LeaseToken = ""
		return r, nil
	}
	worker := a.DeviceID == r.OwnerDeviceID && a.UserID == r.OwnerUserID
	if oneOf(input.Action, "start", "heartbeat", "finish", "reconcile") && !worker {
		return r, ErrDenied
	}
	if oneOf(input.Action, "approve", "start") {
		session, err := s.requestAccess(ctx, tx, r)
		if err != nil {
			return r, err
		}
		if input.Action == "start" && session.ExternalOwner {
			return r, ErrTransition
		}
	}
	previousState, previousEvidence, previousReason := r.State, r.Evidence, r.Reason
	switch input.Action {
	case "approve":
		if a.UserID != r.OwnerUserID {
			return r, ErrDenied
		}
		if r.State != "pending_approval" {
			return r, ErrTransition
		}
		r.State = "queued"
	case "start":
		if !leaseValid || r.State != "claimed" {
			return r, ErrTransition
		}
		r.State = "started"
		r.Reason = ""
	case "heartbeat":
		if !leaseValid || !oneOf(r.State, "claimed", "started", "cancel_requested") {
			return r, ErrTransition
		}
	case "cancel":
		if oneOf(r.State, "queued", "pending_approval", "claimed") {
			r.State = "cancelled"
		} else if oneOf(r.State, "started", "cancel_requested") {
			r.State = "cancel_requested"
		} else if !oneOf(r.State, "cancelled", "completed", "failed", "outcome_unknown") {
			return r, ErrTransition
		}
		if r.State == "outcome_unknown" {
			r.Reason = "cancel_requested"
		}
	case "finish":
		if !leaseValid || !oneOf(r.State, "started", "cancel_requested") {
			return r, ErrTransition
		}
		if !oneOf(input.Outcome, "completed", "failed", "cancelled") || strings.TrimSpace(input.Evidence) == "" {
			return r, ErrInput
		}
		r.State = input.Outcome
		r.Evidence = input.Evidence
	case "reconcile":
		if r.State != "outcome_unknown" || input.LeaseToken != r.LeaseToken || strings.TrimSpace(input.Evidence) == "" || !oneOf(input.Outcome, "completed", "failed", "cancelled") {
			return r, ErrTransition
		}
		r.State = input.Outcome
		r.Evidence = input.Evidence
	}
	if input.Action != "heartbeat" && r.State == previousState && r.Evidence == previousEvidence && r.Reason == previousReason {
		r.LeaseToken = ""
		if err = s.finishCommand(ctx, tx, a, key, r); err != nil {
			return r, err
		}
		return r, tx.Commit(ctx)
	}
	terminal := oneOf(r.State, "completed", "failed", "cancelled")
	_, err = tx.Exec(ctx, `UPDATE prompt_requests SET revision=revision+1,state=$2,outcome_evidence=$3,
	 started_at=CASE WHEN $4='start' THEN now() ELSE started_at END,
	 lease_until=CASE WHEN $4 IN ('start','heartbeat') THEN clock_timestamp()+interval '60 seconds' WHEN $5 THEN NULL ELSE lease_until END,
	 finished_at=CASE WHEN $5 THEN now() ELSE finished_at END,reason=$6 WHERE id=$1`, id, r.State, r.Evidence, input.Action, terminal, r.Reason)
	if err != nil {
		return r, err
	}
	r, err = loadRequest(ctx, tx, campID, id)
	if err != nil {
		return r, err
	}
	if input.Action != "heartbeat" {
		if err = emitRequest(ctx, tx, a, r, "request."+r.State, false); err != nil {
			return r, err
		}
	}
	r.LeaseToken = ""
	if err = s.finishCommand(ctx, tx, a, key, r); err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}

func accessError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDenied
	}
	return err
}

func lockWorker(ctx context.Context, tx pgx.Tx, a auth.Actor) error {
	if !validID(a.DeviceID) {
		return auth.ErrUnauthorized
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM devices WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now() FOR SHARE`, a.DeviceID, a.UserID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrUnauthorized
	}
	return err
}

func (s *Store) invalidateSessionRequests(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, sessionID string) error {
	return s.invalidateRequests(ctx, tx, a, campID, sessionID, nil)
}
func (s *Store) invalidateMemberRequests(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, userID string) error {
	return s.invalidateRequests(ctx, tx, a, campID, nil, userID)
}
func (s *Store) invalidateRequests(ctx context.Context, tx pgx.Tx, a auth.Actor, campID string, sessionID, userID any) error {
	rows, err := tx.Query(ctx, `UPDATE prompt_requests SET revision=revision+1,state=CASE WHEN state IN ('started','cancel_requested') THEN 'cancel_requested' WHEN state='outcome_unknown' THEN state ELSE 'cancelled' END,
	 reason='permission_changed',finished_at=CASE WHEN state IN ('pending_approval','queued','claimed') THEN now() ELSE finished_at END
	 WHERE camp_id=$1 AND state IN ('pending_approval','queued','claimed','started','cancel_requested','outcome_unknown')
	 AND (($2::uuid IS NOT NULL AND (session_id=$2 OR id IN(SELECT request_id FROM request_sources WHERE session_id=$2)))
	 OR ($3::uuid IS NOT NULL AND (requester_user_id=$3 OR owner_user_id=$3))) RETURNING id,session_id,state,revision`, campID, sessionID, userID)
	if err != nil {
		return err
	}
	var changed []Request
	for rows.Next() {
		r := Request{CampID: campID, Reason: "permission_changed"}
		if err = rows.Scan(&r.ID, &r.SessionID, &r.State, &r.Revision); err != nil {
			rows.Close()
			return err
		}
		changed = append(changed, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range changed {
		if err = emitRequest(ctx, tx, a, r, "request.permission_changed", true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) reapCamp(ctx context.Context, tx pgx.Tx, campID string) error {
	rows, err := tx.Query(ctx, `UPDATE prompt_requests SET revision=revision+1,state=CASE WHEN state='claimed' THEN 'queued' ELSE 'outcome_unknown' END,reason='lease_expired',lease_until=NULL
	 WHERE id IN(SELECT id FROM prompt_requests WHERE camp_id=$1 AND state IN ('claimed','started','cancel_requested') AND lease_until<=clock_timestamp() LIMIT 100)
	 RETURNING id,session_id,owner_user_id,state,revision`, campID)
	if err != nil {
		return err
	}
	var results []Request
	for rows.Next() {
		r := Request{CampID: campID, Reason: "lease_expired"}
		if err = rows.Scan(&r.ID, &r.SessionID, &r.OwnerUserID, &r.State, &r.Revision); err != nil {
			rows.Close()
			return err
		}
		results = append(results, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range results {
		if err = emitRequest(ctx, tx, auth.Actor{UserID: r.OwnerUserID}, r, "request.lease_expired", true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ReapRequests(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT camp_id FROM prompt_requests WHERE state IN ('claimed','started','cancel_requested') AND lease_until<=clock_timestamp() LIMIT 50`)
	if err != nil {
		return err
	}
	var camps []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		camps = append(camps, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range camps {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `SELECT id FROM camps WHERE id=$1 FOR UPDATE`, id); err == nil {
			err = s.reapCamp(ctx, tx, id)
		}
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func hasLiveLease(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT COALESCE(lease_until>clock_timestamp(),false) FROM prompt_requests WHERE id=$1`, id).Scan(&valid)
	return valid, err
}
