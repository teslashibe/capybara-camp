package camp

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"net/url"
	"sort"
	"strings"
)

type ProjectUpdate struct {
	Name             string   `json:"name"`
	Archived         bool     `json:"archived"`
	Repositories     []string `json:"repositories"`
	ExpectedRevision int64    `json:"expected_revision"`
}
type ProjectAssignment struct {
	ProjectID        string `json:"project_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

// Repository links describe project membership; they do not grant Git access.
func repositoryURLs(values []string) ([]string, error) {
	if len(values) > 50 {
		return nil, ErrInput
	}
	result := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Host) != "github.com" || parsed.User != nil || strings.ContainsAny(value, "?#%") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
			return nil, ErrInput
		}
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) != 2 {
			return nil, ErrInput
		}
		for _, part := range parts {
			if part == "" || part == "." || part == ".." || strings.ContainsAny(part, " \\\t\r\n%") {
				return nil, ErrInput
			}
			for _, c := range part {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
					return nil, ErrInput
				}
			}
		}
		repo := strings.TrimSuffix(parts[1], ".git")
		if repo == "" {
			return nil, ErrInput
		}
		canonical := "https://github.com/" + strings.ToLower(parts[0]+"/"+repo)
		if len(canonical) > 500 {
			return nil, ErrInput
		}
		if !seen[canonical] {
			result = append(result, canonical)
			seen[canonical] = true
		}
	}
	sort.Strings(result)
	return result, nil
}
func readProject(ctx context.Context, tx pgx.Tx, campID, id string) (Project, error) {
	if !validID(id) {
		return Project{}, ErrInput
	}
	var p Project
	err := tx.QueryRow(ctx, `SELECT p.id,p.name,p.revision,p.archived,p.inbox,ARRAY(SELECT url FROM project_repository_links r WHERE r.project_id=p.id ORDER BY url) FROM projects p WHERE camp_id=$1 AND p.id=$2`, campID, id).Scan(&p.ID, &p.Name, &p.Revision, &p.Archived, &p.Inbox, &p.Repositories)
	if err == pgx.ErrNoRows {
		return p, ErrDenied
	}
	return p, err
}
func assignmentProject(ctx context.Context, tx pgx.Tx, campID, id string) (string, error) {
	if id == "" {
		err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE camp_id=$1 AND inbox`, campID).Scan(&id)
		if err != nil {
			return "", err
		}
	}
	p, err := readProject(ctx, tx, campID, id)
	if err != nil {
		return "", err
	}
	if p.Archived {
		return "", ErrInput
	}
	return id, nil
}
func (s *Store) UpdateProject(ctx context.Context, a auth.Actor, campID, id, key string, input ProjectUpdate) (Project, error) {
	input.Name = strings.TrimSpace(input.Name)
	if !validName(input.Name) || input.ExpectedRevision < 1 {
		return Project{}, ErrInput
	}
	urls, err := repositoryURLs(input.Repositories)
	if err != nil {
		return Project{}, err
	}
	input.Repositories = urls
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return Project{}, err
	}
	current, err := readProject(ctx, tx, campID, id)
	if err != nil {
		return current, err
	}
	var result Project
	if hit, err := s.beginCommand(ctx, tx, a, key, "project.update:"+id, input, &result); err != nil {
		return result, err
	} else if hit {
		return current, nil
	}
	if current.Inbox {
		return current, ErrInput
	}
	if current.Revision != input.ExpectedRevision {
		return current, ErrRevision
	}
	if _, err = tx.Exec(ctx, `UPDATE projects SET name=$2,archived=$3,revision=revision+1 WHERE id=$1`, id, input.Name, input.Archived); err != nil {
		return current, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM project_repository_links WHERE project_id=$1`, id); err != nil {
		return current, err
	}
	for _, url := range urls {
		if _, err = tx.Exec(ctx, `INSERT INTO project_repository_links(camp_id,project_id,url) VALUES($1,$2,$3)`, campID, id, url); err != nil {
			return current, err
		}
	}
	result, err = readProject(ctx, tx, campID, id)
	if err != nil {
		return result, err
	}
	if err = emit(ctx, tx, a, campID, "project.updated", result); err != nil {
		return result, err
	}
	if err = s.finishCommand(ctx, tx, a, key, result); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
func (s *Store) AssignSessionProject(ctx context.Context, a auth.Actor, campID, id, key string, input ProjectAssignment) (Session, error) {
	if input.ExpectedRevision < 1 {
		return Session{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return Session{}, err
	}
	current, err := s.readSession(ctx, tx, a, campID, id)
	if err != nil {
		return current, err
	}
	var result Session
	if hit, err := s.beginCommand(ctx, tx, a, key, "session.project:"+id, input, &result); err != nil {
		return result, err
	} else if hit {
		return current, nil
	}
	if current.Revision != input.ExpectedRevision {
		return current, ErrRevision
	}
	project, err := assignmentProject(ctx, tx, campID, input.ProjectID)
	if err != nil {
		return current, err
	}
	if _, err = tx.Exec(ctx, `UPDATE sessions SET project_id=$2,revision=revision+1,updated_at=now() WHERE id=$1`, id, project); err != nil {
		return current, err
	}
	result, err = s.readSession(ctx, tx, a, campID, id)
	if err != nil {
		return current, err
	}
	if err = emitSession(ctx, tx, a, campID, "session.updated", result, false); err != nil {
		return result, err
	}
	if err = s.finishCommand(ctx, tx, a, key, result); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
func (s *Store) AssignPlanningProject(ctx context.Context, a auth.Actor, campID, id, key string, input ProjectAssignment) (PlanningTicket, error) {
	if input.ExpectedRevision < 1 {
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
	current, err := s.loadPlanning(ctx, tx, a, campID, id, 0)
	if err != nil {
		return current, err
	}
	if err = s.checkPlanningSources(ctx, tx, a, current); err != nil {
		return current, err
	}
	var result PlanningTicket
	if hit, err := s.beginCommand(ctx, tx, a, key, "planning.project:"+id, input, &result); err != nil {
		return result, err
	} else if hit {
		return current, nil
	}
	if current.Revision != input.ExpectedRevision {
		return current, ErrRevision
	}
	project, err := assignmentProject(ctx, tx, campID, input.ProjectID)
	if err != nil {
		return current, err
	}
	if _, err = tx.Exec(ctx, `UPDATE planning_tickets SET project_id=$2,revision=revision+1,updated_at=now() WHERE id=$1`, id, project); err != nil {
		return current, err
	}
	result, err = s.loadPlanning(ctx, tx, a, campID, id, 0)
	if err != nil {
		return current, err
	}
	if err = emitPlanning(ctx, tx, a, campID, id, result.ActiveVersion, result.Version.State); err != nil {
		return result, err
	}
	if err = s.finishCommand(ctx, tx, a, key, result); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
