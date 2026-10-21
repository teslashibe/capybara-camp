package camp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

var ErrPlanningBudget = errors.New("this provider cannot enforce the selected token ceiling")

type PlanningUsage struct {
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
}

func validPlanningUsage(u *PlanningUsage) bool {
	return u == nil || ((u.InputTokens == nil || *u.InputTokens >= 0) && (u.OutputTokens == nil || *u.OutputTokens >= 0))
}

type PlannerInput struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	Speed     string `json:"speed"`
}
type PlanningInput struct {
	Title        string         `json:"title"`
	ProjectID    string         `json:"project_id"`
	Brief        string         `json:"brief"`
	Shared       bool           `json:"shared"`
	Planners     []PlannerInput `json:"planners"`
	Sources      []SourceRef    `json:"sources"`
	BudgetPolicy string         `json:"budget_policy"`
	MergeSlot    int            `json:"merge_slot"`
}
type Planner struct {
	PlannerInput
	Slot               int    `json:"slot"`
	Provider           string `json:"provider"`
	ProfileID          string `json:"profile_id"`
	OwnerUserID        string `json:"owner_user_id"`
	OwnerDeviceID      string `json:"owner_device_id"`
	PermissionRevision int64  `json:"permission_revision"`
}
type PlanningStep struct {
	Ordinal   int            `json:"ordinal"`
	Slot      int            `json:"slot"`
	Phase     string         `json:"phase"`
	RequestID string         `json:"request_id"`
	State     string         `json:"state"`
	Output    string         `json:"output"`
	Usage     *PlanningUsage `json:"usage,omitempty"`
}
type PlanningVersion struct {
	Version      int64          `json:"version"`
	State        string         `json:"state"`
	Brief        string         `json:"brief"`
	Sources      []SourceRef    `json:"sources"`
	BudgetPolicy string         `json:"budget_policy"`
	CallLimit    int            `json:"call_limit"`
	InputTarget  int64          `json:"input_target"`
	OutputTarget int64          `json:"output_target"`
	MergeSlot    int            `json:"merge_slot"`
	Planners     []Planner      `json:"planners"`
	Steps        []PlanningStep `json:"steps"`
}
type PlanningTicket struct {
	ID            string          `json:"id"`
	CampID        string          `json:"camp_id"`
	ProjectID     string          `json:"project_id"`
	CreatorUserID string          `json:"creator_user_id"`
	Title         string          `json:"title"`
	Shared        bool            `json:"shared"`
	Revision      int64           `json:"revision"`
	ActiveVersion int64           `json:"active_version"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Version       PlanningVersion `json:"version"`
}
type PlanningAction struct {
	Action           string         `json:"action"`
	ExpectedRevision int64          `json:"expected_revision"`
	Input            *PlanningInput `json:"input,omitempty"`
}
type planningStepLink struct {
	PlanID  string
	Version int64
	Ordinal int
}

// Both clients and workers use immutable planner assignments. A changed login,
// session owner or permission revision requires a newly approved plan version.
func (s *Store) preparePlanning(ctx context.Context, tx pgx.Tx, a auth.Actor, campID string, input PlanningInput) (PlanningVersion, error) {
	input.Brief = strings.TrimSpace(input.Brief)
	if len(input.Brief) == 0 || len(input.Brief) > 8192 || len(input.Planners) != 2 || len(input.Sources) > 20 || input.MergeSlot < 0 || input.MergeSlot > 1 {
		return PlanningVersion{}, ErrInput
	}
	if input.BudgetPolicy == "" {
		input.BudgetPolicy = "strict_tokens"
	}
	if !oneOf(input.BudgetPolicy, "calls", "strict_tokens") {
		return PlanningVersion{}, ErrInput
	}
	v := PlanningVersion{State: "draft", Brief: input.Brief, Sources: []SourceRef{}, BudgetPolicy: input.BudgetPolicy, CallLimit: 5, InputTarget: 40000, OutputTarget: 8000, MergeSlot: input.MergeSlot, Planners: []Planner{}, Steps: []PlanningStep{}}
	for slot, p := range input.Planners {
		if p.Speed == "" {
			p.Speed = "standard"
		}
		if len(p.Model) > 200 || !oneOf(p.Effort, "", "low", "medium", "high", "xhigh", "max", "ultra") || !oneOf(p.Speed, "standard", "fast", "ultrafast") {
			return v, ErrInput
		}
		session, err := s.readSession(ctx, tx, a, campID, p.SessionID)
		if err != nil {
			return v, err
		}
		if session.OwnerUserID != a.UserID && !session.AllowRequests {
			return v, ErrDenied
		}
		if session.Provider == "Claude" && p.Speed == "ultrafast" {
			return v, ErrInput
		}
		if p.Model == "" {
			p.Model = session.Model
		}
		v.Planners = append(v.Planners, Planner{PlannerInput: p, Slot: slot, Provider: session.Provider, ProfileID: session.ProfileID, OwnerUserID: session.OwnerUserID, OwnerDeviceID: session.OwnerDeviceID, PermissionRevision: session.PermissionRevision})
	}
	if v.Planners[0].Provider == v.Planners[1].Provider {
		return v, ErrInput
	}
	seen := map[string]bool{}
	for _, ref := range input.Sources {
		if seen[ref.SessionID] || ref.TranscriptRevision < 1 || len(ref.MessageIDs) == 0 {
			return v, ErrInput
		}
		seen[ref.SessionID] = true
		source, err := s.readSession(ctx, tx, a, campID, ref.SessionID)
		if err != nil {
			return v, err
		}
		if input.Shared && !source.Shared {
			return v, ErrDenied
		}
		if ref.PermissionRevision != 0 && ref.PermissionRevision != source.PermissionRevision {
			return v, ErrDenied
		}
		ref.PermissionRevision = source.PermissionRevision
		if ref.TranscriptRevision > source.TranscriptRevision {
			return v, ErrRevision
		}
		if _, err = readContextMessages(ctx, tx, ref); err != nil {
			return v, err
		}
		v.Sources = append(v.Sources, ref)
	}
	return v, nil
}

func insertPlanningVersion(ctx context.Context, tx pgx.Tx, id string, v PlanningVersion) error {
	source, err := json.Marshal(v.Sources)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO planning_versions(plan_id,version,state,brief,sources,budget_policy,merge_slot) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, v.Version, v.State, v.Brief, source, v.BudgetPolicy, v.MergeSlot)
	if err != nil {
		return err
	}
	for _, p := range v.Planners {
		_, err = tx.Exec(ctx, `INSERT INTO planning_participants(plan_id,version,slot,session_id,profile_id,owner_user_id,owner_device_id,permission_revision,provider,model,effort,speed) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, v.Version, p.Slot, p.SessionID, p.ProfileID, p.OwnerUserID, p.OwnerDeviceID, p.PermissionRevision, p.Provider, p.Model, p.Effort, p.Speed)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreatePlanning(ctx context.Context, a auth.Actor, campID, key string, input PlanningInput) (PlanningTicket, error) {
	input.Title = strings.TrimSpace(input.Title)
	if !validName(input.Title) || !validID(input.ProjectID) {
		return PlanningTicket{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PlanningTicket{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return PlanningTicket{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE camp_id=$1 AND id=$2)`, campID, input.ProjectID).Scan(&exists); err != nil {
		return PlanningTicket{}, err
	}
	if !exists {
		return PlanningTicket{}, ErrDenied
	}
	var saved PlanningTicket
	if hit, err := s.beginCommand(ctx, tx, a, key, "planning.create:"+campID, input, &saved); err != nil {
		return saved, err
	} else if hit {
		return s.loadPlanning(ctx, tx, a, campID, saved.ID, 0)
	}
	version, err := s.preparePlanning(ctx, tx, a, campID, input)
	if err != nil {
		return saved, err
	}
	version.Version = 1
	id := uuid.NewString()
	if _, err = tx.Exec(ctx, `INSERT INTO planning_tickets(id,camp_id,project_id,creator_user_id,title,shared) VALUES($1,$2,$3,$4,$5,$6)`, id, campID, input.ProjectID, a.UserID, input.Title, input.Shared); err != nil {
		return saved, err
	}
	if err = insertPlanningVersion(ctx, tx, id, version); err != nil {
		return saved, err
	}
	if err = emitPlanning(ctx, tx, a, campID, id, 1, "draft"); err != nil {
		return saved, err
	}
	saved, err = s.loadPlanning(ctx, tx, a, campID, id, 0)
	if err != nil {
		return saved, err
	}
	if err = s.finishCommand(ctx, tx, a, key, saved); err != nil {
		return saved, err
	}
	return saved, tx.Commit(ctx)
}

func (s *Store) loadPlanning(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, id string, version int64) (PlanningTicket, error) {
	var p PlanningTicket
	if !validID(id) || version < 0 {
		return p, ErrDenied
	}
	err := tx.QueryRow(ctx, `SELECT p.id,p.camp_id,p.project_id,p.creator_user_id,p.title,p.shared,p.revision,p.active_version,p.updated_at FROM planning_tickets p
      WHERE p.camp_id=$1 AND p.id=$2 AND (p.creator_user_id=$3 OR p.shared OR EXISTS(SELECT 1 FROM planning_participants x WHERE x.plan_id=p.id AND x.version=p.active_version AND x.owner_user_id=$3))`, campID, id, a.UserID).Scan(&p.ID, &p.CampID, &p.ProjectID, &p.CreatorUserID, &p.Title, &p.Shared, &p.Revision, &p.ActiveVersion, &p.UpdatedAt)
	if err != nil {
		return p, accessError(err)
	}
	if version == 0 {
		version = p.ActiveVersion
	}
	v := PlanningVersion{Version: version, Planners: []Planner{}, Steps: []PlanningStep{}}
	var sources []byte
	err = tx.QueryRow(ctx, `SELECT state,brief,sources,budget_policy,call_limit,input_target,output_target,merge_slot FROM planning_versions WHERE plan_id=$1 AND version=$2`, id, version).Scan(&v.State, &v.Brief, &sources, &v.BudgetPolicy, &v.CallLimit, &v.InputTarget, &v.OutputTarget, &v.MergeSlot)
	if err != nil {
		return p, accessError(err)
	}
	if err = json.Unmarshal(sources, &v.Sources); err != nil {
		return p, err
	}
	rows, err := tx.Query(ctx, `SELECT slot,session_id,profile_id,owner_user_id,owner_device_id,permission_revision,provider,model,effort,speed FROM planning_participants WHERE plan_id=$1 AND version=$2 ORDER BY slot`, id, version)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var x Planner
		if err = rows.Scan(&x.Slot, &x.SessionID, &x.ProfileID, &x.OwnerUserID, &x.OwnerDeviceID, &x.PermissionRevision, &x.Provider, &x.Model, &x.Effort, &x.Speed); err != nil {
			rows.Close()
			return p, err
		}
		v.Planners = append(v.Planners, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	rows, err = tx.Query(ctx, `SELECT x.ordinal,x.request_id,r.state,x.output,x.usage FROM planning_steps x JOIN prompt_requests r ON r.id=x.request_id WHERE x.plan_id=$1 AND x.version=$2 ORDER BY x.ordinal`, id, version)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var x PlanningStep
		var usage []byte
		if err = rows.Scan(&x.Ordinal, &x.RequestID, &x.State, &x.Output, &usage); err != nil {
			rows.Close()
			return p, err
		}
		x.Slot, x.Phase = planningStepRole(x.Ordinal, v.MergeSlot)
		if len(usage) > 0 {
			if err = json.Unmarshal(usage, &x.Usage); err != nil {
				rows.Close()
				return p, err
			}
		}
		v.Steps = append(v.Steps, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	if oneOf(v.State, "running", "paused") {
		for _, step := range v.Steps {
			if step.State == "outcome_unknown" {
				v.State = "needs_review"
				break
			}
		}
	}
	p.Version = v
	return p, nil
}
func (s *Store) Planning(ctx context.Context, a auth.Actor, campID, id string, version int64) (PlanningTicket, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return PlanningTicket{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return PlanningTicket{}, err
	}
	p, err := s.loadPlanning(ctx, tx, a, campID, id, version)
	if err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

type PlanningSummary struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	CreatorUserID string    `json:"creator_user_id"`
	Title         string    `json:"title"`
	Shared        bool      `json:"shared"`
	Revision      int64     `json:"revision"`
	ActiveVersion int64     `json:"active_version"`
	State         string    `json:"state"`
	Planners      []Planner `json:"planners"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s *Store) ListPlanning(ctx context.Context, a auth.Actor, campID string) ([]PlanningSummary, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT p.id,p.project_id,p.creator_user_id,p.title,p.shared,p.revision,p.active_version,p.updated_at,
      CASE WHEN v.state IN ('running','paused') AND EXISTS(SELECT 1 FROM prompt_requests r WHERE r.plan_id=p.id AND r.plan_version=p.active_version AND r.state='outcome_unknown') THEN 'needs_review' ELSE v.state END
      FROM planning_tickets p JOIN planning_versions v ON v.plan_id=p.id AND v.version=p.active_version
      WHERE p.camp_id=$1 AND (p.creator_user_id=$2 OR p.shared OR EXISTS(SELECT 1 FROM planning_participants x WHERE x.plan_id=p.id AND x.version=p.active_version AND x.owner_user_id=$2))
      ORDER BY p.updated_at DESC,p.id LIMIT 100`, campID, a.UserID)
	if err != nil {
		return nil, err
	}
	out := []PlanningSummary{}
	ids := []string{}
	indexes := map[string]int{}
	for rows.Next() {
		var p PlanningSummary
		p.Planners = []Planner{}
		if err = rows.Scan(&p.ID, &p.ProjectID, &p.CreatorUserID, &p.Title, &p.Shared, &p.Revision, &p.ActiveVersion, &p.UpdatedAt, &p.State); err != nil {
			rows.Close()
			return nil, err
		}
		indexes[p.ID] = len(out)
		ids = append(ids, p.ID)
		out = append(out, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		rows, err = tx.Query(ctx, `SELECT x.plan_id,x.slot,x.session_id,x.profile_id,x.owner_user_id,x.owner_device_id,x.permission_revision,x.provider,x.model,x.effort,x.speed FROM planning_participants x JOIN planning_tickets p ON p.id=x.plan_id AND p.active_version=x.version WHERE x.plan_id=ANY($1::uuid[]) ORDER BY x.plan_id,x.slot`, ids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var p Planner
			if err = rows.Scan(&id, &p.Slot, &p.SessionID, &p.ProfileID, &p.OwnerUserID, &p.OwnerDeviceID, &p.PermissionRevision, &p.Provider, &p.Model, &p.Effort, &p.Speed); err != nil {
				rows.Close()
				return nil, err
			}
			index := indexes[id]
			out[index].Planners = append(out[index].Planners, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, tx.Commit(ctx)
}

func planningStepRole(step, merge int) (int, string) {
	if step < 2 {
		return step, "draft"
	}
	if step < 4 {
		return step - 2, "review"
	}
	return merge, "merge"
}
func planningPrompt(v PlanningVersion, ordinal int) string {
	slot, phase := planningStepRole(ordinal, v.MergeSlot)
	intro := "Plan the following feature. Do not execute project changes. Keep the response concise, under 900 words.\n\nBrief:\n" + v.Brief
	output := map[int]string{}
	for _, step := range v.Steps {
		output[step.Ordinal] = step.Output
	}
	switch phase {
	case "draft":
		return intro + "\n\nDraft your plan independently. Include implementation steps, risks, tests, and open questions."
	case "review":
		return intro + fmt.Sprintf("\n\nYour independent draft:\n%s\n\nOther provider's draft:\n%s\n\nReview the other proposal once. Identify the strongest choices, conflicts, missing tests, and decisions requiring the user. Do not erase disagreements.", output[slot], output[1-slot])
	default:
		return intro + fmt.Sprintf("\n\nDraft A:\n%s\n\nDraft B:\n%s\n\nReview A:\n%s\n\nReview B:\n%s\n\nProduce one combined plan. Preserve unresolved disagreements and alternatives explicitly. Include scope, implementation steps, acceptance tests, and decisions requiring approval. Do not implement the plan.", output[0], output[1], output[2], output[3])
	}
}

func (s *Store) enqueuePlanningStep(ctx context.Context, tx pgx.Tx, p PlanningTicket, ordinal int) error {
	if ordinal < 0 || ordinal >= p.Version.CallLimit {
		return ErrInput
	}
	slot, _ := planningStepRole(ordinal, p.Version.MergeSlot)
	planner := p.Version.Planners[slot]
	actor := auth.Actor{UserID: p.CreatorUserID}
	session, err := s.readSession(ctx, tx, actor, p.CampID, planner.SessionID)
	if err != nil {
		return err
	}
	if session.OwnerUserID != planner.OwnerUserID || session.OwnerDeviceID != planner.OwnerDeviceID || session.ProfileID != planner.ProfileID || session.PermissionRevision != planner.PermissionRevision {
		return ErrDenied
	}
	prompt := planningPrompt(p.Version, ordinal)
	if len(prompt) > 65536 {
		return ErrInput
	}
	request, err := s.enqueueRequest(ctx, tx, actor, p.CampID, session, RequestInput{Prompt: prompt, Model: planner.Model, Effort: planner.Effort, Speed: planner.Speed, Sources: p.Version.Sources}, &planningStepLink{PlanID: p.ID, Version: p.Version.Version, Ordinal: ordinal})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO planning_steps(plan_id,version,ordinal,request_id) VALUES($1,$2,$3,$4)`, p.ID, p.Version.Version, ordinal, request.ID)
	return err
}
func (s *Store) advancePlanning(ctx context.Context, tx pgx.Tx, p PlanningTicket) error {
	if p.Version.State != "running" || p.Version.Version != p.ActiveVersion {
		return nil
	}
	steps := map[int]PlanningStep{}
	for _, step := range p.Version.Steps {
		steps[step.Ordinal] = step
	}
	next := []int{}
	if len(steps) == 0 {
		next = []int{0, 1}
	} else if steps[0].State == "completed" && steps[1].State == "completed" {
		if _, exists := steps[2]; !exists {
			next = append(next, 2)
		}
		if _, exists := steps[3]; !exists {
			next = append(next, 3)
		}
		if steps[2].State == "completed" && steps[3].State == "completed" {
			if _, exists := steps[4]; !exists {
				next = append(next, 4)
			}
		}
	}
	if steps[4].State == "completed" {
		_, err := tx.Exec(ctx, `UPDATE planning_versions SET state='ready' WHERE plan_id=$1 AND version=$2`, p.ID, p.Version.Version)
		return err
	}
	for _, ordinal := range next {
		if err := s.enqueuePlanningStep(ctx, tx, p, ordinal); err != nil {
			return err
		}
	}
	return nil
}
func emitPlanning(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, id string, version int64, state string) error {
	if err := emit(ctx, tx, a, campID, "planning.changed", map[string]any{"plan_id": id, "version": version, "state": state}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE events SET plan_id=$2 WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, campID, id)
	return err
}
func (s *Store) cancelPlanningRequests(ctx context.Context, tx pgx.Tx, a auth.Actor, p PlanningTicket) error {
	rows, err := tx.Query(ctx, `SELECT id FROM prompt_requests WHERE plan_id=$1 AND plan_version=$2 AND state IN ('queued','pending_approval','claimed','started','cancel_requested')`, p.ID, p.Version.Version)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		_, err = tx.Exec(ctx, `UPDATE prompt_requests SET revision=revision+1,state=CASE WHEN state IN ('started','cancel_requested') THEN 'cancel_requested' ELSE 'cancelled' END,finished_at=CASE WHEN state IN ('started','cancel_requested') THEN NULL ELSE now() END,reason='plan_cancelled' WHERE id=$1`, id)
		if err != nil {
			return err
		}
		r, err := loadRequest(ctx, tx, p.CampID, id)
		if err != nil {
			return err
		}
		if err = emitRequest(ctx, tx, a, r, "request."+r.State, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ActOnPlanning(ctx context.Context, a auth.Actor, campID, id, key string, input PlanningAction) (PlanningTicket, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PlanningTicket{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return PlanningTicket{}, err
	}
	p, err := s.loadPlanning(ctx, tx, a, campID, id, 0)
	if err != nil {
		return p, err
	}
	if p.CreatorUserID != a.UserID {
		return p, ErrDenied
	}
	var saved PlanningTicket
	if hit, err := s.beginCommand(ctx, tx, a, key, "planning.action:"+id, input, &saved); err != nil {
		return p, err
	} else if hit {
		return s.loadPlanning(ctx, tx, a, campID, id, 0)
	}
	if p.Revision != input.ExpectedRevision {
		return p, ErrRevision
	}
	switch input.Action {
	case "start":
		if p.Version.State != "draft" {
			return p, ErrTransition
		}
		if p.Version.BudgetPolicy != "calls" {
			return p, ErrPlanningBudget
		}
		p.Version.State = "running"
	case "pause":
		if p.Version.State != "running" {
			return p, ErrTransition
		}
		p.Version.State = "paused"
	case "resume":
		if p.Version.State != "paused" {
			return p, ErrTransition
		}
		p.Version.State = "running"
	case "cancel":
		p.Version.State = "cancelled"
		if err = s.cancelPlanningRequests(ctx, tx, a, p); err != nil {
			return p, err
		}
	case "restart":
		if input.Input == nil {
			return p, ErrInput
		}
		definition := *input.Input
		definition.Shared = p.Shared
		v, err := s.preparePlanning(ctx, tx, a, campID, definition)
		if err != nil {
			return p, err
		}
		if err = s.cancelPlanningRequests(ctx, tx, a, p); err != nil {
			return p, err
		}
		if _, err = tx.Exec(ctx, `UPDATE planning_versions SET state='cancelled' WHERE plan_id=$1 AND version=$2 AND state NOT IN ('ready','failed','cancelled')`, id, p.ActiveVersion); err != nil {
			return p, err
		}
		v.Version = p.ActiveVersion + 1
		if err = insertPlanningVersion(ctx, tx, id, v); err != nil {
			return p, err
		}
		p.Version = v
		p.ActiveVersion = v.Version
	default:
		return p, ErrInput
	}
	if _, err = tx.Exec(ctx, `UPDATE planning_versions SET state=$3 WHERE plan_id=$1 AND version=$2`, id, p.Version.Version, p.Version.State); err != nil {
		return p, err
	}
	if _, err = tx.Exec(ctx, `UPDATE planning_tickets SET revision=revision+1,active_version=$2,updated_at=now() WHERE id=$1`, id, p.ActiveVersion); err != nil {
		return p, err
	}
	if err = s.advancePlanning(ctx, tx, p); err != nil {
		return p, err
	}
	p, err = s.loadPlanning(ctx, tx, a, campID, id, 0)
	if err != nil {
		return p, err
	}
	if err = emitPlanning(ctx, tx, a, campID, id, p.ActiveVersion, p.Version.State); err != nil {
		return p, err
	}
	if err = s.finishCommand(ctx, tx, a, key, p); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

func (s *Store) planningCanStart(ctx context.Context, tx pgx.Tx, r Request) error {
	if r.Kind != "planning" {
		return nil
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM planning_versions v JOIN planning_tickets p ON p.id=v.plan_id WHERE v.plan_id=$1 AND v.version=$2 AND p.active_version=v.version AND v.state='running')`, r.PlanID, r.PlanVersion).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrTransition
	}
	return nil
}
func (s *Store) planningRequestFinished(ctx context.Context, tx pgx.Tx, a auth.Actor, r Request, input RequestAction) error {
	var usage []byte
	var err error
	if input.Usage != nil {
		usage, err = json.Marshal(input.Usage)
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE planning_steps SET output=$2,usage=$3 WHERE request_id=$1`, r.ID, input.Output, usage); err != nil {
		return err
	}
	creator := auth.Actor{}
	if err = tx.QueryRow(ctx, `SELECT creator_user_id FROM planning_tickets WHERE id=$1`, r.PlanID).Scan(&creator.UserID); err != nil {
		return err
	}
	p, err := s.loadPlanning(ctx, tx, creator, r.CampID, r.PlanID, r.PlanVersion)
	if err != nil {
		return err
	}
	if p.ActiveVersion == r.PlanVersion && !oneOf(p.Version.State, "cancelled", "failed", "ready") {
		if r.State != "completed" {
			p.Version.State = "failed"
			if _, err = tx.Exec(ctx, `UPDATE planning_versions SET state='failed' WHERE plan_id=$1 AND version=$2`, p.ID, p.Version.Version); err != nil {
				return err
			}
			if err = s.cancelPlanningRequests(ctx, tx, a, p); err != nil {
				return err
			}
		} else if err = s.advancePlanning(ctx, tx, p); err != nil {
			if !errors.Is(err, ErrDenied) && !errors.Is(err, ErrInput) && !errors.Is(err, ErrRevision) {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE planning_versions SET state='failed' WHERE plan_id=$1 AND version=$2`, p.ID, p.Version.Version); err != nil {
				return err
			}
			if err = s.cancelPlanningRequests(ctx, tx, a, p); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE planning_tickets SET revision=revision+1,updated_at=now() WHERE id=$1`, p.ID); err != nil {
		return err
	}
	p, err = s.loadPlanning(ctx, tx, creator, r.CampID, r.PlanID, r.PlanVersion)
	if err != nil {
		return err
	}
	return emitPlanning(ctx, tx, a, r.CampID, p.ID, r.PlanVersion, p.Version.State)
}
func (s *Store) ClaimPlanningRequest(ctx context.Context, a auth.Actor, campID, key string, sessions []string) (*Request, error) {
	if sessions == nil {
		sessions = []string{}
	}
	return s.claimRequest(ctx, a, campID, key, sessions, "planning")
}

func (s *Store) failPlanning(ctx context.Context, tx pgx.Tx, a auth.Actor, r Request) error {
	var creator string
	if err := tx.QueryRow(ctx, `SELECT creator_user_id FROM planning_tickets WHERE id=$1`, r.PlanID).Scan(&creator); err != nil {
		return err
	}
	p, err := s.loadPlanning(ctx, tx, auth.Actor{UserID: creator}, r.CampID, r.PlanID, r.PlanVersion)
	if err != nil {
		return err
	}
	if p.ActiveVersion != r.PlanVersion || oneOf(p.Version.State, "ready", "cancelled", "failed") {
		return nil
	}
	if _, err = tx.Exec(ctx, `UPDATE planning_versions SET state='failed' WHERE plan_id=$1 AND version=$2`, p.ID, p.Version.Version); err != nil {
		return err
	}
	if err = s.cancelPlanningRequests(ctx, tx, a, p); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE planning_tickets SET revision=revision+1,updated_at=now() WHERE id=$1`, p.ID); err != nil {
		return err
	}
	return emitPlanning(ctx, tx, a, p.CampID, p.ID, p.Version.Version, "failed")
}
