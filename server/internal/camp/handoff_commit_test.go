package camp

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

func acknowledgedHandoff(t *testing.T, f transferWorkspaceFixture, h Handoff, local bool) (Handoff, auth.Actor, string, HandoffAck) {
	t.Helper()
	a, token := f.owner, ""
	if !local {
		a = auth.Actor{}
		token = f.grant.Token
	}
	claim := uuid.NewString()
	if _, err := f.git.store.ClaimHandoff(t.Context(), a, token, h.ID, claim); err != nil {
		t.Fatal(err)
	}
	ack := HandoffAck{ClaimID: claim, Stopped: true, PayloadSHA256: f.upload.PayloadSHA256, ReviewSHA256: f.upload.ReviewSHA256}
	h, err := f.git.store.AcknowledgeHandoff(t.Context(), a, token, h.ID, ack)
	if err != nil {
		t.Fatal(err)
	}
	return h, a, token, ack
}

func TestHandoffCommitFreshProofAndAtomicReplay(t *testing.T) {
	for _, local := range []bool{true, false} {
		name := "cloud"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			f, h := handoffFixture(t, local)
			h, a, token, ack := acknowledgedHandoff(t, f, h, local)
			in := HandoffCommitInput{ExpectedHandoffRevision: h.Revision}
			key := uuid.NewString()
			requested, err := f.git.store.RequestHandoffCommit(t.Context(), f.owner, f.camp.ID, f.session.ID, h.ID, key, in)
			if err != nil {
				t.Fatal(err)
			}
			if requested.State != "acknowledged" || !validID(requested.CommitNonce) {
				t.Fatal("Missing fresh source challenge")
			}
			current, err := f.git.store.Session(t.Context(), f.owner, f.camp.ID, f.session.ID)
			if err != nil || current.ExecutionEpoch != f.session.ExecutionEpoch || current.ExecutionWorkspaceID != f.session.ExecutionWorkspaceID {
				t.Fatal("Owner request moved execution", err)
			}
			replay, err := f.git.store.RequestHandoffCommit(t.Context(), f.owner, f.camp.ID, f.session.ID, h.ID, key, in)
			if err != nil || replay.CommitNonce != requested.CommitNonce {
				t.Fatal("Commit request replay changed challenge", err)
			}
			proof := HandoffFinishInput{HandoffAck: ack, CommitNonce: requested.CommitNonce}
			bad := proof
			bad.CommitNonce = uuid.NewString()
			if _, err = f.git.store.FinishHandoff(t.Context(), a, token, h.ID, bad); !errors.Is(err, ErrConflict) {
				t.Fatal("Old or wrong challenge accepted", err)
			}
			var wg sync.WaitGroup
			results := make(chan Handoff, 4)
			failures := make(chan error, 4)
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, err := f.git.store.FinishHandoff(t.Context(), a, token, h.ID, proof)
					results <- result
					failures <- err
				}()
			}
			wg.Wait()
			close(results)
			close(failures)
			for err := range failures {
				if err != nil {
					t.Fatal(err)
				}
			}
			for result := range results {
				if result.State != "committed" || result.ExecutionEpoch != f.session.ExecutionEpoch+1 {
					t.Fatal("Repeated completion changed assignment", result)
				}
			}
			current, err = f.git.store.Session(t.Context(), f.owner, f.camp.ID, f.session.ID)
			if err != nil || current.ExecutionWorkspaceID != f.target.ID || current.ExecutionEpoch != f.session.ExecutionEpoch+1 {
				t.Fatal("Destination not assigned atomically", err)
			}
			upload, err := f.git.store.Transfer(t.Context(), f.owner, f.camp.ID, f.session.ID, f.upload.ID)
			if err != nil || upload.State != "completed" || upload.UploadedBytes != 0 {
				t.Fatal("Upload was not consumed", err)
			}
			if _, err = f.git.store.CancelTransfer(t.Context(), f.owner, f.camp.ID, f.session.ID, f.upload.ID); !errors.Is(err, ErrTransition) {
				t.Fatal("Completed transfer was cancelled", err)
			}
			if _, err = f.git.store.pool.Exec(t.Context(), `UPDATE session_transfers SET expires_at=now()-interval '1 second' WHERE id=$1`, f.upload.ID); err != nil {
				t.Fatal(err)
			}
			target, err := f.git.store.Execution(t.Context(), f.owner, f.camp.ID, f.session.ID)
			if err != nil || !target.Available || !target.RequiresTransfer {
				t.Fatal("Transferred files lost their next-move guard", err)
			}
			if !local {
				old, err := f.git.Workspace(t.Context(), f.owner, f.camp.ID, f.source.ID)
				if err != nil || old.State != "cancelled" {
					t.Fatal("Old runtime remained eligible", err)
				}
			}
			request, err := f.git.store.CreateRequest(t.Context(), f.owner, f.camp.ID, f.session.ID, uuid.NewString(), RequestInput{Prompt: "Continue in destination"})
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := f.git.ClaimRunnerRequest(t.Context(), f.grant.Token, f.target.ID, uuid.NewString())
			if err != nil || claimed == nil || claimed.ID != request.ID {
				t.Fatal("Destination cannot claim continued work", err)
			}
			bad = proof
			bad.Stopped = false
			if _, err = f.git.store.FinishHandoff(t.Context(), a, token, h.ID, bad); err == nil {
				t.Fatal("Changed replay was accepted")
			}
		})
	}
}

func TestHandoffCommitRequiresExplicitImplementationPermissions(t *testing.T) {
	f, h := handoffFixture(t, true, "implementation")
	h, a, token, ack := acknowledgedHandoff(t, f, h, true)
	in := HandoffCommitInput{ExpectedHandoffRevision: h.Revision}
	if _, err := f.git.store.RequestHandoffCommit(t.Context(), f.owner, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), in); !errors.Is(err, ErrDenied) {
		t.Fatal("Implementation did not require approval", err)
	}
	in.ExecutionApproved = true
	if _, err := f.git.store.RequestHandoffCommit(t.Context(), f.owner, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), in); !errors.Is(err, ErrInput) {
		t.Fatal("Implicit permission settings accepted", err)
	}
	in.Permissions = localcli.PermissionSettings{"sandbox": "danger-full-access", "approval": "never", "reviewer": "user"}
	requested, err := f.git.store.RequestHandoffCommit(t.Context(), f.owner, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.git.store.FinishHandoff(t.Context(), a, token, h.ID, HandoffFinishInput{HandoffAck: ack, CommitNonce: requested.CommitNonce}); err != nil {
		t.Fatal(err)
	}
	target, err := f.git.store.Execution(t.Context(), f.owner, f.camp.ID, f.session.ID)
	if err != nil || target.Mode != "implementation" || target.Permissions["sandbox"] != "danger-full-access" || target.Permissions["approval"] != "never" {
		t.Fatal("Approved permissions changed", err)
	}
}

func TestHandoffCommitInvalidationLeavesSourceAssigned(t *testing.T) {
	for _, change := range []string{"expired", "transcript", "permissions", "destination", "released"} {
		t.Run(change, func(t *testing.T) {
			f, h := handoffFixture(t, true)
			h, a, token, ack := acknowledgedHandoff(t, f, h, true)
			requested, err := f.git.store.RequestHandoffCommit(t.Context(), f.owner, f.camp.ID, f.session.ID, h.ID, uuid.NewString(), HandoffCommitInput{ExpectedHandoffRevision: h.Revision})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "expired":
				_, err = f.git.store.pool.Exec(t.Context(), `UPDATE transfer_handoffs SET expires_at=now()-interval '1 second' WHERE id=$1`, h.ID)
			case "transcript":
				_, err = f.git.store.pool.Exec(t.Context(), `UPDATE sessions SET transcript_revision=transcript_revision+1 WHERE id=$1`, f.session.ID)
			case "permissions":
				_, err = f.git.store.pool.Exec(t.Context(), `UPDATE sessions SET permission_revision=permission_revision+1 WHERE id=$1`, f.session.ID)
			case "destination":
				_, err = f.git.store.pool.Exec(t.Context(), `UPDATE ticket_workspaces SET revision=revision+1 WHERE id=$1`, f.target.ID)
			case "released":
				_, err = f.git.store.ReleaseHandoff(t.Context(), f.owner, f.camp.ID, f.session.ID, h.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.git.store.FinishHandoff(t.Context(), a, token, h.ID, HandoffFinishInput{HandoffAck: ack, CommitNonce: requested.CommitNonce}); err == nil {
				t.Fatal("Changed transfer was committed")
			}
			current, err := f.git.store.Session(t.Context(), f.owner, f.camp.ID, f.session.ID)
			if err != nil || current.ExecutionEpoch != f.session.ExecutionEpoch || current.ExecutionLocation != "local" {
				t.Fatal("Failed commit changed source", err)
			}
		})
	}
}
