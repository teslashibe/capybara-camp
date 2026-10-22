package camp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
	"github.com/teslashibe/cortex"
	"github.com/teslashibe/cortex/llm"
)

const memoryTestOutput = `{"ops":[{"kind":"insert","memory":{"kind":"fact","summary":"Use the shared navigation","confidence":0.8},"sources":["s1"]}]}`

func memoryExecutionFixture(t *testing.T) (*Store, auth.Actor, string, MemoryJob) {
	t.Helper()
	s, actors, c, project, source, input := memoryFixture(t)
	observation, err := s.SaveMemoryObservation(t.Context(), actors[0], c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateMemoryJob(t.Context(), actors[0], c.ID, uuid.NewString(), MemoryJobInput{ProjectID: project.ID, Visibility: "private", Target: MemoryTarget{Location: "local", ProfileID: source.ProfileID}, Model: "fixture-model", Effort: "high", Speed: "standard", ObservationIDs: []string{observation.ID}})
	if err != nil {
		t.Fatal(err)
	}
	return s, actors[0], c.ID, job
}

func TestMemoryExecutionCommitsOnceAndPersistsRequest(t *testing.T) {
	s, a, c, idle := memoryExecutionFixture(t)
	calls := 0
	dispatch := func(ctx context.Context, target MemoryTarget, provider string, input localcli.InferenceRequest) (localcli.InferenceResult, error) {
		calls++
		if target != idle.Target || provider != "Codex" || input.Session.SessionID != "" || input.Model != "fixture-model" || input.Effort != "high" || input.Speed != "standard" || !strings.Contains(input.Prompt, "useful project decision") {
			t.Fatal("dispatch changed frozen selection")
		}
		var state, system, prompt, hash string
		if err := s.pool.QueryRow(ctx, `SELECT state,inference_system,inference_prompt,prompt_hash FROM memory_jobs WHERE id=$1`, idle.ID).Scan(&state, &system, &prompt, &hash); err != nil {
			t.Fatal(err)
		}
		want, err := cortex.ConsolidationPromptHash(input.Model, []llm.Message{{Role: "system", Content: input.System}, {Role: "user", Content: input.Prompt}})
		if err != nil || state != "running" || system != input.System || prompt != input.Prompt || hash != want {
			t.Fatal("provider ran before durable request")
		}
		return localcli.InferenceResult{Text: memoryTestOutput, Model: "resolved-fixture"}, nil
	}
	job, err := s.RunMemoryJob(t.Context(), a, c, idle.ID, dispatch)
	if err != nil || job.State != "completed" || calls != 1 {
		t.Fatal(job.State, calls, err)
	}
	if _, err = s.RunMemoryJob(t.Context(), a, c, idle.ID, dispatch); err != nil || calls != 1 {
		t.Fatal("completed job called provider", calls, err)
	}
	page, err := s.Memories(t.Context(), a, c, idle.ProjectID, "private", "", 50)
	if err != nil || len(page.Memories) != 1 {
		t.Fatal(page, err)
	}
	if len(page.Memories[0].ObservationIDs) != 1 || page.Memories[0].ObservationIDs[0] != idle.ObservationIDs[0] {
		t.Fatal("source provenance lost")
	}
	var run string
	if err = s.pool.QueryRow(t.Context(), `SELECT cortex_run_id FROM memory_jobs WHERE id=$1`, idle.ID).Scan(&run); err != nil {
		t.Fatal(err)
	}
	outcome, err := s.memory.Outcome(t.Context(), idle.SpaceID, run)
	if err != nil || outcome.Status != "completed" || outcome.ResolvedModel != "resolved-fixture" {
		t.Fatal(outcome, err)
	}
}

func TestMemoryExecutionConcurrentClaimAndUncertainFailure(t *testing.T) {
	s, a, c, job := memoryExecutionFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var calls atomic.Int32
	dispatch := func(context.Context, MemoryTarget, string, localcli.InferenceRequest) (localcli.InferenceResult, error) {
		calls.Add(1)
		close(entered)
		<-release
		return localcli.InferenceResult{}, errors.New("fixture lost response")
	}
	go func() { _, err := s.RunMemoryJob(t.Context(), a, c, job.ID, dispatch); done <- err }()
	<-entered
	if _, err := s.RunMemoryJob(t.Context(), a, c, job.ID, dispatch); !errors.Is(err, ErrTransition) {
		t.Fatal("parallel dispatch admitted", err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("uncertain result succeeded")
	}
	saved, err := s.MemoryJob(t.Context(), a, c, job.ID)
	if err != nil || saved.State != "outcome_unknown" {
		t.Fatal(saved.State, err)
	}
	var run string
	if err = s.pool.QueryRow(t.Context(), `SELECT cortex_run_id FROM memory_jobs WHERE id=$1`, job.ID).Scan(&run); err != nil {
		t.Fatal(err)
	}
	outcome, err := s.memory.Outcome(t.Context(), job.SpaceID, run)
	if err != nil || strings.Contains(outcome.ErrorMessage, "fixture lost response") {
		t.Fatal("raw runtime failure retained", err)
	}
	if _, err = s.RunMemoryJob(t.Context(), a, c, job.ID, dispatch); !errors.Is(err, ErrTransition) || calls.Load() != 1 {
		t.Fatal("uncertain attempt repeated", err, calls.Load())
	}
}

func TestMemoryExecutionCancellationStopsProvider(t *testing.T) {
	s, a, c, job := memoryExecutionFixture(t)
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.RunMemoryJob(t.Context(), a, c, job.ID, func(ctx context.Context, _ MemoryTarget, _ string, _ localcli.InferenceRequest) (localcli.InferenceResult, error) {
			close(entered)
			<-ctx.Done()
			return localcli.InferenceResult{}, ctx.Err()
		})
		done <- err
	}()
	<-entered
	saved, err := s.MemoryJob(t.Context(), a, c, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CancelMemoryJob(t.Context(), a, c, job.ID, uuid.NewString(), saved.Revision); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel succeeded as consolidation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled provider remained live")
	}
	saved, err = s.MemoryJob(t.Context(), a, c, job.ID)
	if err != nil || saved.State != "cancelled" {
		t.Fatal(saved.State, err)
	}
	tx, err := s.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	pending, err := s.memory.Pending(t.Context(), tx, job.SpaceID, job.ObservationIDs)
	if err != nil || !pending {
		t.Fatal("cancel acknowledged observations", err)
	}
}

func TestMemoryExecutionRejectsForeignDeviceAndMalformedOutput(t *testing.T) {
	s, a, c, job := memoryExecutionFixture(t)
	wrong := a
	wrong.DeviceID = uuid.NewString()
	calls := 0
	dispatch := func(context.Context, MemoryTarget, string, localcli.InferenceRequest) (localcli.InferenceResult, error) {
		calls++
		return localcli.InferenceResult{Text: "not a consolidation"}, nil
	}
	if _, err := s.RunMemoryJob(t.Context(), wrong, c, job.ID, dispatch); !errors.Is(err, ErrDenied) || calls != 0 {
		t.Fatal("foreign device ran", err)
	}
	saved, err := s.RunMemoryJob(t.Context(), a, c, job.ID, dispatch)
	if err == nil || saved.State != "failed" || calls != 1 {
		t.Fatal(saved.State, err, calls)
	}
}

func TestMemoryExecutionRecoversSavedResultWithoutProvider(t *testing.T) {
	s, a, c, job := memoryExecutionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	dispatch := func(context.Context, MemoryTarget, string, localcli.InferenceRequest) (localcli.InferenceResult, error) {
		cancel()
		return localcli.InferenceResult{Text: memoryTestOutput}, nil
	}
	saved, err := s.RunMemoryJob(ctx, a, c, job.ID, dispatch)
	if err == nil || saved.State != "result_ready" {
		t.Fatal("received result lost after disconnect", saved.State, err)
	}
	// A replacement coordinator can also crash after taking the saved response.
	if _, _, err = s.claimMemoryExecution(t.Context(), a, c, job.ID, uuid.NewString(), false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE memory_jobs SET claim_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	saved, err = s.RunMemoryJob(t.Context(), a, c, job.ID, nil)
	if err != nil || saved.State != "completed" {
		t.Fatal("saved response could not commit", saved.State, err)
	}
}

func TestMemoryExecutionExpiredProviderClaimBecomesUnknown(t *testing.T) {
	s, a, c, job := memoryExecutionFixture(t)
	if _, err := s.pool.Exec(t.Context(), `UPDATE memory_jobs SET state='running',started_at=now()-interval '5 minutes',claim_id=$2,claim_until=now()-interval '1 second' WHERE id=$1`, job.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	saved, err := s.RunMemoryJob(t.Context(), a, c, job.ID, func(context.Context, MemoryTarget, string, localcli.InferenceRequest) (localcli.InferenceResult, error) {
		calls++
		return localcli.InferenceResult{}, nil
	})
	if !errors.Is(err, ErrTransition) || saved.State != "outcome_unknown" || calls != 0 {
		t.Fatal("expired provider call repeated", saved.State, err, calls)
	}
}

func TestMemoryExecutionSourceRevocationFencesCommit(t *testing.T) {
	s, actors, c, project, source, input := memoryFixture(t)
	input.Visibility = "project"
	observation, err := s.SaveMemoryObservation(t.Context(), actors[0], c.ID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateMemoryJob(t.Context(), actors[0], c.ID, uuid.NewString(), MemoryJobInput{ProjectID: project.ID, Visibility: "project", Target: MemoryTarget{Location: "local", ProfileID: source.ProfileID}, Model: "fixture", ObservationIDs: []string{observation.ID}})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.RunMemoryJob(t.Context(), actors[0], c.ID, job.ID, func(ctx context.Context, _ MemoryTarget, _ string, _ localcli.InferenceRequest) (localcli.InferenceResult, error) {
		tx, e := s.pool.Begin(ctx)
		if e != nil {
			return localcli.InferenceResult{}, e
		}
		defer tx.Rollback(ctx)
		if _, e = s.lock(ctx, tx, actors[0], c.ID, true); e != nil {
			return localcli.InferenceResult{}, e
		}
		if _, e = tx.Exec(ctx, `UPDATE sessions SET shared=false,permission_revision=permission_revision+1 WHERE id=$1`, source.ID); e != nil {
			return localcli.InferenceResult{}, e
		}
		if e = tx.Commit(ctx); e != nil {
			return localcli.InferenceResult{}, e
		}
		return localcli.InferenceResult{Text: memoryTestOutput}, nil
	})
	if err == nil || saved.State == "completed" || len(saved.ObservationIDs) != 0 {
		t.Fatal("revoked consolidation committed", saved, err)
	}
	tx, e := s.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(t.Context())
	pending, e := s.memory.Pending(t.Context(), tx, job.SpaceID, job.ObservationIDs)
	if e != nil || !pending {
		t.Fatal("revoked observations acknowledged", e)
	}
	values, e := s.memory.Memories(t.Context(), tx, job.SpaceID, nil, "", 50)
	if e != nil || len(values) != 0 {
		t.Fatal("revoked memory retained", e)
	}
}

func TestMemoryExecutionRejectsRetainedResultTampering(t *testing.T) {
	s, a, c, job := memoryExecutionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	saved, err := s.RunMemoryJob(ctx, a, c, job.ID, func(context.Context, MemoryTarget, string, localcli.InferenceRequest) (localcli.InferenceResult, error) {
		cancel()
		return localcli.InferenceResult{Text: memoryTestOutput}, nil
	})
	if err == nil || saved.State != "result_ready" {
		t.Fatal(saved.State, err)
	}
	if _, err = s.pool.Exec(t.Context(), `UPDATE memory_jobs SET inference_result=jsonb_set(inference_result,'{text}','"changed output"'::jsonb) WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if _, err = s.RunMemoryJob(t.Context(), a, c, job.ID, func(context.Context, MemoryTarget, string, localcli.InferenceRequest) (localcli.InferenceResult, error) {
		calls++
		return localcli.InferenceResult{}, nil
	}); !errors.Is(err, ErrRevision) || calls != 0 {
		t.Fatal("changed response accepted", err, calls)
	}
}

func TestMemoryExecutionCloudEpochAndProviderBinding(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			s, a, c, local := memoryExecutionFixture(t)
			if _, err := s.CancelMemoryJob(t.Context(), a, c, local.ID, uuid.NewString(), local.Revision); err != nil {
				t.Fatal(err)
			}
			cloud, err := NewWithProviderKey(s.pool, "fixture", bytes.Repeat([]byte{7}, 32))
			if err != nil {
				t.Fatal(err)
			}
			cloud.SetMemoryStore(s.memory)
			account, grant := addCloud(t, cloud, a, "Claude")
			login, claim := startCloudLogin(t, cloud, a, account, grant)
			if _, err = cloud.ReportRunnerLogin(t.Context(), grant.Token, login.ID, claim, loginProof("fixture")); err != nil {
				t.Fatal(err)
			}
			job, err := cloud.CreateMemoryJob(t.Context(), a, c, uuid.NewString(), MemoryJobInput{ProjectID: local.ProjectID, Visibility: "private", Target: MemoryTarget{Location: "cloud", AccountID: account.ID}, Model: "fixture-claude", ObservationIDs: local.ObservationIDs})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			result, err := cloud.RunMemoryJob(t.Context(), a, c, job.ID, func(ctx context.Context, target MemoryTarget, provider string, input localcli.InferenceRequest) (localcli.InferenceResult, error) {
				calls++
				if provider != "Claude" || target != job.Target || input.Model != "fixture-claude" {
					t.Fatal("target changed")
				}
				if changed {
					if _, e := s.pool.Exec(ctx, `UPDATE provider_installations SET epoch=epoch+1 WHERE id=$1`, account.InstallationID); e != nil {
						return localcli.InferenceResult{}, e
					}
				}
				return localcli.InferenceResult{Text: memoryTestOutput}, nil
			})
			if calls != 1 {
				t.Fatal(calls)
			}
			if changed {
				if err == nil || result.State == "completed" {
					t.Fatal("changed installation committed", result.State, err)
				}
			} else if err != nil || result.State != "completed" {
				t.Fatal(result.State, err)
			}
		})
	}
}
