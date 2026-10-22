package camp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

var ErrTransition = errors.New("request state has changed")

type SourceRef struct {
	SessionID          string   `json:"session_id"`
	TranscriptRevision int64    `json:"transcript_revision"`
	PermissionRevision int64    `json:"permission_revision"`
	MessageIDs         []string `json:"message_ids,omitempty"`
}
type RequestInput struct {
	Prompt  string      `json:"prompt"`
	Model   string      `json:"model"`
	Effort  string      `json:"effort"`
	Speed   string      `json:"speed"`
	Sources []SourceRef `json:"sources"`
}
type Request struct {
	ImplementationID     string                      `json:"implementation_id,omitempty"`
	ProviderSessionID    string                      `json:"provider_session_id,omitempty"`
	ExecutionContext     string                      `json:"execution_context,omitempty"`
	ContentRestricted    bool                        `json:"content_restricted,omitempty"`
	Kind                 string                      `json:"kind"`
	PlanID               string                      `json:"plan_id,omitempty"`
	PlanVersion          int64                       `json:"plan_version,omitempty"`
	PlanStep             int                         `json:"plan_step"`
	ID                   string                      `json:"id"`
	CampID               string                      `json:"camp_id"`
	SessionID            string                      `json:"session_id"`
	RequesterUserID      string                      `json:"requester_user_id"`
	OwnerUserID          string                      `json:"owner_user_id"`
	OwnerDeviceID        string                      `json:"owner_device_id"`
	PermissionRevision   int64                       `json:"permission_revision"`
	Revision             int64                       `json:"revision"`
	Prompt               string                      `json:"prompt"`
	Model                string                      `json:"model"`
	Effort               string                      `json:"effort"`
	Speed                string                      `json:"speed"`
	State                string                      `json:"state"`
	LeaseToken           string                      `json:"lease_token,omitempty"`
	LeaseUntil           *time.Time                  `json:"lease_until,omitempty"`
	CreatedAt            time.Time                   `json:"created_at"`
	Evidence             string                      `json:"evidence,omitempty"`
	Reason               string                      `json:"reason,omitempty"`
	Sources              []SourceRef                 `json:"sources"`
	ExecutionLocation    string                      `json:"execution_location"`
	ExecutionEpoch       int64                       `json:"execution_epoch"`
	ExecutionWorkspaceID string                      `json:"execution_workspace_id,omitempty"`
	Permissions          localcli.PermissionSettings `json:"permissions"`
	Mode                 string                      `json:"mode"`
	RunnerGrantID        string                      `json:"-"`
	ContextRevision      int64                       `json:"context_revision"`
	StreamSeq            int64                       `json:"stream_seq"`
	QuotedSources        []ImportedPlanningSource    `json:"quoted_sources,omitempty"`
	QuotedContextDigest  string                      `json:"quoted_context_digest,omitempty"`
}
type RequestAction struct {
	ExecutionContext    string         `json:"execution_context,omitempty"`
	QuotedContextDigest string         `json:"quoted_context_digest,omitempty"`
	Output              string         `json:"output,omitempty"`
	Usage               *PlanningUsage `json:"usage,omitempty"`
	Action              string         `json:"action"`
	LeaseToken          string         `json:"lease_token,omitempty"`
	Outcome             string         `json:"outcome,omitempty"`
	Evidence            string         `json:"evidence,omitempty"`
}

const requestColumns = `r.id,r.camp_id,r.session_id,r.requester_user_id,r.owner_user_id,r.owner_device_id,r.permission_revision,r.revision,r.prompt,r.model,r.effort,r.speed,r.state,
 COALESCE(r.lease_token::text,''),r.lease_until,r.created_at,r.outcome_evidence,r.reason,r.kind,COALESCE(r.plan_id::text,''),COALESCE(r.plan_version,0),COALESCE(r.plan_step,-1),r.execution_location,r.execution_epoch,COALESCE(r.execution_workspace_id::text,''),r.execution_permissions,r.execution_mode,COALESCE(r.runner_grant_id::text,''),r.context_revision,r.stream_seq,COALESCE(r.implementation_id::text,'')`

func scanRequest(row rowScanner, extra ...any) (Request, error) {
	r := Request{Sources: []SourceRef{}}
	var permissions []byte
	columns := []any{&r.ID, &r.CampID, &r.SessionID, &r.RequesterUserID, &r.OwnerUserID, &r.OwnerDeviceID, &r.PermissionRevision, &r.Revision, &r.Prompt, &r.Model, &r.Effort, &r.Speed, &r.State, &r.LeaseToken, &r.LeaseUntil, &r.CreatedAt, &r.Evidence, &r.Reason, &r.Kind, &r.PlanID, &r.PlanVersion, &r.PlanStep, &r.ExecutionLocation, &r.ExecutionEpoch, &r.ExecutionWorkspaceID, &permissions, &r.Mode, &r.RunnerGrantID, &r.ContextRevision, &r.StreamSeq, &r.ImplementationID}
	err := row.Scan(append(columns, extra...)...)
	if err == nil {
		err = json.Unmarshal(permissions, &r.Permissions)
	}
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
	rows, err := tx.Query(ctx, `SELECT session_id,transcript_revision,permission_revision,message_ids FROM request_sources WHERE request_id=$1 ORDER BY session_id`, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var source SourceRef
		if err = rows.Scan(&source.SessionID, &source.TranscriptRevision, &source.PermissionRevision, &source.MessageIDs); err != nil {
			return r, err
		}
		r.Sources = append(r.Sources, source)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return r, err
	}
	if oneOf(r.Kind, "planning", "implementation") {
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT quoted_sources FROM planning_versions WHERE plan_id=$1 AND version=$2`, r.PlanID, r.PlanVersion).Scan(&raw); err != nil {
			return r, err
		}
		if err = json.Unmarshal(raw, &r.QuotedSources); err != nil {
			return r, err
		}
		r.QuotedContextDigest = quotedContextDigest(r.QuotedSources)
	}
	if r.Kind == "implementation" {
		if err = tx.QueryRow(ctx, `SELECT COALESCE(i.provider_session_id,''),COALESCE(a.execution_context,'') FROM planning_implementations i JOIN planning_execution_approvals a ON a.id=i.approval_id WHERE i.id=$1`, r.ImplementationID).Scan(&r.ProviderSessionID, &r.ExecutionContext); err != nil {
			return r, err
		}
	}

	return r, nil
}

func (s *Store) requestAccess(ctx context.Context, tx pgx.Tx, r Request) (Session, error) {
	requester := auth.Actor{UserID: r.RequesterUserID}
	session, err := s.readSession(ctx, tx, requester, r.CampID, r.SessionID)
	if err != nil {
		return session, err
	}
	if err = requireUnheldSession(ctx, tx, r.SessionID); err != nil {
		return session, err
	}
	var role string
	if err = tx.QueryRow(ctx, `SELECT role FROM memberships WHERE camp_id=$1 AND user_id=$2`, r.CampID, r.RequesterUserID).Scan(&role); err != nil {
		return session, accessError(err)
	}
	if session.HistoryOnly || role == "viewer" || (session.OwnerUserID != r.RequesterUserID && !session.AllowRequests) || session.PermissionRevision != r.PermissionRevision {
		return session, ErrDenied
	}
	if session.OwnerUserID != r.OwnerUserID || session.OwnerDeviceID != r.OwnerDeviceID {
		return session, ErrDenied
	}
	if session.ExecutionLocation != r.ExecutionLocation || session.ExecutionEpoch != r.ExecutionEpoch || session.ExecutionWorkspaceID != r.ExecutionWorkspaceID {
		return session, ErrDenied
	}
	if r.ExecutionLocation == "cloud" {
		if _, err = s.executionTarget(ctx, tx, session); err != nil {
			return session, err
		}
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
		if source.PermissionRevision != ref.PermissionRevision || source.TranscriptRevision < ref.TranscriptRevision || (r.Kind != "implementation" && session.Shared && !source.Shared) {
			return session, ErrDenied
		}
	}
	if oneOf(r.Kind, "planning", "implementation") {
		for _, actor := range []auth.Actor{requester, owner} {
			plan, err := s.loadPlanning(ctx, tx, actor, r.CampID, r.PlanID, r.PlanVersion)
			if err != nil {
				return session, err
			}
			if err = s.checkPlanningSources(ctx, tx, actor, plan); err != nil {
				return session, err
			}
		}
	}

	if r.Kind == "implementation" {
		if err = s.implementationAccess(ctx, tx, r); err != nil {
			return session, err
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
		if err != nil {
			return Request{}, err
		}
		if err = requestContentAccess(ctx, tx, a, current); err != nil {
			return Request{}, err
		}
		current.LeaseToken = ""
		return current, nil
	}
	r, err = s.enqueueRequest(ctx, tx, a, campID, session, input, nil)
	if err != nil {
		return r, err
	}
	if err = s.finishCommand(ctx, tx, a, key, r); err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}

// enqueueRequest shares authorization and persistence between ordinary chat and
// the bounded planning coordinator. Callers hold the Camp transaction lock.
func (s *Store) enqueueRequest(ctx context.Context, tx pgx.Tx, a auth.Actor, campID string, session Session, input RequestInput, planning *planRequestLink) (Request, error) {
	if err := requireUnheldSession(ctx, tx, session.ID); err != nil {
		return Request{}, err
	}
	r := Request{Kind: "chat", PlanStep: -1, ID: uuid.NewString(), CampID: campID, SessionID: session.ID, RequesterUserID: a.UserID, OwnerUserID: session.OwnerUserID, OwnerDeviceID: session.OwnerDeviceID, PermissionRevision: session.PermissionRevision, Revision: 1, Prompt: input.Prompt, Model: input.Model, Effort: input.Effort, Speed: input.Speed, State: "pending_approval", Sources: []SourceRef{}}
	target, err := s.executionTarget(ctx, tx, session)
	if err != nil {
		return Request{}, err
	}
	r.ExecutionLocation, r.ExecutionEpoch, r.ExecutionWorkspaceID, r.Permissions, r.Mode = target.Location, target.Epoch, target.WorkspaceID, target.Permissions, target.Mode
	r.ContextRevision = session.TranscriptRevision
	if planning != nil {
		r.Kind = "planning"
		r.PlanID = planning.PlanID
		r.PlanVersion = planning.Version
		r.PlanStep = planning.Ordinal
		r.Mode = "planning"
		r.Permissions = localcli.PermissionSettings{}
		if planning.ImplementationID != "" {
			r.Kind = "implementation"
			r.Mode = "implementation"
			r.PlanStep = -1
			r.ImplementationID = planning.ImplementationID
			r.Permissions = planning.Permissions
			if planning.Approved {
				r.State = "queued"
			}
		}
	}
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
		if ref.PermissionRevision != 0 && ref.PermissionRevision != source.PermissionRevision {
			return r, ErrDenied
		}
		ref.PermissionRevision = source.PermissionRevision
		if len(ref.MessageIDs) > 0 {
			if _, err = readContextMessages(ctx, tx, ref); err != nil {
				return r, err
			}
		} else {
			ref.MessageIDs = []string{}
		}
		r.Sources = append(r.Sources, ref)
	}
	if _, err := s.requestAccess(ctx, tx, r); err != nil {
		return Request{}, err
	}
	permissions, _ := json.Marshal(r.Permissions)
	err = tx.QueryRow(ctx, `INSERT INTO prompt_requests(id,camp_id,session_id,requester_user_id,owner_user_id,owner_device_id,permission_revision,prompt,model,effort,speed,state,kind,plan_id,plan_version,plan_step,execution_location,execution_epoch,execution_workspace_id,execution_permissions,execution_mode,context_revision,implementation_id)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,'')::uuid,NULLIF($15,0),NULLIF($16,-1),$17,$18,NULLIF($19,'')::uuid,$20,$21,$22,NULLIF($23,'')::uuid) RETURNING created_at`, r.ID, campID, session.ID, a.UserID, r.OwnerUserID, r.OwnerDeviceID, r.PermissionRevision, r.Prompt, r.Model, r.Effort, r.Speed, r.State, r.Kind, r.PlanID, r.PlanVersion, r.PlanStep, r.ExecutionLocation, r.ExecutionEpoch, r.ExecutionWorkspaceID, permissions, r.Mode, r.ContextRevision, r.ImplementationID).Scan(&r.CreatedAt)
	if err != nil {
		return Request{}, err
	}
	for _, ref := range r.Sources {
		if _, err = tx.Exec(ctx, `INSERT INTO request_sources(request_id,session_id,transcript_revision,permission_revision,message_ids) VALUES($1,$2,$3,$4,$5)`, r.ID, ref.SessionID, ref.TranscriptRevision, ref.PermissionRevision, ref.MessageIDs); err != nil {
			return Request{}, err
		}
	}
	if err = emitRequest(ctx, tx, a, r, "request.created", false); err != nil {
		return r, err
	}
	if err = s.cloudActivity(ctx, tx, r); err != nil {
		return r, err
	}
	return r, nil
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
	if err = requestContentAccess(ctx, tx, a, r); err != nil {
		if !errors.Is(err, ErrDenied) {
			return Request{}, err
		}
		r = redactRequestContent(r)
	}
	r.LeaseToken = ""
	return r, tx.Commit(ctx)
}

func (s *Store) ListRequests(ctx context.Context, a auth.Actor, campID string) ([]Request, error) {
	if !validID(campID) {
		return nil, ErrDenied
	}
	rows, err := s.pool.Query(ctx, `SELECT `+requestColumns+`, (`+readableRequestContentSQL+`) FROM prompt_requests r JOIN sessions s ON s.id=r.session_id
	 JOIN memberships m ON m.camp_id=r.camp_id AND m.user_id=$1 WHERE r.camp_id=$2 AND (r.requester_user_id=$1 OR r.owner_user_id=$1)
	 AND (s.shared OR s.owner_user_id=$1) ORDER BY r.created_at DESC,r.id DESC LIMIT 100`, a.UserID, campID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		var visible bool
		r, err := scanRequest(rows, &visible)
		if err != nil {
			return nil, err
		}
		r.LeaseToken = ""
		if !visible {
			r = redactRequestContent(r)
		}
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
	payload := map[string]any{"request_id": r.ID, "session_id": r.SessionID, "state": r.State, "revision": r.Revision, "reason": r.Reason, "automatic": automatic}
	if oneOf(r.Kind, "planning", "implementation") {
		payload["plan_id"] = r.PlanID
		payload["plan_version"] = r.PlanVersion
		payload["plan_step"] = r.PlanStep
		if r.ImplementationID != "" {
			payload["implementation_id"] = r.ImplementationID
		}
	}
	if err := emit(ctx, tx, a, r.CampID, kind, payload); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE events SET session_id=$2,plan_id=NULLIF($3,'')::uuid WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, r.CampID, r.SessionID, r.PlanID)
	if err != nil {
		return err
	}
	return notifyRequest(ctx, tx, a, r)
}

const eligiblePlanningRequestSQL = `(r.kind='chat' OR (r.kind='implementation' AND EXISTS(SELECT 1 FROM planning_implementations i JOIN planning_execution_approvals a ON a.id=i.approval_id JOIN planning_tickets p ON p.id=i.plan_id WHERE i.id=r.implementation_id AND a.state='dispatched' AND p.active_version=i.plan_version)) OR (r.kind='planning' AND EXISTS(SELECT 1 FROM planning_versions v JOIN planning_tickets p ON p.id=v.plan_id WHERE v.plan_id=r.plan_id AND v.version=r.plan_version AND v.state='running' AND p.active_version=v.version)
 AND NOT EXISTS(SELECT 1 FROM prompt_requests b WHERE b.plan_id=r.plan_id AND b.plan_version=r.plan_version AND b.state IN ('failed','cancelled','outcome_unknown'))))`

// ClaimRequest never starts a provider. The worker must durably mark its local
// attempt and acknowledge Start before invoking the CLI. An expired Start is
// held as unknown, never automatically put back on the queue.
func (s *Store) ClaimRequest(ctx context.Context, a auth.Actor, campID, key string) (*Request, error) {
	return s.claimRequest(ctx, a, campID, key, nil, "chat")
}

// A native worker offers only sessions available on this device. An empty
// non-nil list deliberately claims nothing; nil retains the original API.
func (s *Store) ClaimAvailableRequest(ctx context.Context, a auth.Actor, campID, key string, sessions []string) (*Request, error) {
	if sessions == nil {
		sessions = []string{}
	}
	return s.claimRequest(ctx, a, campID, key, sessions, "chat")
}

// New native workers opt into planning only when they can run isolated turns.
// Existing clients retain their chat-only claim contract.
func (s *Store) ClaimAvailableWork(ctx context.Context, a auth.Actor, campID, key string, sessions []string) (*Request, error) {
	if sessions == nil {
		sessions = []string{}
	}
	return s.claimRequest(ctx, a, campID, key, sessions, "all")
}

func (s *Store) claimRequest(ctx context.Context, a auth.Actor, campID, key string, sessions []string, kind string) (*Request, error) {
	if len(sessions) > 200 {
		return nil, ErrInput
	}
	seen := map[string]bool{}
	for _, id := range sessions {
		if !validID(id) || seen[id] {
			return nil, ErrInput
		}
		seen[id] = true
	}
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
	var payload any = a.DeviceID // Preserve receipts from the original unfiltered endpoint.
	if kind != "chat" {
		payload = struct {
			DeviceID string
			Sessions []string
			Kind     string
		}{a.DeviceID, sessions, kind}
	} else if sessions != nil {
		payload = struct {
			DeviceID string
			Sessions []string
		}{a.DeviceID, sessions}
	}
	if hit, err := s.beginCommand(ctx, tx, a, key, "request.claim:"+campID, payload, &result); err != nil {
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
	err = tx.QueryRow(ctx, `SELECT r.id FROM prompt_requests r JOIN sessions s ON s.id=r.session_id JOIN profiles profile ON profile.id=s.profile_id
	 WHERE r.camp_id=$1 AND r.owner_device_id=$2 AND r.owner_user_id=$4 AND (r.kind=$5 OR ($5='all' AND r.kind IN ('chat','planning')) OR $5='execution-v1') AND r.state='queued' AND NOT s.external_owner
	 AND r.execution_location='local' AND s.execution_location='local' AND r.execution_epoch=s.execution_epoch
	 AND `+eligiblePlanningRequestSQL+`
	 AND ($3::uuid[] IS NULL OR r.session_id=ANY($3))
	 AND NOT EXISTS(SELECT 1 FROM prompt_requests running WHERE running.session_id=r.session_id AND running.state IN ('claimed','started','cancel_requested','outcome_unknown'))
	 AND (r.kind='chat' OR NOT EXISTS(SELECT 1 FROM prompt_requests running JOIN sessions rs ON rs.id=running.session_id JOIN profiles rp ON rp.id=rs.profile_id
	 WHERE running.kind IN ('planning','implementation') AND running.owner_device_id=r.owner_device_id AND rp.provider=profile.provider AND running.state IN ('claimed','started','cancel_requested','outcome_unknown')))
	 ORDER BY r.created_at,r.id LIMIT 1 FOR UPDATE OF r SKIP LOCKED`, campID, a.DeviceID, sessions, a.UserID, kind).Scan(&id)
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
	if !oneOf(input.Action, "approve", "start", "heartbeat", "release", "cancel", "finish", "reconcile") || len(input.Evidence) > 1024 || len(input.Output) > 8192 || !validPlanningUsage(input.Usage) {
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
	worker := a.DeviceID == r.OwnerDeviceID && a.UserID == r.OwnerUserID && r.ExecutionLocation == "local"
	if input.Action == "reconcile" && r.ExecutionLocation == "cloud" && a.UserID == r.OwnerUserID && input.LeaseToken == "" {
		input.LeaseToken = r.LeaseToken
		worker = true
	}
	return s.actOnRequest(ctx, tx, a, campID, id, key, input, r, worker, "request.action:"+id)
}

// Device and installation workers share the same transitions after their own authorization checks.
func (s *Store) actOnRequest(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, id, key string, input RequestAction, r Request, worker bool, receiptScope string) (result Request, resultErr error) {
	var err error
	if r.RequesterUserID != a.UserID && r.OwnerUserID != a.UserID {
		return Request{}, ErrDenied
	}
	if _, err = s.readSession(ctx, tx, a, campID, r.SessionID); err != nil {
		return Request{}, err
	}
	contentErr := requestContentAccess(ctx, tx, a, r)
	if contentErr != nil && !errors.Is(contentErr, ErrDenied) {
		return Request{}, contentErr
	}
	if contentErr != nil {
		defer func() { result = redactRequestContent(result) }()
	}

	if input.Action == "start" && r.QuotedContextDigest != "" && input.QuotedContextDigest != r.QuotedContextDigest {
		return r, ErrInput
	}
	leaseValid := false
	if r.LeaseToken != "" && input.LeaseToken == r.LeaseToken {
		leaseValid, err = hasLiveLease(ctx, tx, id)
		if err != nil {
			return r, err
		}
	}
	var prior Request
	if hit, err := s.beginCommand(ctx, tx, a, key, receiptScope, input, &prior); err != nil {
		return r, err
	} else if hit {
		if oneOf(input.Action, "start", "heartbeat") && (r.State != prior.State || !leaseValid) {
			return r, ErrTransition
		}
		if input.Action == "start" {
			if contentErr != nil {
				return Request{}, contentErr
			}
			if err = s.planWorkCanStart(ctx, tx, r, input.ExecutionContext); err != nil {
				return r, err
			}
			if _, err = s.requestAccess(ctx, tx, r); err != nil {
				return r, err
			}
		}
		r.LeaseToken = ""
		return r, nil
	}
	if oneOf(input.Action, "start", "heartbeat", "release", "finish", "reconcile") && !worker {
		return r, ErrDenied
	}
	if oneOf(input.Action, "approve", "start") {
		if contentErr != nil {
			return Request{}, contentErr
		}
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
		if err = s.planWorkCanStart(ctx, tx, r, input.ExecutionContext); err != nil {
			return r, err
		}
		if !leaseValid || r.State != "claimed" {
			return r, ErrTransition
		}
		r.State = "started"
		r.Reason = ""
	case "heartbeat":
		if !leaseValid || !oneOf(r.State, "claimed", "started", "cancel_requested") {
			return r, ErrTransition
		}
	case "release":
		if !leaseValid || r.State != "claimed" {
			return r, ErrTransition
		}
		r.State = "queued"
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
		if r.Kind == "planning" && input.Outcome == "completed" && strings.TrimSpace(input.Output) == "" {
			return r, ErrInput
		}
		r.State = input.Outcome
		r.Evidence = input.Evidence
	case "reconcile":
		if r.State != "outcome_unknown" || input.LeaseToken != r.LeaseToken || strings.TrimSpace(input.Evidence) == "" || !oneOf(input.Outcome, "completed", "failed", "cancelled") {
			return r, ErrTransition
		}
		if r.Kind == "planning" && input.Outcome == "completed" && strings.TrimSpace(input.Output) == "" {
			return r, ErrInput
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
	 lease_until=CASE WHEN $4 IN ('start','heartbeat') THEN clock_timestamp()+interval '60 seconds' WHEN $5 OR $4='release' THEN NULL ELSE lease_until END,
	 lease_token=CASE WHEN $4='release' THEN NULL ELSE lease_token END,
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
	if r.Kind == "implementation" && terminal && input.Outcome == "completed" && r.ProviderSessionID == "" {
		return r, ErrTransition
	}
	if r.Kind == "planning" && terminal {
		if err = s.planningRequestFinished(ctx, tx, a, r, input); err != nil {
			return r, err
		}
	}
	if r.ExecutionLocation == "cloud" && input.Action != "heartbeat" {
		if err = s.cloudActivity(ctx, tx, r); err != nil {
			return r, err
		}
	}
	if r.ExecutionLocation == "cloud" && terminal {
		if err = s.publishRunnerTranscript(ctx, tx, r); err != nil {
			return r, err
		}
		if _, err = tx.Exec(ctx, `UPDATE request_controls SET encrypted_input=NULL,state='resolved' WHERE request_id=$1`, r.ID); err != nil {
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
	 OR ($3::uuid IS NOT NULL AND (requester_user_id=$3 OR owner_user_id=$3))) RETURNING id,session_id,state,revision,kind,COALESCE(plan_id::text,''),COALESCE(plan_version,0),COALESCE(plan_step,-1)`, campID, sessionID, userID)
	if err != nil {
		return err
	}
	var changed []Request
	for rows.Next() {
		r := Request{CampID: campID, Reason: "permission_changed"}
		if err = rows.Scan(&r.ID, &r.SessionID, &r.State, &r.Revision, &r.Kind, &r.PlanID, &r.PlanVersion, &r.PlanStep); err != nil {
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
		if r.Kind == "implementation" {
			r, err = loadRequest(ctx, tx, campID, r.ID)
			if err != nil {
				return err
			}
		}
		if err = emitRequest(ctx, tx, a, r, "request.permission_changed", true); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, r := range changed {
		if r.Kind == "planning" && !seen[r.PlanID] {
			seen[r.PlanID] = true
			if err = s.failPlanning(ctx, tx, a, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) reapCamp(ctx context.Context, tx pgx.Tx, campID string) error {
	rows, err := tx.Query(ctx, `UPDATE prompt_requests SET revision=revision+1,state=CASE WHEN state='claimed' THEN 'queued' ELSE 'outcome_unknown' END,reason='lease_expired',lease_until=NULL
	 WHERE id IN(SELECT id FROM prompt_requests WHERE camp_id=$1 AND state IN ('claimed','started','cancel_requested') AND lease_until<=clock_timestamp() LIMIT 100)
	 RETURNING id,session_id,owner_user_id,state,revision,kind,COALESCE(plan_id::text,''),COALESCE(plan_version,0),COALESCE(plan_step,-1),execution_location`, campID)
	if err != nil {
		return err
	}
	var results []Request
	for rows.Next() {
		r := Request{CampID: campID, Reason: "lease_expired"}
		if err = rows.Scan(&r.ID, &r.SessionID, &r.OwnerUserID, &r.State, &r.Revision, &r.Kind, &r.PlanID, &r.PlanVersion, &r.PlanStep, &r.ExecutionLocation); err != nil {
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
		if r.Kind == "implementation" {
			r, err = loadRequest(ctx, tx, campID, r.ID)
			if err != nil {
				return err
			}
		}
		if err = emitRequest(ctx, tx, auth.Actor{UserID: r.OwnerUserID}, r, "request.lease_expired", true); err != nil {
			return err
		}
		if err = s.cloudActivity(ctx, tx, r); err != nil {
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
