package camp

import (
	"context"
	"crypto/subtle"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// A provider conversation belongs to this implementation, not to the source
// session used to select its account and project. Requests retain the plan ID.
type PlanningImplementation struct {
	ID                string    `json:"id"`
	ApprovalID        string    `json:"approval_id"`
	PlanID            string    `json:"plan_id"`
	PlanVersion       int64     `json:"plan_version"`
	ProviderSessionID string    `json:"provider_session_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	Requests          []Request `json:"requests"`
}

func (s *Store) implementationAccess(ctx context.Context, tx pgx.Tx, r Request) error {
	var approvalID string
	if err := tx.QueryRow(ctx, `SELECT approval_id FROM planning_implementations WHERE id=$1 AND camp_id=$2 AND plan_id=$3 AND plan_version=$4`, r.ImplementationID, r.CampID, r.PlanID, r.PlanVersion).Scan(&approvalID); err != nil {
		return accessError(err)
	}
	v, err := readPlanningApproval(ctx, tx, r.CampID, r.PlanID, approvalID)
	if err != nil {
		return err
	}
	if v.State != "dispatched" || r.SessionID != v.SessionID || r.OwnerUserID != v.OwnerUserID || r.OwnerDeviceID != v.OwnerDeviceID || r.Mode != "implementation" || !maps.Equal(r.Permissions, v.Permissions) || (r.RequesterUserID != v.CreatorUserID && r.RequesterUserID != v.OwnerUserID) {
		return ErrDenied
	}
	return s.checkPlanningApproval(ctx, tx, v)
}

func (s *Store) planWorkCanStart(ctx context.Context, tx pgx.Tx, r Request, fingerprint string) error {
	if r.Kind != "implementation" {
		return s.planningCanStart(ctx, tx, r)
	}
	if err := s.implementationAccess(ctx, tx, r); err != nil {
		return err
	}
	if r.ExecutionLocation == "local" && (!digest64(fingerprint) || fingerprint != r.ExecutionContext) {
		return ErrInput
	}
	return nil
}

func (s *Store) DispatchPlanningExecution(ctx context.Context, a auth.Actor, campID, planID, id, key string, revision int64) (Request, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return Request{}, err
	}
	v, err := readPlanningApproval(ctx, tx, campID, planID, id)
	if err != nil {
		return Request{}, err
	}
	if a.UserID != v.CreatorUserID && a.UserID != v.OwnerUserID {
		return Request{}, ErrDenied
	}
	var saved Request
	if hit, e := s.beginCommand(ctx, tx, a, key, "planning.execution.dispatch:"+id, revision, &saved); e != nil {
		return saved, e
	} else if hit {
		saved, err = loadRequest(ctx, tx, campID, saved.ID)
		if err == nil {
			err = requestContentAccess(ctx, tx, a, saved)
		}
		saved.LeaseToken = ""
		return saved, err
	}
	if v.Revision != revision {
		return Request{}, ErrRevision
	}
	if v.State != "approved" {
		return Request{}, ErrTransition
	}
	if err = s.checkPlanningApproval(ctx, tx, v); err != nil {
		return Request{}, err
	}
	p, err := s.loadPlanning(ctx, tx, a, campID, planID, v.PlanVersion)
	if err != nil {
		return Request{}, err
	}
	implementationID := uuid.NewString()
	if _, err = tx.Exec(ctx, `INSERT INTO planning_implementations(id,approval_id,camp_id,plan_id,plan_version) VALUES($1,$2,$3,$4,$5)`, implementationID, id, campID, planID, v.PlanVersion); err != nil {
		return Request{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE planning_execution_approvals SET state='dispatched',revision=revision+1 WHERE id=$1`, id); err != nil {
		return Request{}, err
	}
	target, err := s.readSession(ctx, tx, a, campID, v.SessionID)
	if err != nil {
		return Request{}, err
	}
	request, err := s.enqueueRequest(ctx, tx, auth.Actor{UserID: v.CreatorUserID, DeviceID: a.DeviceID}, campID, target, RequestInput{Prompt: v.Prompt, Model: v.Model, Effort: v.Effort, Speed: v.Speed, Sources: p.Version.Sources}, &planRequestLink{PlanID: planID, Version: v.PlanVersion, Ordinal: -1, ImplementationID: implementationID, Permissions: v.Permissions, Approved: true})
	if err != nil {
		return Request{}, err
	}
	request, err = loadRequest(ctx, tx, campID, request.ID)
	if err != nil {
		return Request{}, err
	}
	v.State = "dispatched"
	v.Revision++
	if err = emitPlanningApproval(ctx, tx, a, v); err != nil {
		return Request{}, err
	}
	if err = s.finishCommand(ctx, tx, a, key, request); err != nil {
		return Request{}, err
	}
	return request, tx.Commit(ctx)
}

// Follow-ups retain the provider conversation. Another person's prompt still
// needs ordinary owner approval; the initial plan approval is not a blanket
// permission to send arbitrary future prompts.
func (s *Store) ContinueImplementation(ctx context.Context, a auth.Actor, campID, planID, id, key string, in RequestInput) (Request, error) {
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Speed == "" {
		in.Speed = "standard"
	}
	if in.Prompt == "" || len(in.Prompt) > 65536 || len(in.Model) > 200 || len(in.Sources) > 20 || !oneOf(in.Effort, "", "low", "medium", "high", "xhigh", "max", "ultra") || !oneOf(in.Speed, "standard", "fast", "ultrafast") {
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
	impl, err := s.readImplementation(ctx, tx, a, campID, planID, id)
	if err != nil {
		return Request{}, err
	}
	var request Request
	if hit, e := s.beginCommand(ctx, tx, a, key, "implementation.continue:"+id, in, &request); e != nil {
		return request, e
	} else if hit {
		request, err = loadRequest(ctx, tx, campID, request.ID)
		if err == nil {
			err = requestContentAccess(ctx, tx, a, request)
		}
		request.LeaseToken = ""
		return request, err
	}
	if impl.ProviderSessionID == "" {
		return request, ErrTransition
	}
	v, err := readPlanningApproval(ctx, tx, campID, planID, impl.ApprovalID)
	if err != nil {
		return request, err
	}
	if a.UserID != v.CreatorUserID && a.UserID != v.OwnerUserID {
		return request, ErrDenied
	}
	if err = s.checkPlanningApproval(ctx, tx, v); err != nil {
		return request, err
	}
	p, err := s.loadPlanning(ctx, tx, a, campID, planID, impl.PlanVersion)
	if err != nil {
		return request, err
	}
	target, err := s.readSession(ctx, tx, a, campID, v.SessionID)
	if err != nil {
		return request, err
	}
	if target.Provider == "Claude" && in.Speed == "ultrafast" {
		return request, ErrInput
	}
	// Retain pinned plan context even when the next message supplies no new tags.
	refs := append([]SourceRef(nil), p.Version.Sources...)
	for _, ref := range in.Sources {
		for _, saved := range refs {
			if saved.SessionID == ref.SessionID {
				return request, ErrInput
			}
		}
		refs = append(refs, ref)
	}
	if len(refs) > 20 {
		return request, ErrInput
	}
	in.Sources = refs
	request, err = s.enqueueRequest(ctx, tx, a, campID, target, in, &planRequestLink{PlanID: planID, Version: impl.PlanVersion, Ordinal: -1, ImplementationID: id, Permissions: v.Permissions})
	if err != nil {
		return request, err
	}
	request, err = loadRequest(ctx, tx, campID, request.ID)
	if err != nil {
		return request, err
	}
	if err = s.finishCommand(ctx, tx, a, key, request); err != nil {
		return request, err
	}
	return request, tx.Commit(ctx)
}

func (s *Store) readImplementation(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, planID, id string) (PlanningImplementation, error) {
	var out PlanningImplementation
	if !validID(id) {
		return out, ErrDenied
	}
	err := tx.QueryRow(ctx, `SELECT i.id,i.approval_id,i.plan_id,i.plan_version,COALESCE(i.provider_session_id,''),i.created_at FROM planning_implementations i JOIN planning_execution_approvals a ON a.id=i.approval_id WHERE i.id=$1 AND i.camp_id=$2 AND i.plan_id=$3 AND (a.creator_user_id=$4 OR a.owner_user_id=$4)`, id, campID, planID, a.UserID).Scan(&out.ID, &out.ApprovalID, &out.PlanID, &out.PlanVersion, &out.ProviderSessionID, &out.CreatedAt)
	if err != nil {
		return out, accessError(err)
	}
	p, err := s.loadPlanning(ctx, tx, a, campID, planID, out.PlanVersion)
	if err != nil {
		return out, err
	}
	if err = s.checkPlanningSources(ctx, tx, a, p); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT `+requestColumns+` FROM prompt_requests r JOIN sessions s ON s.id=r.session_id WHERE r.implementation_id=$2 AND `+readableRequestContentSQL+` ORDER BY r.created_at DESC,r.id DESC LIMIT 100`, a.UserID, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	out.Requests = []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return out, err
		}
		r.LeaseToken = ""
		out.Requests = append(out.Requests, r)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Requests) == 0 {
		return out, ErrDenied
	}
	return out, nil
}
func (s *Store) Implementation(ctx context.Context, a auth.Actor, campID, planID, id string) (PlanningImplementation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PlanningImplementation{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return PlanningImplementation{}, err
	}
	out, err := s.readImplementation(ctx, tx, a, campID, planID, id)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ClaimImplementationWork(ctx context.Context, a auth.Actor, campID, key string, sessions []string) (*Request, error) {
	if sessions == nil {
		sessions = []string{}
	}
	return s.claimRequest(ctx, a, campID, key, sessions, "execution-v1")
}

// A provider ID is write-once. Retrying its identical event is safe; a changed
// identity must never redirect later prompts into an unrelated conversation.
func saveImplementationSession(ctx context.Context, tx pgx.Tx, r Request, id string) error {
	var current string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(provider_session_id,'') FROM planning_implementations WHERE id=$1`, r.ImplementationID).Scan(&current); err != nil {
		return err
	}
	if current == id {
		return nil
	}
	if current != "" {
		return ErrConflict
	}
	// Serialize first adoption across Camps sharing one profile. Once bound,
	// the normal importer may discover this conversation without invalidating
	// follow-ups or retries of its original identity.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(profile_id::text || ':' || $2,33)) FROM sessions WHERE id=$1`, r.SessionID, id); err != nil {
		return err
	}
	var used bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
	 SELECT 1 FROM sessions anchor WHERE anchor.profile_id=(SELECT profile_id FROM sessions WHERE id=$1) AND anchor.local_session_id=$2
	 UNION ALL SELECT 1 FROM workspace_runtime runtime WHERE runtime.workspace_id=$3::uuid AND runtime.provider_session_id=$2
	 UNION ALL SELECT 1 FROM planning_implementations other JOIN planning_execution_approvals a ON a.id=other.approval_id
	 WHERE other.id<>$4 AND a.profile_id=(SELECT profile_id FROM sessions WHERE id=$1) AND other.provider_session_id=$2
	)`, r.SessionID, id, nullableID(r.ExecutionWorkspaceID), r.ImplementationID).Scan(&used)
	if err != nil {
		return err
	}
	if used {
		return ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE planning_implementations SET provider_session_id=$2 WHERE id=$1 AND (provider_session_id IS NULL OR provider_session_id=$2)`, r.ImplementationID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) AppendLocalImplementationEvent(ctx context.Context, a auth.Actor, campID, id string, in LocalPlanningEvent) (int64, error) {
	event, raw, err := validStreamEvent(in.Event)
	if err != nil || in.Seq < 1 || !validID(in.LeaseToken) {
		return 0, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return 0, err
	}
	r, err := loadRequest(ctx, tx, campID, id)
	if err != nil {
		return 0, err
	}
	if r.Kind != "implementation" || r.ExecutionLocation != "local" || r.OwnerUserID != a.UserID || r.OwnerDeviceID != a.DeviceID || subtle.ConstantTimeCompare([]byte(r.LeaseToken), []byte(in.LeaseToken)) != 1 {
		return 0, ErrDenied
	}
	seq, err := s.appendRequestEvent(ctx, tx, r, in.StreamEvent, event, raw, "")
	if err != nil {
		return 0, err
	}
	return seq, tx.Commit(ctx)
}
