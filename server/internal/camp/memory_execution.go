package camp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
	memorydb "github.com/teslashibe/capybara-camp/server/internal/memory"
	"github.com/teslashibe/cortex"
)

// MemoryInference is an internal transport boundary, not a client-supplied
// callback. A runtime adapter must route only to this frozen target and provider.
// It receives no source session to resume and must not retry provider work.
// It must honor cancellation and join its provider process before returning.
type MemoryInference func(context.Context, MemoryTarget, string, localcli.InferenceRequest) (localcli.InferenceResult, error)

const memoryExecutionTimeout = 3 * time.Minute

// RunMemoryJob coordinates one approved job. A saved result can be committed
// after an expired claim without calling dispatch again. An uncertain provider
// attempt cannot be reclaimed. Runtime claim/payload endpoints and scheduler
// admission remain separate from this coordinator.
func (s *Store) RunMemoryJob(ctx context.Context, actor auth.Actor, campID, id string, dispatch MemoryInference) (MemoryJob, error) {
	if s.memory == nil || !validID(id) {
		return MemoryJob{}, ErrInput
	}
	ctx, stop := context.WithTimeout(ctx, memoryExecutionTimeout)
	defer stop()
	claim := uuid.NewString()
	job, replay, err := s.claimMemoryExecution(ctx, actor, campID, id, claim, dispatch != nil)
	if err != nil || job.State == "completed" {
		return job, err
	}
	selection := cortex.ConsolidationSelection{ObservationIDs: job.ObservationIDs}
	for _, ref := range job.Memories {
		selection.MemoryIDs = append(selection.MemoryIDs, ref.ID)
	}
	model := memorydb.CLIModel{Name: job.Model, Effort: job.Effort, Speed: job.Speed}
	model.Dispatch = func(ctx context.Context, input localcli.InferenceRequest, hash string) (localcli.InferenceResult, error) {
		if replay != nil {
			if err := s.replayMemoryInference(ctx, actor, job, claim, hash); err != nil {
				return localcli.InferenceResult{}, err
			}
			return *replay, nil
		}
		if err := s.startMemoryInference(ctx, actor, job, claim, hash, input); err != nil {
			return localcli.InferenceResult{}, err
		}
		callCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			tick := time.NewTicker(500 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-callCtx.Done():
					return
				case <-tick.C:
				}
				check, end := context.WithTimeout(callCtx, 2*time.Second)
				err := s.checkMemoryExecution(check, actor, job, claim)
				end()
				if err != nil {
					cancel()
					return
				}
			}
		}()
		result, callErr := dispatch(callCtx, job.Target, job.Provider, input)
		cancelled := callCtx.Err()
		cancel()
		<-done
		if callErr != nil {
			if cancelled != nil {
				return localcli.InferenceResult{}, cancelled
			}
			// Cortex retains errors in its outcome record. Runtime stderr and
			// transport errors must not become stored credentials or source text.
			return localcli.InferenceResult{}, errors.New("memory inference outcome is unconfirmed")
		}
		// Persist a received response even if the caller disconnected. The guarded
		// write still rejects cancellation, revocation and an expired claim.
		saveCtx, end := context.WithTimeout(context.Background(), 5*time.Second)
		defer end()
		if err := s.saveMemoryInference(saveCtx, actor, job, claim, result); err != nil {
			return localcli.InferenceResult{}, err
		}
		return result, nil
	}
	_, runErr := s.memory.Engine(model).ConsolidateSelected(ctx, job.SpaceID, selection,
		func(ctx context.Context, tx pgx.Tx, review cortex.ConsolidationReview) error {
			current, err := s.lockMemoryExecution(ctx, tx, actor, campID, id)
			if err != nil {
				return err
			}
			if err = memoryClaim(ctx, tx, id, claim); err != nil {
				return err
			}
			if review.UserID != job.SpaceID || !sameMemoryIDs(review.ObservationIDs, job.ObservationIDs) || !sameMemoryIDs(review.MemoryIDs, selection.MemoryIDs) {
				return ErrDenied
			}
			if err = s.checkMemorySnapshots(ctx, tx, current); err != nil {
				return err
			}
			switch review.Phase {
			case "inference":
				if current.State != "preparing" {
					return ErrTransition
				}
				tag, err := tx.Exec(ctx, `UPDATE memory_jobs SET cortex_run_id=$2,prompt_hash=$3 WHERE id=$1 AND (prompt_hash IS NULL OR prompt_hash=$3)`, id, review.RunID, review.PromptHash)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return ErrRevision
				}
			case "commit":
				if current.State != "result_ready" {
					return ErrTransition
				}
				tag, err := tx.Exec(ctx, `UPDATE memory_jobs SET state='completed',revision=revision+1,updated_at=now(),claim_until=NULL,failure_code='' WHERE id=$1 AND cortex_run_id=$2 AND prompt_hash=$3`, id, review.RunID, review.PromptHash)
				if err != nil {
					return err
				}
				if tag.RowsAffected() != 1 {
					return ErrRevision
				}
				current.State, current.Revision = "completed", current.Revision+1
				if err = emitMemoryJob(ctx, tx, actor, current); err != nil {
					return err
				}
			default:
				return ErrInput
			}
			return nil
		})
	// Read the durable state even after a cancelled context or lost commit reply.
	recovery, end := context.WithTimeout(context.Background(), 5*time.Second)
	defer end()
	final, finishErr := s.finishMemoryExecution(recovery, actor, campID, id, claim, runErr)
	if final.State == "completed" {
		return final, finishErr
	}
	return final, errors.Join(runErr, finishErr)
}

func sameMemoryIDs(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func (s *Store) lockMemoryExecution(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, id string) (MemoryJob, error) {
	if _, err := s.lock(ctx, tx, a, campID, true); err != nil {
		return MemoryJob{}, err
	}
	job, err := s.memoryJob(ctx, tx, a, campID, id)
	if err != nil {
		return MemoryJob{}, err
	}
	if job.CreatorUserID != a.UserID {
		return MemoryJob{}, ErrDenied
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT NOT archived FROM projects WHERE camp_id=$1 AND id=$2`, campID, job.ProjectID).Scan(&active); err != nil {
		return MemoryJob{}, accessError(err)
	}
	if !active {
		return MemoryJob{}, ErrDenied
	}
	if job.Target.Location == "local" {
		if job.Target.DeviceID != a.DeviceID {
			return MemoryJob{}, ErrDenied
		}
		var device string
		err = tx.QueryRow(ctx, `SELECT id FROM devices WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now() FOR SHARE`, a.DeviceID, a.UserID).Scan(&device)
		if err != nil {
			return MemoryJob{}, accessError(err)
		}
	} else {
		account, err := s.lockAccount(ctx, tx, a.UserID, job.Target.AccountID)
		if err != nil {
			return MemoryJob{}, err
		}
		if account.State != "connected" || account.InstallationID != job.Target.InstallationID || account.Epoch != job.Target.Epoch || account.VerifiedSubject == "" || account.CredentialExpiresAt != nil && !account.CredentialExpiresAt.After(time.Now()) {
			return MemoryJob{}, ErrTransition
		}
	}
	return job, nil
}

func (s *Store) checkMemorySnapshots(ctx context.Context, tx pgx.Tx, job MemoryJob) error {
	if len(job.Memories) == 0 {
		return nil
	}
	ids := make([]string, 0, len(job.Memories))
	digests := map[string]string{}
	for _, ref := range job.Memories {
		ids = append(ids, ref.ID)
		digests[ref.ID] = ref.Digest
	}
	snapshots, err := s.memory.Memories(ctx, tx, job.SpaceID, ids, "", 500)
	if err != nil {
		return err
	}
	if len(snapshots) != len(ids) {
		return ErrRevision
	}
	for _, snapshot := range snapshots {
		if digests[snapshot.Memory.ID] != snapshot.Digest {
			return ErrRevision
		}
	}
	return nil
}

func memoryClaim(ctx context.Context, tx pgx.Tx, id, claim string) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT COALESCE(claim_id=$2 AND claim_until>now(),false) FROM memory_jobs WHERE id=$1 FOR UPDATE`, id, claim).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrTransition
	}
	return nil
}

func (s *Store) claimMemoryExecution(ctx context.Context, a auth.Actor, campID, id, claim string, canDispatch bool) (MemoryJob, *localcli.InferenceResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MemoryJob{}, nil, err
	}
	defer tx.Rollback(ctx)
	job, err := s.lockMemoryExecution(ctx, tx, a, campID, id)
	if err != nil {
		return MemoryJob{}, nil, err
	}
	if job.State == "completed" {
		return job, nil, nil
	}
	var expired, started bool
	var saved []byte
	var resultHash string
	err = tx.QueryRow(ctx, `SELECT claim_until IS NULL OR claim_until<=now(),started_at IS NOT NULL,inference_result,COALESCE(result_hash,'') FROM memory_jobs WHERE id=$1 FOR UPDATE`, id).Scan(&expired, &started, &saved, &resultHash)
	if err != nil {
		return MemoryJob{}, nil, err
	}
	if expired && job.State == "running" {
		_, err = tx.Exec(ctx, `UPDATE memory_jobs SET state='outcome_unknown',revision=revision+1,updated_at=now(),failure_code='inference_unconfirmed',claim_until=NULL WHERE id=$1`, id)
		if err != nil {
			return MemoryJob{}, nil, err
		}
		job.State, job.FailureCode, job.Revision = "outcome_unknown", "inference_unconfirmed", job.Revision+1
		if err = emitMemoryJob(ctx, tx, a, job); err != nil {
			return MemoryJob{}, nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return MemoryJob{}, nil, err
		}
		return job, nil, ErrTransition
	}
	if !expired || !(job.State == "queued" || job.State == "preparing" && (!started || len(saved) > 0) || job.State == "result_ready") {
		return MemoryJob{}, nil, ErrTransition
	}
	var result *localcli.InferenceResult
	if len(saved) > 0 {
		result = &localcli.InferenceResult{}
		if err = json.Unmarshal(saved, result); err != nil {
			return MemoryJob{}, nil, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return MemoryJob{}, nil, err
		}
		digest := sha256.Sum256(encoded)
		if resultHash != hex.EncodeToString(digest[:]) {
			return MemoryJob{}, nil, ErrRevision
		}
	}
	if result == nil && (!canDispatch || started) {
		return MemoryJob{}, nil, ErrTransition
	}
	if err = s.checkMemorySnapshots(ctx, tx, job); err != nil {
		return MemoryJob{}, nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE memory_jobs SET state='preparing',revision=revision+1,updated_at=now(),claim_id=$2,claim_until=now()+interval '4 minutes',failure_code='' WHERE id=$1`, id, claim)
	if err != nil {
		return MemoryJob{}, nil, err
	}
	job.State, job.Revision = "preparing", job.Revision+1
	if err = emitMemoryJob(ctx, tx, a, job); err != nil {
		return MemoryJob{}, nil, err
	}
	return job, result, tx.Commit(ctx)
}

func (s *Store) replayMemoryInference(ctx context.Context, a auth.Actor, job MemoryJob, claim, hash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := s.lockMemoryExecution(ctx, tx, a, job.CampID, job.ID)
	if err != nil {
		return err
	}
	if err = memoryClaim(ctx, tx, job.ID, claim); err != nil {
		return err
	}
	if current.State != "preparing" {
		return ErrTransition
	}
	tag, err := tx.Exec(ctx, `UPDATE memory_jobs SET state='result_ready',revision=revision+1,updated_at=now() WHERE id=$1 AND prompt_hash=$2 AND inference_result IS NOT NULL`, job.ID, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrRevision
	}
	return tx.Commit(ctx)
}

func (s *Store) startMemoryInference(ctx context.Context, a auth.Actor, job MemoryJob, claim, hash string, input localcli.InferenceRequest) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := s.lockMemoryExecution(ctx, tx, a, job.CampID, job.ID)
	if err != nil {
		return err
	}
	if err = memoryClaim(ctx, tx, job.ID, claim); err != nil {
		return err
	}
	if current.State != "preparing" {
		return ErrTransition
	}
	tag, err := tx.Exec(ctx, `UPDATE memory_jobs SET state='running',revision=revision+1,updated_at=now(),started_at=now(),inference_system=$3,inference_prompt=$4 WHERE id=$1 AND prompt_hash=$2 AND started_at IS NULL`, job.ID, hash, input.System, input.Prompt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrTransition
	}
	current.State, current.Revision = "running", current.Revision+1
	if err = emitMemoryJob(ctx, tx, a, current); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) checkMemoryExecution(ctx context.Context, a auth.Actor, job MemoryJob, claim string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := s.lockMemoryExecution(ctx, tx, a, job.CampID, job.ID)
	if err != nil {
		return err
	}
	if current.State != "running" {
		return ErrTransition
	}
	return memoryClaim(ctx, tx, job.ID, claim)
}

func (s *Store) saveMemoryInference(ctx context.Context, a auth.Actor, job MemoryJob, claim string, result localcli.InferenceResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(data) > 8<<20 || len(result.Text) > 4<<20 {
		return ErrInput
	}
	digest := sha256.Sum256(data)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := s.lockMemoryExecution(ctx, tx, a, job.CampID, job.ID)
	if err != nil {
		return err
	}
	if err = memoryClaim(ctx, tx, job.ID, claim); err != nil {
		return err
	}
	if current.State != "running" {
		return ErrTransition
	}
	_, err = tx.Exec(ctx, `UPDATE memory_jobs SET state='result_ready',revision=revision+1,updated_at=now(),inference_result=$2,result_hash=$3 WHERE id=$1`, job.ID, data, hex.EncodeToString(digest[:]))
	if err != nil {
		return err
	}
	current.State, current.Revision = "result_ready", current.Revision+1
	if err = emitMemoryJob(ctx, tx, a, current); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) finishMemoryExecution(ctx context.Context, a auth.Actor, campID, id, claim string, runErr error) (MemoryJob, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MemoryJob{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return MemoryJob{}, err
	}
	job, err := scanMemoryJob(tx.QueryRow(ctx, `SELECT `+memoryJobColumns+` FROM memory_jobs j JOIN memory_spaces p ON p.id=j.space_id WHERE j.id=$1 AND j.camp_id=$2 AND j.creator_user_id=$3`, id, campID, a.UserID))
	if err != nil {
		return MemoryJob{}, accessError(err)
	}
	if !oneOf(job.State, "completed", "cancelled", "failed") {
		state, code := "failed", "consolidation_failed"
		if job.State == "running" {
			state, code = "outcome_unknown", "inference_unconfirmed"
		}
		if job.State == "result_ready" && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
			state, code = "result_ready", "commit_pending"
		}
		tag, err := tx.Exec(ctx, `UPDATE memory_jobs SET state=$3,failure_code=$4,revision=revision+1,updated_at=now(),claim_until=NULL WHERE id=$1 AND claim_id=$2`, id, claim, state, code)
		if err != nil {
			return MemoryJob{}, err
		}
		if tag.RowsAffected() != 1 {
			return MemoryJob{}, ErrTransition
		}
		job.State, job.FailureCode, job.Revision = state, code, job.Revision+1
		if err = emitMemoryJob(ctx, tx, a, job); err != nil {
			return MemoryJob{}, err
		}
	}
	// A revoked source may no longer be returned even to the job's creator.
	job.ObservationIDs, job.Memories = nil, nil
	return job, tx.Commit(ctx)
}
