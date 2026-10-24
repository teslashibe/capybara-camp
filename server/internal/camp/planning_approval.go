package camp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

// These approvals bind an executor source to a plan. They do not enqueue work.
// Dispatch must consume the approval and establish its implementation session
// atomically, rechecking this binding immediately before provider submission.
type PlanningApprovalInput struct {
	ExpectedRevision           int64                       `json:"expected_revision"`
	ExpectedVersion            int64                       `json:"expected_version"`
	Executor                   PlannerInput                `json:"executor"`
	ExpectedSessionRevision    int64                       `json:"expected_session_revision"`
	ExpectedTranscriptRevision int64                       `json:"expected_transcript_revision"`
	Permissions                localcli.PermissionSettings `json:"permissions"`
}
type PlanningApproval struct {
	ID                 string                      `json:"id"`
	CampID             string                      `json:"camp_id"`
	PlanID             string                      `json:"plan_id"`
	PlanVersion        int64                       `json:"plan_version"`
	PlanRevision       int64                       `json:"plan_revision"`
	PlanHash           string                      `json:"plan_sha256"`
	CreatorUserID      string                      `json:"creator_user_id"`
	OwnerUserID        string                      `json:"owner_user_id"`
	OwnerDeviceID      string                      `json:"owner_device_id"`
	SessionID          string                      `json:"session_id"`
	ProfileID          string                      `json:"profile_id"`
	SessionRevision    int64                       `json:"session_revision"`
	TranscriptRevision int64                       `json:"transcript_revision"`
	PermissionRevision int64                       `json:"permission_revision"`
	Provider           string                      `json:"provider"`
	Model              string                      `json:"model"`
	Effort             string                      `json:"effort"`
	Speed              string                      `json:"speed"`
	Execution          PlanningExecution           `json:"execution"`
	Permissions        localcli.PermissionSettings `json:"permissions"`
	ExecutionContext   string                      `json:"execution_context,omitempty"`
	Prompt             string                      `json:"prompt"`
	State              string                      `json:"state"`
	Revision           int64                       `json:"revision"`
	ApprovedByDeviceID string                      `json:"approved_by_device_id,omitempty"`
	CreatedAt          time.Time                   `json:"created_at"`
	ApprovedAt         *time.Time                  `json:"approved_at,omitempty"`
}
type PlanningApprovalAction struct {
	ExpectedRevision int64  `json:"expected_revision"`
	ExecutionContext string `json:"execution_context,omitempty"`
}

// A plan's creator may inspect the requested executor's prepared permissions
// before proposing consent. This exposes no provider credentials or workspace
// checkout details and does not grant execution or change its assignment.
func (s *Store) PlanningExecutorTarget(ctx context.Context, a auth.Actor, campID, planID, sessionID string) (ExecutionTarget, error) {
	return retryRead(ctx, func() (ExecutionTarget, error) {
		return s.planningExecutorTargetOnce(ctx, a, campID, planID, sessionID)
	})
}

func (s *Store) planningExecutorTargetOnce(ctx context.Context, a auth.Actor, campID, planID, sessionID string) (ExecutionTarget, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return ExecutionTarget{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return ExecutionTarget{}, err
	}
	p, err := s.loadPlanning(ctx, tx, a, campID, planID, 0)
	if err != nil {
		return ExecutionTarget{}, err
	}
	if err = s.checkPlanningSources(ctx, tx, a, p); err != nil {
		return ExecutionTarget{}, err
	}
	target, err := s.readSession(ctx, tx, a, campID, sessionID)
	if err != nil {
		return ExecutionTarget{}, err
	}
	if a.UserID != p.CreatorUserID && a.UserID != target.OwnerUserID || target.ProjectID != p.ProjectID || target.HistoryOnly || target.ExternalOwner || a.UserID != target.OwnerUserID && !target.AllowRequests {
		return ExecutionTarget{}, ErrDenied
	}
	out, err := s.executionTarget(ctx, tx, target)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func planApprovalHash(p PlanningTicket) (string, error) {
	raw, err := json.Marshal(p.Version)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func readPlanningApproval(ctx context.Context, tx pgx.Tx, campID, planID, id string) (PlanningApproval, error) {
	var v PlanningApproval
	if !validID(id) || !validID(planID) {
		return v, ErrDenied
	}
	var permissions []byte
	err := tx.QueryRow(ctx, `SELECT id,camp_id,plan_id,plan_version,plan_revision,plan_sha256,creator_user_id,owner_user_id,owner_device_id,session_id,profile_id,session_revision,transcript_revision,permission_revision,provider,model,effort,speed,execution_location,execution_epoch,COALESCE(execution_workspace_id::text,''),permissions,COALESCE(execution_context,''),prompt,state,revision,COALESCE(approved_by_device_id::text,''),created_at,approved_at FROM planning_execution_approvals WHERE camp_id=$1 AND plan_id=$2 AND id=$3`, campID, planID, id).Scan(&v.ID, &v.CampID, &v.PlanID, &v.PlanVersion, &v.PlanRevision, &v.PlanHash, &v.CreatorUserID, &v.OwnerUserID, &v.OwnerDeviceID, &v.SessionID, &v.ProfileID, &v.SessionRevision, &v.TranscriptRevision, &v.PermissionRevision, &v.Provider, &v.Model, &v.Effort, &v.Speed, &v.Execution.Location, &v.Execution.Epoch, &v.Execution.WorkspaceID, &permissions, &v.ExecutionContext, &v.Prompt, &v.State, &v.Revision, &v.ApprovedByDeviceID, &v.CreatedAt, &v.ApprovedAt)
	if err != nil {
		return v, accessError(err)
	}
	err = json.Unmarshal(permissions, &v.Permissions)
	return v, err
}
func (s *Store) checkPlanningApproval(ctx context.Context, tx pgx.Tx, v PlanningApproval) error {
	// Review, approval and dispatch pin the exact ticket, session and
	// transcript the owner saw. After dispatch, board moves, chat in the anchor
	// and ticket edits must not strand the work; the plan hash, membership,
	// ownership, permission revision and execution target still bind it.
	dispatched := v.State == "dispatched"
	for _, user := range []string{v.CreatorUserID, v.OwnerUserID} {
		actor := auth.Actor{UserID: user}
		var role string
		if err := tx.QueryRow(ctx, `SELECT role FROM memberships WHERE camp_id=$1 AND user_id=$2`, v.CampID, user).Scan(&role); err != nil {
			return accessError(err)
		}
		if role == "viewer" {
			return ErrDenied
		}
		p, err := s.loadPlanning(ctx, tx, actor, v.CampID, v.PlanID, v.PlanVersion)
		if err != nil {
			return err
		}
		if p.CreatorUserID != v.CreatorUserID || !dispatched && p.Revision != v.PlanRevision || p.ActiveVersion != v.PlanVersion || p.Version.State != "ready" || p.Version.Imported != nil {
			return ErrRevision
		}
		hash, err := planApprovalHash(p)
		if err != nil {
			return err
		}
		if hash != v.PlanHash {
			return ErrRevision
		}
		if err = s.checkPlanningSources(ctx, tx, actor, p); err != nil {
			return err
		}
		target, err := s.readSession(ctx, tx, actor, v.CampID, v.SessionID)
		if err != nil {
			return err
		}
		if target.HistoryOnly || target.ExternalOwner || target.ProjectID != p.ProjectID || target.OwnerUserID != v.OwnerUserID || target.OwnerDeviceID != v.OwnerDeviceID || target.ProfileID != v.ProfileID || target.PermissionRevision != v.PermissionRevision || !dispatched && (target.Revision != v.SessionRevision || target.TranscriptRevision != v.TranscriptRevision) || planningExecution(target) != v.Execution {
			return ErrRevision
		}
		if user != target.OwnerUserID && !target.AllowRequests {
			return ErrDenied
		}
		if err = requireUnheldSession(ctx, tx, target.ID); err != nil {
			return err
		}
		current, err := s.executionTarget(ctx, tx, target)
		if err != nil {
			return err
		}
		if current.Location == "cloud" && (current.Mode != "implementation" || !maps.Equal(current.Permissions, v.Permissions)) {
			return ErrRevision
		}
	}
	rows, err := tx.Query(ctx, `SELECT session_id,permission_revision,transcript_revision FROM planning_execution_sources WHERE approval_id=$1`, v.ID)
	if err != nil {
		return err
	}
	var refs []SourceRef
	for rows.Next() {
		var ref SourceRef
		if err = rows.Scan(&ref.SessionID, &ref.PermissionRevision, &ref.TranscriptRevision); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		for _, user := range []string{v.CreatorUserID, v.OwnerUserID} {
			source, err := s.readSession(ctx, tx, auth.Actor{UserID: user}, v.CampID, ref.SessionID)
			if err != nil {
				return err
			}
			if source.PermissionRevision != ref.PermissionRevision || source.TranscriptRevision < ref.TranscriptRevision {
				return ErrDenied
			}
		}
	}
	return nil
}
func emitPlanningApproval(ctx context.Context, tx pgx.Tx, a auth.Actor, v PlanningApproval) error {
	recipients := []string{v.CreatorUserID}
	if v.OwnerUserID != v.CreatorUserID {
		recipients = append(recipients, v.OwnerUserID)
	}
	for _, user := range recipients {
		if err := emit(ctx, tx, a, v.CampID, "planning.execution_approval", map[string]any{"approval_id": v.ID, "plan_id": v.PlanID, "version": v.PlanVersion, "state": v.State, "revision": v.Revision}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE events SET plan_id=$2,recipient_user_id=$3 WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, v.CampID, v.PlanID, user); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) PreparePlanningApproval(ctx context.Context, a auth.Actor, campID, planID, key string, in PlanningApprovalInput) (PlanningApproval, error) {
	var out PlanningApproval
	if in.ExpectedRevision < 1 || in.ExpectedVersion < 1 || in.ExpectedSessionRevision < 1 || in.ExpectedTranscriptRevision < 0 || in.Executor.Execution == nil || !in.Executor.Execution.valid() || strings.TrimSpace(in.Executor.Model) == "" || len(in.Executor.Model) > 200 || !oneOf(in.Executor.Effort, "", "low", "medium", "high", "xhigh", "max", "ultra") || !oneOf(in.Executor.Speed, "standard", "fast", "ultrafast") {
		return out, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return out, err
	}
	if hit, e := s.beginCommand(ctx, tx, a, key, "planning.execution.prepare:"+planID, in, &out); e != nil {
		return out, e
	} else if hit {
		out, err = readPlanningApproval(ctx, tx, campID, planID, out.ID)
		if err == nil {
			err = s.checkPlanningApproval(ctx, tx, out)
		}
		return out, err
	}
	p, err := s.loadPlanning(ctx, tx, a, campID, planID, 0)
	if err != nil {
		return out, err
	}
	if p.CreatorUserID != a.UserID {
		return out, ErrDenied
	}
	if p.Revision != in.ExpectedRevision || p.ActiveVersion != in.ExpectedVersion {
		return out, ErrRevision
	}
	if p.Version.State != "ready" || p.Version.Imported != nil || len(p.Version.Steps) != p.Version.CallLimit {
		return out, ErrTransition
	}
	for _, step := range p.Version.Steps {
		if step.State != "completed" || strings.TrimSpace(step.Output) == "" {
			return out, ErrTransition
		}
	}
	target, err := s.readSession(ctx, tx, a, campID, in.Executor.SessionID)
	if err != nil {
		return out, err
	}
	if target.Provider == "Claude" && in.Executor.Speed == "ultrafast" {
		return out, ErrInput
	}
	permissions, err := localcli.NormalizePermissions(target.Provider, in.Permissions)
	if err != nil {
		return out, ErrInput
	}
	hash, err := planApprovalHash(p)
	if err != nil {
		return out, err
	}
	out = PlanningApproval{ID: uuid.NewString(), CampID: campID, PlanID: planID, PlanVersion: p.ActiveVersion, PlanRevision: p.Revision, PlanHash: hash, CreatorUserID: a.UserID, OwnerUserID: target.OwnerUserID, OwnerDeviceID: target.OwnerDeviceID, SessionID: target.ID, ProfileID: target.ProfileID, SessionRevision: in.ExpectedSessionRevision, TranscriptRevision: in.ExpectedTranscriptRevision, PermissionRevision: target.PermissionRevision, Provider: target.Provider, Model: in.Executor.Model, Effort: in.Executor.Effort, Speed: in.Executor.Speed, Execution: *in.Executor.Execution, Permissions: permissions, State: "pending", Revision: 1}
	out.Prompt = fmt.Sprintf("Implement the approved plan below using the selected project permissions. Preserve the plan's unresolved decisions and ask before proceeding where approval is still required.\n\nBrief:\n%s\n\nApproved plan:\n%s", p.Version.Brief, p.Version.Steps[len(p.Version.Steps)-1].Output)
	// A Camp lock serializes attempts; return an ordinary conflict instead of a
	// database uniqueness error when a second approval is already outstanding.
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM planning_execution_approvals WHERE plan_id=$1 AND state IN ('pending','approved')) OR EXISTS(SELECT 1 FROM prompt_requests WHERE plan_id=$1 AND kind='implementation' AND state IN ('pending_approval','queued','claimed','started','cancel_requested','outcome_unknown'))`, planID).Scan(&active); err != nil {
		return out, err
	}
	if active {
		return out, ErrConflict
	}
	raw, _ := json.Marshal(permissions)
	err = tx.QueryRow(ctx, `INSERT INTO planning_execution_approvals(id,camp_id,plan_id,plan_version,plan_revision,plan_sha256,creator_user_id,owner_user_id,owner_device_id,session_id,profile_id,session_revision,transcript_revision,permission_revision,provider,model,effort,speed,execution_location,execution_epoch,execution_workspace_id,permissions,prompt) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,NULLIF($21,'')::uuid,$22,$23) RETURNING created_at`, out.ID, campID, planID, out.PlanVersion, out.PlanRevision, hash, a.UserID, out.OwnerUserID, out.OwnerDeviceID, out.SessionID, out.ProfileID, out.SessionRevision, out.TranscriptRevision, out.PermissionRevision, out.Provider, out.Model, out.Effort, out.Speed, out.Execution.Location, out.Execution.Epoch, out.Execution.WorkspaceID, raw, out.Prompt).Scan(&out.CreatedAt)
	if err != nil {
		return out, err
	}
	refs := map[string]SourceRef{}
	for _, planner := range p.Version.Planners {
		refs[planner.SessionID] = SourceRef{SessionID: planner.SessionID, PermissionRevision: planner.PermissionRevision}
	}
	for _, ref := range p.Version.Sources {
		if prev, ok := refs[ref.SessionID]; ok && prev.PermissionRevision != ref.PermissionRevision {
			return out, ErrDenied
		}
		refs[ref.SessionID] = ref
	}
	for _, ref := range refs {
		if _, err = tx.Exec(ctx, `INSERT INTO planning_execution_sources(approval_id,camp_id,session_id,permission_revision,transcript_revision) VALUES($1,$2,$3,$4,$5)`, out.ID, campID, ref.SessionID, ref.PermissionRevision, ref.TranscriptRevision); err != nil {
			return out, err
		}
	}
	if err = s.checkPlanningApproval(ctx, tx, out); err != nil {
		return out, err
	}
	if err = emitPlanningApproval(ctx, tx, a, out); err != nil {
		return out, err
	}
	if err = s.finishCommand(ctx, tx, a, key, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) PlanningApproval(ctx context.Context, a auth.Actor, campID, planID, id string) (PlanningApproval, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PlanningApproval{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return PlanningApproval{}, err
	}
	out, err := readPlanningApproval(ctx, tx, campID, planID, id)
	if err != nil {
		return out, err
	}
	if a.UserID != out.CreatorUserID && a.UserID != out.OwnerUserID {
		return PlanningApproval{}, ErrDenied
	}
	if err = s.checkPlanningApproval(ctx, tx, out); err != nil {
		return PlanningApproval{}, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ApprovePlanningExecution(ctx context.Context, a auth.Actor, campID, planID, id, key string, in PlanningApprovalAction) (PlanningApproval, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PlanningApproval{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return PlanningApproval{}, err
	}
	out, err := readPlanningApproval(ctx, tx, campID, planID, id)
	if err != nil {
		return out, err
	}
	if a.UserID != out.OwnerUserID || out.Execution.Location == "local" && a.DeviceID != out.OwnerDeviceID {
		return PlanningApproval{}, ErrDenied
	}
	if err = s.checkPlanningApproval(ctx, tx, out); err != nil {
		return PlanningApproval{}, err
	}
	var saved PlanningApproval
	if hit, e := s.beginCommand(ctx, tx, a, key, "planning.execution.approve:"+id, in, &saved); e != nil {
		return out, e
	} else if hit {
		return out, nil
	}
	if out.Revision != in.ExpectedRevision {
		return PlanningApproval{}, ErrRevision
	}
	if out.State != "pending" {
		return PlanningApproval{}, ErrTransition
	}
	if out.Execution.Location == "local" && !digest64(in.ExecutionContext) || out.Execution.Location == "cloud" && in.ExecutionContext != "" {
		return PlanningApproval{}, ErrInput
	}
	out.State = "approved"
	out.Revision++
	out.ExecutionContext = in.ExecutionContext
	out.ApprovedByDeviceID = a.DeviceID
	err = tx.QueryRow(ctx, `UPDATE planning_execution_approvals SET state='approved',revision=revision+1,execution_context=NULLIF($2,''),approved_by_device_id=$3,approved_at=now() WHERE id=$1 RETURNING approved_at`, id, in.ExecutionContext, a.DeviceID).Scan(&out.ApprovedAt)
	if err != nil {
		return out, err
	}
	if err = emitPlanningApproval(ctx, tx, a, out); err != nil {
		return out, err
	}
	if err = s.finishCommand(ctx, tx, a, key, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// Cancellation returns no saved prompt/source content and remains possible
// after a source is revoked. Both principals can withdraw the unused approval.
func (s *Store) CancelPlanningApproval(ctx context.Context, a auth.Actor, campID, planID, id, key string, revision int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return err
	}
	out, err := readPlanningApproval(ctx, tx, campID, planID, id)
	if err != nil {
		return err
	}
	if a.UserID != out.CreatorUserID && a.UserID != out.OwnerUserID {
		return ErrDenied
	}
	var saved struct{}
	if hit, e := s.beginCommand(ctx, tx, a, key, "planning.execution.cancel:"+id, revision, &saved); e != nil {
		return e
	} else if hit {
		return nil
	}
	if out.Revision != revision {
		return ErrRevision
	}
	if oneOf(out.State, "cancelled", "dispatched") {
		return ErrTransition
	}
	if _, err = tx.Exec(ctx, `UPDATE planning_execution_approvals SET state='cancelled',revision=revision+1 WHERE id=$1`, id); err != nil {
		return err
	}
	out.State = "cancelled"
	out.Revision++
	if err = emitPlanningApproval(ctx, tx, a, out); err != nil {
		return err
	}
	if err = s.finishCommand(ctx, tx, a, key, saved); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Status remains available to either principal after source revocation so they
// can withdraw a stale approval without receiving its saved prompt or sources.
type PlanningApprovalStatus struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	Revision       int64  `json:"revision"`
	ReviewRequired bool   `json:"review_required"`
}

func (s *Store) PlanningApprovalStatuses(ctx context.Context, a auth.Actor, campID, planID string) ([]PlanningApprovalStatus, error) {
	if !validID(planID) {
		return nil, ErrDenied
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id,state,revision FROM planning_execution_approvals WHERE camp_id=$1 AND plan_id=$2 AND (creator_user_id=$3 OR owner_user_id=$3) ORDER BY created_at DESC,id DESC LIMIT 100`, campID, planID, a.UserID)
	if err != nil {
		return nil, err
	}
	out := []PlanningApprovalStatus{}
	for rows.Next() {
		var status PlanningApprovalStatus
		if err = rows.Scan(&status.ID, &status.State, &status.Revision); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, status)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		// Only unused consent can require another review. Historical records
		// must not trigger hundreds of source/runner queries when opening a plan.
		if out[i].State != "pending" && out[i].State != "approved" {
			continue
		}
		v, err := readPlanningApproval(ctx, tx, campID, planID, out[i].ID)
		if err != nil {
			return nil, err
		}
		check := s.checkPlanningApproval(ctx, tx, v)
		if check != nil && !errors.Is(check, ErrDenied) && !errors.Is(check, ErrRevision) && !errors.Is(check, ErrTransition) && !errors.Is(check, ErrTransferHeld) {
			return nil, check
		}
		out[i].ReviewRequired = check != nil
	}
	return out, tx.Commit(ctx)
}
