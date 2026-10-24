package camp

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// Discovery contains no prompts, provider session identities or frozen context.
// Opening a run and each subsequent request page rechecks source access.
type PlanningImplementationSummary struct {
	ID                string    `json:"id"`
	ApprovalID        string    `json:"approval_id"`
	PlanVersion       int64     `json:"plan_version"`
	CreatedAt         time.Time `json:"created_at"`
	RequestID         string    `json:"request_id"`
	SessionID         string    `json:"session_id"`
	OwnerUserID       string    `json:"owner_user_id"`
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	Effort            string    `json:"effort"`
	Speed             string    `json:"speed"`
	State             string    `json:"state"`
	ExecutionLocation string    `json:"execution_location"`
}

type PlanningImplementationPage struct {
	Implementations []PlanningImplementationSummary `json:"implementations"`
	Before          string                          `json:"before,omitempty"`
}

func (s *Store) Implementations(ctx context.Context, a auth.Actor, campID, planID string, version int64, before string, limit int) (PlanningImplementationPage, error) {
	return retryRead(ctx, func() (PlanningImplementationPage, error) {
		return s.implementationsOnce(ctx, a, campID, planID, version, before, limit)
	})
}

func (s *Store) implementationsOnce(ctx context.Context, a auth.Actor, campID, planID string, version int64, before string, limit int) (PlanningImplementationPage, error) {
	out := PlanningImplementationPage{Implementations: []PlanningImplementationSummary{}}
	if version < 1 || limit < 1 || limit > 20 || (before != "" && !validID(before)) {
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
	p, err := s.loadPlanning(ctx, tx, a, campID, planID, version)
	if err != nil {
		return out, err
	}
	if err = s.checkPlanningSources(ctx, tx, a, p); err != nil {
		return out, err
	}
	// Pick the newest request first, then enforce the same content predicate as
	// detail/stream reads. An inaccessible follow-up must not appear as an older
	// successful run. Cursor lookup is constrained to this Camp, plan and version.
	rows, err := tx.Query(ctx, `SELECT i.id,i.approval_id,i.plan_version,i.created_at,r.id,r.session_id,r.owner_user_id,profile.provider,COALESCE(NULLIF(settings.payload->>'model',''),r.model),COALESCE(NULLIF(settings.payload->>'effort',''),r.effort),COALESCE(NULLIF(settings.payload->>'speed',''),r.speed),r.state,r.execution_location
 FROM planning_implementations i
 JOIN LATERAL (SELECT * FROM prompt_requests work WHERE work.implementation_id=i.id ORDER BY work.created_at DESC,work.id DESC LIMIT 1) r ON true
 JOIN sessions s ON s.id=r.session_id
 JOIN profiles profile ON profile.id=s.profile_id
 LEFT JOIN LATERAL (SELECT payload FROM request_stream_events WHERE request_id=r.id AND payload->>'type'='settings' ORDER BY seq DESC LIMIT 1) settings ON true
 WHERE i.camp_id=$2 AND i.plan_id=$3 AND i.plan_version=$4 AND `+readableRequestContentSQL+`
 AND ($5::uuid IS NULL OR (i.created_at,i.id)<(SELECT cursor.created_at,cursor.id FROM planning_implementations cursor WHERE cursor.id=$5 AND cursor.camp_id=$2 AND cursor.plan_id=$3 AND cursor.plan_version=$4))
 ORDER BY i.created_at DESC,i.id DESC LIMIT $6`, a.UserID, campID, planID, version, nullableID(before), limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v PlanningImplementationSummary
		if err = rows.Scan(&v.ID, &v.ApprovalID, &v.PlanVersion, &v.CreatedAt, &v.RequestID, &v.SessionID, &v.OwnerUserID, &v.Provider, &v.Model, &v.Effort, &v.Speed, &v.State, &v.ExecutionLocation); err != nil {
			return out, err
		}
		out.Implementations = append(out.Implementations, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Implementations) > limit {
		out.Implementations = out.Implementations[:limit]
		out.Before = out.Implementations[limit-1].ID
	}
	return out, tx.Commit(ctx)
}

// Batch the board's latest implementation projection. Choose newest work before
// checking access, so a revoked new run never falls back to an older success.
// Within that run, current or uncertain execution takes priority over queued work.
// version=0 selects each ticket's active version; detail can request an older one.
func (s *Store) latestPlanningImplementations(ctx context.Context, tx pgx.Tx, a auth.Actor, campID string, ids []string, version int64) (map[string]*PlanningImplementationSummary, error) {
	rows, err := tx.Query(ctx, `SELECT p.id,i.id,i.approval_id,i.plan_version,i.created_at,r.id,r.session_id,r.owner_user_id,profile.provider,COALESCE(NULLIF(settings.payload->>'model',''),r.model),COALESCE(NULLIF(settings.payload->>'effort',''),r.effort),COALESCE(NULLIF(settings.payload->>'speed',''),r.speed),r.state,r.execution_location
 FROM planning_tickets p
 JOIN LATERAL (SELECT * FROM planning_implementations work WHERE work.camp_id=p.camp_id AND work.plan_id=p.id AND work.plan_version=CASE WHEN $4::bigint>0 THEN $4 ELSE p.active_version END ORDER BY work.created_at DESC,work.id DESC LIMIT 1) i ON true
 JOIN LATERAL (SELECT * FROM prompt_requests work WHERE work.implementation_id=i.id ORDER BY (work.state IN ('claimed','started','cancel_requested','outcome_unknown')) DESC,work.created_at DESC,work.id DESC LIMIT 1) r ON true
 JOIN sessions s ON s.id=r.session_id
 JOIN profiles profile ON profile.id=s.profile_id
 LEFT JOIN LATERAL (SELECT payload FROM request_stream_events WHERE request_id=r.id AND payload->>'type'='settings' ORDER BY seq DESC LIMIT 1) settings ON true
 WHERE p.camp_id=$2 AND p.id=ANY($3::uuid[]) AND `+readableRequestContentSQL, a.UserID, campID, ids, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*PlanningImplementationSummary{}
	for rows.Next() {
		var id string
		var value PlanningImplementationSummary
		if err := rows.Scan(&id, &value.ID, &value.ApprovalID, &value.PlanVersion, &value.CreatedAt, &value.RequestID, &value.SessionID, &value.OwnerUserID, &value.Provider, &value.Model, &value.Effort, &value.Speed, &value.State, &value.ExecutionLocation); err != nil {
			return nil, err
		}
		out[id] = &value
	}
	return out, rows.Err()
}
