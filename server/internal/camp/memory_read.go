package camp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/auth"
	memdb "github.com/teslashibe/capybara-camp/server/internal/memory"
)

type MemoryItem struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	Summary         string    `json:"summary"`
	Confidence      float64   `json:"confidence"`
	ReinforcedCount int       `json:"reinforced_count"`
	LastSeenAt      time.Time `json:"last_seen_at"`
	ObservationIDs  []string  `json:"observation_ids"`
	Digest          string    `json:"digest"`
}
type MemoryPage struct {
	Memories []MemoryItem `json:"memories"`
	Next     string       `json:"next"`
}
type MemoryJobPage struct {
	Jobs []MemoryJob `json:"jobs"`
	Next string      `json:"next"`
}

func memoryReadError(err error) error {
	if errors.Is(err, memdb.ErrSnapshotLimit) {
		return ErrInput
	}
	return err
}

func memoryItem(v memdb.Snapshot) MemoryItem {
	return MemoryItem{ID: v.Memory.ID, Kind: string(v.Memory.Kind), Summary: v.Memory.Summary, Confidence: v.Memory.Confidence, ReinforcedCount: v.Memory.ReinforcedCount, LastSeenAt: v.Memory.LastSeenAt, ObservationIDs: v.Memory.Sources, Digest: v.Digest}
}

func (s *Store) Memories(ctx context.Context, a auth.Actor, campID, projectID, visibility, after string, limit int) (MemoryPage, error) {
	page := MemoryPage{Memories: []MemoryItem{}}
	if s.memory == nil {
		return page, ErrTransition
	}
	if limit < 1 || limit > 100 || len(after) > 256 || strings.ContainsRune(after, 0) {
		return page, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return page, err
	}
	space, err := s.memorySpace(ctx, tx, a, campID, projectID, visibility, false)
	if err != nil || space == "" {
		return page, err
	}
	values, err := s.memory.Memories(ctx, tx, space, nil, after, limit+1)
	if err != nil {
		return page, memoryReadError(err)
	}
	all := map[string]bool{}
	for _, v := range values {
		for _, id := range v.Memory.Sources {
			all[id] = true
		}
	}
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	allowed, err := s.memorySourceSet(ctx, tx, a, campID, space, ids)
	if err != nil {
		return page, err
	}
	for index, v := range values {
		if index == limit {
			page.Next = values[index-1].Memory.ID
			break
		}
		visible := len(v.Memory.Sources) > 0
		for _, id := range v.Memory.Sources {
			if !allowed[id] {
				visible = false
				break
			}
		}
		if visible {
			page.Memories = append(page.Memories, memoryItem(v))
		}
	}
	return page, nil
}

func (s *Store) Memory(ctx context.Context, a auth.Actor, campID, projectID, visibility, id string) (MemoryItem, error) {
	if s.memory == nil {
		return MemoryItem{}, ErrTransition
	}
	if len(id) == 0 || len(id) > 256 || strings.ContainsRune(id, 0) {
		return MemoryItem{}, ErrDenied
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MemoryItem{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return MemoryItem{}, err
	}
	space, err := s.memorySpace(ctx, tx, a, campID, projectID, visibility, false)
	if err != nil {
		return MemoryItem{}, err
	}
	if space == "" {
		return MemoryItem{}, ErrDenied
	}
	values, err := s.memory.Memories(ctx, tx, space, []string{id}, "", 1)
	if err != nil {
		return MemoryItem{}, memoryReadError(err)
	}
	if len(values) != 1 || len(values[0].Memory.Sources) == 0 {
		return MemoryItem{}, ErrDenied
	}
	allowed, err := s.memorySourceSet(ctx, tx, a, campID, space, values[0].Memory.Sources)
	if err != nil {
		return MemoryItem{}, err
	}
	for _, id := range values[0].Memory.Sources {
		if !allowed[id] {
			return MemoryItem{}, ErrDenied
		}
	}
	return memoryItem(values[0]), nil
}

func (s *Store) MemoryJobs(ctx context.Context, a auth.Actor, campID, projectID, visibility, after string, limit int) (MemoryJobPage, error) {
	page := MemoryJobPage{Jobs: []MemoryJob{}}
	if limit < 1 || limit > 100 || after != "" && !validID(after) {
		return page, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return page, err
	}
	space, err := s.memorySpace(ctx, tx, a, campID, projectID, visibility, false)
	if err != nil || space == "" {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT `+memoryJobColumns+` FROM memory_jobs j JOIN memory_spaces p ON p.id=j.space_id WHERE j.space_id=$2 AND ($3='' OR j.id>NULLIF($3,'')::uuid) AND `+memoryJobSourceAccessSQL+` ORDER BY j.id LIMIT $4`, a.UserID, space, after, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		j, err := scanMemoryJob(rows)
		if err != nil {
			return page, err
		}
		if len(page.Jobs) == limit {
			page.Next = page.Jobs[limit-1].ID
			break
		}
		if j.CreatorUserID != a.UserID {
			j.Target = MemoryTarget{Location: j.Target.Location}
		}
		page.Jobs = append(page.Jobs, j)
	}
	return page, rows.Err()
}
