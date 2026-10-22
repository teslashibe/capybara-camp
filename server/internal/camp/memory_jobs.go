package camp

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

type MemoryRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}
type MemoryTarget struct {
	Location       string `json:"location"`
	ProfileID      string `json:"profile_id,omitempty"`
	AccountID      string `json:"account_id,omitempty"`
	InstallationID string `json:"installation_id,omitempty"`
	DeviceID       string `json:"device_id,omitempty"`
	Epoch          int64  `json:"epoch,omitempty"`
}
type MemoryJobInput struct {
	ProjectID      string       `json:"project_id"`
	Visibility     string       `json:"visibility"`
	Target         MemoryTarget `json:"target"`
	Model          string       `json:"model"`
	Effort         string       `json:"effort"`
	Speed          string       `json:"speed"`
	ObservationIDs []string     `json:"observation_ids"`
	Memories       []MemoryRef  `json:"memories"`
}
type MemoryJob struct {
	ID             string       `json:"id"`
	CampID         string       `json:"camp_id"`
	SpaceID        string       `json:"space_id"`
	ProjectID      string       `json:"project_id"`
	Visibility     string       `json:"visibility"`
	CreatorUserID  string       `json:"creator_user_id"`
	State          string       `json:"state"`
	Revision       int64        `json:"revision"`
	Provider       string       `json:"provider"`
	Target         MemoryTarget `json:"target"`
	Model          string       `json:"model"`
	Effort         string       `json:"effort"`
	Speed          string       `json:"speed"`
	ObservationIDs []string     `json:"observation_ids"`
	Memories       []MemoryRef  `json:"memories"`
	FailureCode    string       `json:"failure_code,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

const memoryJobColumns = `j.id,j.camp_id,j.space_id,p.project_id,CASE WHEN p.owner_user_id IS NULL THEN 'project' ELSE 'private' END,j.creator_user_id,j.state,j.revision,j.provider,j.location,COALESCE(j.profile_id::text,''),COALESCE(j.account_id::text,''),COALESCE(j.installation_id::text,''),COALESCE(j.owner_device_id::text,''),COALESCE(j.installation_epoch,0),j.model,j.effort,j.speed,j.observation_ids,j.memory_refs,j.failure_code,j.created_at,j.updated_at`

// All saved source grants, including sources inherited from existing memories,
// must remain readable. Used by job reads and event replay in the same snapshot.
const memoryJobSourceAccessSQL = `NOT EXISTS(SELECT 1 FROM memory_job_sources ms JOIN memory_observations o ON o.id=ms.observation_id JOIN memory_spaces sp ON sp.id=o.space_id JOIN sessions src ON src.id=o.source_session_id WHERE ms.job_id=j.id AND NOT COALESCE(o.camp_id=j.camp_id AND o.space_id=j.space_id AND (sp.owner_user_id IS NULL OR sp.owner_user_id=$1) AND (src.shared OR src.owner_user_id=$1) AND (sp.owner_user_id IS NOT NULL OR src.shared) AND (src.permission_revision=o.permission_revision OR sp.owner_user_id=$1 AND src.owner_user_id=$1) AND src.transcript_revision>=o.transcript_revision AND NOT EXISTS(SELECT 1 FROM session_children ch WHERE ch.session_id=src.id),false))`

func scanMemoryJob(row rowScanner) (MemoryJob, error) {
	var j MemoryJob
	var refs []byte
	err := row.Scan(&j.ID, &j.CampID, &j.SpaceID, &j.ProjectID, &j.Visibility, &j.CreatorUserID, &j.State, &j.Revision, &j.Provider, &j.Target.Location, &j.Target.ProfileID, &j.Target.AccountID, &j.Target.InstallationID, &j.Target.DeviceID, &j.Target.Epoch, &j.Model, &j.Effort, &j.Speed, &j.ObservationIDs, &refs, &j.FailureCode, &j.CreatedAt, &j.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(refs, &j.Memories)
	}
	return j, err
}
func (s *Store) memoryJob(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, id string) (MemoryJob, error) {
	if !validID(id) {
		return MemoryJob{}, ErrDenied
	}
	j, err := scanMemoryJob(tx.QueryRow(ctx, `SELECT `+memoryJobColumns+` FROM memory_jobs j JOIN memory_spaces p ON p.id=j.space_id WHERE j.camp_id=$2 AND j.id=$3 AND (p.owner_user_id IS NULL OR p.owner_user_id=$1) AND `+memoryJobSourceAccessSQL, a.UserID, campID, id))
	if err != nil {
		return j, accessError(err)
	}
	if j.CreatorUserID != a.UserID {
		j.Target = MemoryTarget{Location: j.Target.Location}
	}
	return j, nil
}
func (s *Store) MemoryJob(ctx context.Context, a auth.Actor, campID, id string) (MemoryJob, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MemoryJob{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return MemoryJob{}, err
	}
	return s.memoryJob(ctx, tx, a, campID, id)
}

func (s *Store) memoryTarget(ctx context.Context, tx pgx.Tx, a auth.Actor, in MemoryTarget) (MemoryTarget, string, error) {
	if in.DeviceID != "" || in.InstallationID != "" || in.Epoch != 0 {
		return MemoryTarget{}, "", ErrInput
	}
	switch in.Location {
	case "local":
		if !validID(in.ProfileID) || in.AccountID != "" {
			return MemoryTarget{}, "", ErrInput
		}
		var provider string
		if err := tx.QueryRow(ctx, `SELECT p.provider,p.owner_device_id FROM profiles p WHERE p.id=$1 AND p.owner_user_id=$2`, in.ProfileID, a.UserID).Scan(&provider, &in.DeviceID); err != nil {
			return in, "", accessError(err)
		}
		return in, provider, nil
	case "cloud":
		if !validID(in.AccountID) || in.ProfileID != "" {
			return MemoryTarget{}, "", ErrInput
		}
		if err := s.cloudEnabled(); err != nil {
			return in, "", err
		}
		account, err := s.lockAccount(ctx, tx, a.UserID, in.AccountID)
		if err != nil {
			return in, "", err
		}
		if account.State != "connected" || account.VerifiedSubject == "" {
			return in, "", ErrTransition
		}
		in.InstallationID, in.Epoch = account.InstallationID, account.Epoch
		return in, account.Provider, nil
	default:
		return in, "", ErrInput
	}
}

// memorySourceSet checks the complete union in one query instead of retaining
// a partially authorized memory or making one database read per source.
func (s *Store) memorySourceSet(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, space string, ids []string) (map[string]bool, error) {
	if len(ids) > 10000 {
		return nil, ErrInput
	}
	for _, id := range ids {
		if !validID(id) {
			return nil, ErrDenied
		}
	}
	rows, err := tx.Query(ctx, `SELECT o.id FROM memory_observations o JOIN memory_spaces p ON p.id=o.space_id JOIN sessions src ON src.id=o.source_session_id WHERE o.camp_id=$1 AND o.space_id=$2 AND o.id=ANY($3::uuid[]) AND (p.owner_user_id IS NULL OR p.owner_user_id=$4) AND (src.shared OR src.owner_user_id=$4) AND (p.owner_user_id IS NOT NULL OR src.shared) AND (src.permission_revision=o.permission_revision OR p.owner_user_id=$4 AND src.owner_user_id=$4) AND src.transcript_revision>=o.transcript_revision AND NOT EXISTS(SELECT 1 FROM session_children ch WHERE ch.session_id=src.id)`, campID, space, ids, a.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allowed := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		allowed[id] = true
	}
	return allowed, rows.Err()
}

func (s *Store) CreateMemoryJob(ctx context.Context, a auth.Actor, campID, key string, in MemoryJobInput) (MemoryJob, error) {
	if s.memory == nil {
		return MemoryJob{}, ErrTransition
	}
	if len(in.ObservationIDs) < 1 || len(in.ObservationIDs) > 200 || len(in.Memories) > 500 || len(in.Model) < 1 || len(in.Model) > 160 || strings.HasPrefix(in.Model, "-") || strings.ContainsFunc(in.Model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || !oneOf(in.Effort, "", "low", "medium", "high", "xhigh", "max", "ultra") {
		return MemoryJob{}, ErrInput
	}
	if in.Speed == "" {
		in.Speed = "standard"
	}
	if !oneOf(in.Speed, "standard", "fast", "ultrafast") {
		return MemoryJob{}, ErrInput
	}
	seen := map[string]bool{}
	for _, id := range in.ObservationIDs {
		if !validID(id) || seen[id] {
			return MemoryJob{}, ErrInput
		}
		seen[id] = true
	}
	memoryIDs := []string{}
	memorySeen := map[string]bool{}
	for _, ref := range in.Memories {
		if ref.ID == "" || len(ref.ID) > 256 || strings.ContainsRune(ref.ID, 0) || len(ref.Digest) != 64 || strings.Trim(ref.Digest, "0123456789abcdef") != "" || memorySeen[ref.ID] {
			return MemoryJob{}, ErrInput
		}
		memorySeen[ref.ID] = true
		memoryIDs = append(memoryIDs, ref.ID)
	}
	if in.Memories == nil {
		in.Memories = []MemoryRef{}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MemoryJob{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return MemoryJob{}, err
	}
	var saved MemoryJob
	if hit, e := s.beginCommand(ctx, tx, a, key, "memory.job:"+campID, in, &saved); e != nil {
		return saved, e
	} else if hit {
		return s.memoryJob(ctx, tx, a, campID, saved.ID)
	}
	space, err := s.memorySpace(ctx, tx, a, campID, in.ProjectID, in.Visibility, true)
	if err != nil {
		return saved, err
	}
	target, provider, err := s.memoryTarget(ctx, tx, a, in.Target)
	if err != nil {
		return saved, err
	}
	if provider == "Claude" && in.Speed == "ultrafast" {
		return saved, ErrInput
	}
	if len(memoryIDs) > 0 {
		values, err := s.memory.Memories(ctx, tx, space, memoryIDs, "", 500)
		if err != nil {
			return saved, memoryReadError(err)
		}
		if len(values) != len(memoryIDs) {
			return saved, ErrDenied
		}
		byID := map[string]string{}
		for _, v := range values {
			byID[v.Memory.ID] = v.Digest
			if len(v.Memory.Sources) == 0 {
				return saved, ErrDenied
			}
			for _, id := range v.Memory.Sources {
				seen[id] = true
			}
		}
		for _, ref := range in.Memories {
			if byID[ref.ID] != ref.Digest {
				return saved, ErrRevision
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	allowed, err := s.memorySourceSet(ctx, tx, a, campID, space, ids)
	if err != nil {
		return saved, err
	}
	if len(allowed) != len(seen) {
		return saved, ErrDenied
	}
	pending, err := s.memory.Pending(ctx, tx, space, in.ObservationIDs)
	if err != nil {
		return saved, err
	}
	if !pending {
		return saved, ErrRevision
	}
	var busy bool
	var count int
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memory_jobs WHERE space_id=$1 AND state IN ('queued','preparing','awaiting','running','result_ready','outcome_unknown')), (SELECT count(*) FROM memory_jobs WHERE creator_user_id=$2 AND state IN ('queued','preparing','awaiting','running','result_ready','outcome_unknown'))`, space, a.UserID).Scan(&busy, &count); err != nil {
		return saved, err
	}
	if busy || count >= 32 {
		return saved, ErrTransition
	}
	refs, err := json.Marshal(in.Memories)
	if err != nil {
		return saved, err
	}
	id := uuid.NewString()
	_, err = tx.Exec(ctx, `INSERT INTO memory_jobs(id,camp_id,space_id,creator_user_id,provider,model,effort,speed,location,profile_id,owner_device_id,account_id,installation_id,installation_epoch,observation_ids,memory_refs) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::uuid,NULLIF($11,'')::uuid,NULLIF($12,'')::uuid,NULLIF($13,'')::uuid,NULLIF($14,0),$15,$16)`, id, campID, space, a.UserID, provider, in.Model, in.Effort, in.Speed, target.Location, target.ProfileID, target.DeviceID, target.AccountID, target.InstallationID, target.Epoch, in.ObservationIDs, refs)
	if err != nil {
		return saved, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO memory_job_sources(job_id,observation_id) SELECT $1,unnest($2::uuid[])`, id, ids); err != nil {
		return saved, err
	}
	saved, err = s.memoryJob(ctx, tx, a, campID, id)
	if err != nil {
		return saved, err
	}
	if err = emitMemoryJob(ctx, tx, a, saved); err != nil {
		return saved, err
	}
	if err = s.finishCommand(ctx, tx, a, key, saved); err != nil {
		return saved, err
	}
	return saved, tx.Commit(ctx)
}

func emitMemoryJob(ctx context.Context, tx pgx.Tx, a auth.Actor, j MemoryJob) error {
	if err := emit(ctx, tx, a, j.CampID, "memory.job", map[string]any{"job_id": j.ID, "project_id": j.ProjectID, "state": j.State, "revision": j.Revision}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE events SET memory_job_id=$2,recipient_user_id=CASE WHEN $3='private' THEN $4::uuid ELSE NULL END WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, j.CampID, j.ID, j.Visibility, j.CreatorUserID)
	return err
}

func (s *Store) CancelMemoryJob(ctx context.Context, a auth.Actor, campID, id, key string, revision int64) (MemoryJob, error) {
	if !validID(id) || revision < 1 {
		return MemoryJob{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MemoryJob{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return MemoryJob{}, err
	}
	var result MemoryJob
	loadOwned := func() (MemoryJob, error) {
		j, err := scanMemoryJob(tx.QueryRow(ctx, `SELECT `+memoryJobColumns+` FROM memory_jobs j JOIN memory_spaces p ON p.id=j.space_id WHERE j.camp_id=$1 AND j.id=$2 AND j.creator_user_id=$3`, campID, id, a.UserID))
		if err == nil {
			j.ObservationIDs = nil
			j.Memories = nil
		}
		return j, accessError(err)
	}
	if hit, e := s.beginCommand(ctx, tx, a, key, "memory.cancel:"+id, revision, &result); e != nil {
		return result, e
	} else if hit {
		return loadOwned()
	}
	j, err := loadOwned()
	if err != nil {
		return j, err
	}
	if j.CreatorUserID != a.UserID {
		return j, ErrDenied
	}
	if j.Revision != revision {
		return j, ErrRevision
	}
	if oneOf(j.State, "completed", "failed", "cancelled") {
		return j, ErrTransition
	}
	if _, err = tx.Exec(ctx, `UPDATE memory_jobs SET state='cancelled',revision=revision+1,updated_at=now() WHERE id=$1`, id); err != nil {
		return j, err
	}
	j, err = loadOwned()
	if err != nil {
		return j, err
	}
	if err = emitMemoryJob(ctx, tx, a, j); err != nil {
		return j, err
	}
	if err = s.finishCommand(ctx, tx, a, key, j); err != nil {
		return j, err
	}
	return j, tx.Commit(ctx)
}
