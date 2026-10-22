package camp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

const MaxPlanningImportBytes = 16 << 20

// Imported steps are observations supplied by the owner. They are never
// fabricated provider requests or permission to execute a historical prompt.
type ImportedPlanningStep struct {
	Ordinal int            `json:"ordinal"`
	State   string         `json:"state"`
	Output  string         `json:"output"`
	Error   string         `json:"error,omitempty"`
	Usage   *PlanningUsage `json:"usage,omitempty"`
}
type ImportedPlanningMessage struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Text   string `json:"text"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Speed  string `json:"speed,omitempty"`
}
type ImportedPlanningSource struct {
	ID       string                    `json:"id"`
	Title    string                    `json:"title"`
	Revision string                    `json:"revision"`
	Messages []ImportedPlanningMessage `json:"messages"`
}
type ImportedPlanningVersion struct {
	Version      int64                    `json:"version"`
	Mode         string                   `json:"mode"`
	State        string                   `json:"state"`
	Brief        string                   `json:"brief"`
	BudgetPolicy string                   `json:"budget_policy"`
	MergeSlot    int                      `json:"merge_slot"`
	Planners     []PlannerInput           `json:"planners"`
	Sources      []SourceRef              `json:"sources"`
	LocalSources []ImportedPlanningSource `json:"local_sources"`
	Steps        []ImportedPlanningStep   `json:"steps"`
}
type PlanningImportInput struct {
	SourceID        string                        `json:"source_id"`
	Title           string                        `json:"title"`
	ProjectID       string                        `json:"project_id"`
	Versions        []ImportedPlanningVersion     `json:"versions"`
	Implementations []ImportedImplementationInput `json:"implementations,omitempty"`
}
type PlanningImportSnapshot struct {
	State        string                   `json:"state"`
	LocalSources []ImportedPlanningSource `json:"local_sources"`
	Steps        []ImportedPlanningStep   `json:"steps"`
}

func validImportedPlanningVersion(v ImportedPlanningVersion) bool {
	calls, count := 5, 2
	if v.Mode == "single" {
		calls, count = 1, 1
	} else if v.Mode != "comparison" {
		return false
	}
	if !oneOf(v.State, "draft", "paused", "ready", "cancelled", "failed", "needs_review") || len(v.Brief) > 8192 || len(v.Planners) != count || len(v.Steps) != 0 && len(v.Steps) != calls || v.MergeSlot < 0 || v.MergeSlot >= count || len(v.Sources)+len(v.LocalSources) > 20 {
		return false
	}
	if v.State == "draft" && len(v.Steps) > 0 {
		return false
	}
	for i, step := range v.Steps {
		if step.Ordinal != i || !oneOf(step.State, "pending", "completed", "cancelled", "failed", "unknown") || len(step.Output) > 8192 || len(step.Error) > 8192 || !validPlanningUsage(step.Usage) {
			return false
		}
		if v.State == "ready" && (step.State != "completed" || strings.TrimSpace(step.Output) == "") {
			return false
		}
	}
	for _, planner := range v.Planners {
		if planner.Execution != nil || len(planner.Model) > 200 || len(planner.Effort) > 64 || len(planner.Speed) > 64 {
			return false
		}
	}
	if v.State == "ready" && len(v.Steps) != calls {
		return false
	}
	seen := map[string]bool{}
	total := 0
	for _, source := range v.LocalSources {
		if len(source.ID) != 64 || len(source.Revision) != 64 || seen[source.ID] || len(source.Title) > 500 || len(source.Messages) == 0 || len(source.Messages) > 120 {
			return false
		}
		if !digest64(source.ID) || !digest64(source.Revision) {
			return false
		}
		seen[source.ID] = true
		messages := map[string]bool{}
		for _, m := range source.Messages {
			if m.ID == "" || len(m.ID) > 256 || messages[m.ID] || !oneOf(m.Role, "user", "assistant", "system", "tool") || len(m.Text) > 48<<10 || len(m.Model) > 200 || len(m.Effort) > 64 || len(m.Speed) > 64 {
				return false
			}
			messages[m.ID] = true
			total += len(m.Text)
		}
	}
	return total <= 256<<10
}

func (s *Store) ImportPlanning(ctx context.Context, a auth.Actor, campID, key string, in PlanningImportInput) (PlanningTicket, error) {
	if !digest64(in.SourceID) || !validID(in.ProjectID) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 2000 || len(in.Versions) == 0 || len(in.Versions) > 100 || !validImportedImplementations(in.Implementations, len(in.Versions)) {
		return PlanningTicket{}, ErrInput
	}
	for i, v := range in.Versions {
		if v.Version != int64(i+1) || !validImportedPlanningVersion(v) {
			return PlanningTicket{}, ErrInput
		}
	}
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > MaxPlanningImportBytes {
		return PlanningTicket{}, ErrInput
	}
	digest := sha256.Sum256(raw)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PlanningTicket{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return PlanningTicket{}, err
	}
	var saved PlanningTicket
	if hit, e := s.beginCommand(ctx, tx, a, key, "planning.import:"+campID, in, &saved); e != nil {
		return saved, e
	} else if hit {
		saved, err = s.loadPlanning(ctx, tx, a, campID, saved.ID, 0)
		if err == nil {
			err = s.checkPlanningSources(ctx, tx, a, saved)
		}
		return saved, err
	}
	var prior string
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT plan_id,input_sha256 FROM planning_imports WHERE camp_id=$1 AND owner_user_id=$2 AND source_id=$3`, campID, a.UserID, in.SourceID).Scan(&prior, &previous)
	if err == nil {
		if !bytes.Equal(previous, digest[:]) {
			return saved, ErrConflict
		}
		saved, err = s.loadPlanning(ctx, tx, a, campID, prior, 0)
		if err != nil {
			return saved, err
		}
		if err = s.checkPlanningSources(ctx, tx, a, saved); err != nil {
			return saved, err
		}
		if err = s.finishCommand(ctx, tx, a, key, saved); err != nil {
			return saved, err
		}
		return saved, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return saved, err
	}
	var project bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE camp_id=$1 AND id=$2 AND NOT archived)`, campID, in.ProjectID).Scan(&project); err != nil {
		return saved, err
	}
	if !project {
		return saved, ErrDenied
	}
	prepared := make([]PlanningVersion, 0, len(in.Versions))
	for _, v := range in.Versions {
		brief := v.Brief
		if strings.TrimSpace(brief) == "" {
			if v.State != "draft" {
				return saved, ErrInput
			}
			brief = "Imported draft"
		}
		// Resolve ownership and source access through the normal planning path.
		// Historical settings are observations, not executable model choices.
		bindings := append([]PlannerInput(nil), v.Planners...)
		for i := range bindings {
			bindings[i].Model, bindings[i].Effort, bindings[i].Speed = "", "", ""
		}
		current, e := s.preparePlanning(ctx, tx, a, campID, PlanningInput{Mode: v.Mode, Title: in.Title, ProjectID: in.ProjectID, Brief: brief, Planners: bindings, Sources: v.Sources, BudgetPolicy: v.BudgetPolicy, MergeSlot: v.MergeSlot})
		if e != nil {
			return saved, e
		}
		for i := range current.Planners {
			if current.Planners[i].OwnerUserID != a.UserID {
				return saved, ErrDenied
			}
			current.Planners[i].Model = v.Planners[i].Model
			current.Planners[i].Effort = v.Planners[i].Effort
			current.Planners[i].Speed = v.Planners[i].Speed
		}
		current.Version = v.Version
		current.Brief = v.Brief
		current.State = v.State
		if !oneOf(v.State, "ready", "cancelled", "failed") {
			current.State = "failed"
		}
		prepared = append(prepared, current)
	}
	id := uuid.NewString()
	if _, err = tx.Exec(ctx, `INSERT INTO planning_tickets(id,camp_id,project_id,creator_user_id,title,shared,active_version) VALUES($1,$2,$3,$4,$5,false,$6)`, id, campID, in.ProjectID, a.UserID, in.Title, len(in.Versions)); err != nil {
		return saved, err
	}
	for i, v := range prepared {
		if err = insertPlanningVersion(ctx, tx, id, v); err != nil {
			return saved, err
		}
		if _, err = tx.Exec(ctx, `UPDATE planning_participants SET execution_location=NULL,execution_epoch=NULL,execution_workspace_id=NULL WHERE plan_id=$1 AND version=$2`, id, v.Version); err != nil {
			return saved, err
		}
		snapshot := PlanningImportSnapshot{State: in.Versions[i].State, LocalSources: in.Versions[i].LocalSources, Steps: in.Versions[i].Steps}
		if snapshot.LocalSources == nil {
			snapshot.LocalSources = []ImportedPlanningSource{}
		}
		if snapshot.Steps == nil {
			snapshot.Steps = []ImportedPlanningStep{}
		}
		data, e := json.Marshal(snapshot)
		if e != nil {
			return saved, e
		}
		if _, err = tx.Exec(ctx, `UPDATE planning_versions SET import_snapshot=$3 WHERE plan_id=$1 AND version=$2`, id, v.Version, data); err != nil {
			return saved, err
		}
	}
	if err = s.insertImportedImplementations(ctx, tx, a, campID, id, in.Implementations); err != nil {
		return saved, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO planning_imports(camp_id,owner_user_id,source_id,plan_id,input_sha256) VALUES($1,$2,$3,$4,$5)`, campID, a.UserID, in.SourceID, id, digest[:]); err != nil {
		return saved, err
	}
	saved, err = s.loadPlanning(ctx, tx, a, campID, id, 0)
	if err != nil {
		return saved, err
	}
	if err = emitPlanning(ctx, tx, a, campID, id, saved.ActiveVersion, saved.Version.State); err != nil {
		return saved, err
	}
	if err = s.finishCommand(ctx, tx, a, key, saved); err != nil {
		return saved, err
	}
	return saved, tx.Commit(ctx)
}
