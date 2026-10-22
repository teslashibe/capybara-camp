package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func prepareSourceHandoff(t *testing.T, f fixture, g *camp.GitStore, source camp.Workspace, local ...bool) (camp.Handoff, camp.Session) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.worker.checkout.workdir(source), "README.md"), []byte("reviewed source contents\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := transfer.Capture(t.Context(), f.worker.checkout.workdir(source), []string{"README.md"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(manifest)
	review, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.store.Session(t.Context(), f.owner, source.CampID, source.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	input := camp.TransferInput{TargetLocation: "cloud", TargetAccountID: source.ProviderAccountID, ExpectedEpoch: session.ExecutionEpoch, ExpectedRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision, ExpectedPermissionRevision: session.PermissionRevision, PayloadSHA256: payloadHash(raw), ReviewSHA256: review, PayloadBytes: len(raw)}
	if len(local) > 0 && local[0] {
		input.TargetLocation, input.TargetAccountID = "local", ""
	}
	upload, err := f.store.CreateTransfer(t.Context(), f.owner, source.CampID, source.SessionID, uuid.NewString(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.PutTransferChunk(t.Context(), f.owner, source.CampID, source.SessionID, upload.ID, 0, camp.TransferChunk{Data: raw, SHA256: payloadHash(raw)}); err != nil {
		t.Fatal(err)
	}
	upload, err = f.store.CompleteTransfer(t.Context(), f.owner, source.CampID, source.SessionID, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if input.TargetLocation == "local" {
		destination, state := t.TempDir(), t.TempDir()
		fixtureCommand(t, destination, "clone", "--no-local", "--no-hardlinks", "--", f.worker.checkout.workdir(source), ".")
		if err := os.Chmod(destination, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(state, 0700); err != nil {
			t.Fatal(err)
		}
		stager, err := transfer.OpenStager(destination, state)
		if err != nil {
			t.Fatal(err)
		}
		defer stager.Close()
		receipt, err := stager.Apply(t.Context(), manifest, review, source.Branch)
		if err != nil {
			t.Fatal(err)
		}
		// This fixture installs real files but only attests a history revision.
		// Native history installation is a separate acceptance requirement.
		rawReceipt, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		h, err := f.store.BeginHandoff(t.Context(), f.owner, source.CampID, source.SessionID, upload.ID, uuid.NewString(), camp.HandoffInput{ExpectedTransferRevision: upload.Revision, LocalDestination: &camp.LocalHandoffDestination{ProfileID: session.ProfileID, SessionID: uuid.NewString(), Purpose: source.Purpose, ReceiptSHA256: payloadHash(rawReceipt), TranscriptRevision: session.TranscriptRevision}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			data, err := os.ReadFile(filepath.Join(destination, "README.md"))
			if err != nil || string(data) != "reviewed source contents\n" {
				t.Error("local destination lost reviewed files", err)
			}
		})
		return h, session
	}
	target, err := g.CreateTransferWorkspace(t.Context(), f.owner, source.CampID, source.SessionID, upload.ID, uuid.NewString(), camp.WorkspaceInput{ProviderAccountID: source.ProviderAccountID, GitConnectionID: source.GitConnectionID, GitHubInstallationID: source.GitHubInstallationID, RepositoryID: source.RepositoryID, InstallationPage: 1, RepositoryPage: 1, Purpose: source.Purpose, ExpectedSessionRevision: session.Revision, ExpectedProjectRevision: source.ProjectRevision})
	if err != nil {
		t.Fatal(err)
	}
	claim := uuid.NewString()
	if _, err = g.ClaimRunnerWorkspace(t.Context(), f.grant.Token, target.ID, claim); err != nil {
		t.Fatal(err)
	}
	token, err := g.RunnerWorkspaceToken(t.Context(), f.grant.Token, target.ID, claim)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.worker.prepareTransfer(t.Context(), target, claim, token)
	if err != nil {
		t.Fatal(err)
	}
	target, err = g.ReportRunnerWorkspace(t.Context(), f.grant.Token, target.ID, claim, camp.WorkspaceReport{State: "ready", BaseCommit: target.BaseCommit, Branch: target.Branch, TransferReceipt: receipt})
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.store.BeginHandoff(t.Context(), f.owner, source.CampID, source.SessionID, upload.ID, uuid.NewString(), camp.HandoffInput{WorkspaceID: target.ID, ExpectedWorkspaceRevision: target.Revision, ExpectedTransferRevision: upload.Revision})
	if err != nil {
		t.Fatal(err)
	}
	return h, session
}

func assertProviderExited(t *testing.T, record string) {
	t.Helper()
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "pid:"); ok {
			pid, err := strconv.Atoi(value)
			if err != nil || pid <= 1 {
				t.Fatal("invalid fixture pid")
			}
			if err = syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatal("provider process remained alive", err)
			}
			count++
		}
	}
	if count != 1 {
		t.Fatal("unexpected provider launches", count)
	}
}

func TestRunnerHandoffVerifiesStoppedSourceAndRecoversReceipts(t *testing.T) {
	for _, tc := range []struct {
		provider string
		change   bool
		local    bool
	}{{"Codex", false, false}, {"Claude", false, false}, {"Codex", true, false}, {"Codex", false, true}, {"Claude", false, true}, {"Codex", true, true}} {
		name := tc.provider
		if tc.local {
			name += " local destination"
		}
		if tc.change {
			name += " changed source"
		}
		t.Run(name, func(t *testing.T) {
			finished := make(chan struct{}, 1)
			var finishDropped, ackDropped, commitDropped atomic.Bool
			var ackCalls atomic.Int32
			f, g, source, record := turnFixture(t, tc.provider, "complete", func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(r.URL.Path, "/v1/runner/handoffs/") && strings.HasSuffix(r.URL.Path, "/finish") && commitDropped.CompareAndSwap(false, true) {
						next.ServeHTTP(discardResponse{}, r)
						w.WriteHeader(503)
						return
					}
					if strings.HasSuffix(r.URL.Path, "/actions") && r.Method == "POST" {
						var body camp.RequestAction
						_ = json.NewDecoder(r.Body).Decode(&body)
						raw, _ := json.Marshal(body)
						r.Body = io.NopCloser(bytes.NewReader(raw))
						if body.Action == "finish" && finishDropped.CompareAndSwap(false, true) {
							next.ServeHTTP(discardResponse{}, r)
							finished <- struct{}{}
							<-r.Context().Done()
							return
						}
					}
					if strings.HasPrefix(r.URL.Path, "/v1/runner/handoffs/") && strings.HasSuffix(r.URL.Path, "/ack") {
						ackCalls.Add(1)
						if ackDropped.CompareAndSwap(false, true) {
							next.ServeHTTP(discardResponse{}, r)
							w.WriteHeader(503)
							return
						}
					}
					next.ServeHTTP(w, r)
				})
			})
			request, err := f.store.CreateRequest(t.Context(), f.owner, source.CampID, source.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Finish source before transfer"})
			if err != nil {
				t.Fatal(err)
			}
			cancel, done := startWorker(t, f.worker)
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("source did not finish")
			}
			stopWorker(t, cancel, done)
			pending, err := f.worker.checkout.loadTurn()
			if err != nil || pending == nil || pending.Phase != "finishing" {
				t.Fatal("lost finish was not retained", err)
			}
			status, err := f.store.Request(t.Context(), f.owner, source.CampID, request.ID)
			if err != nil || status.State != "completed" {
				t.Fatal("source was not terminal", err)
			}
			assertProviderExited(t, record)
			h, session := prepareSourceHandoff(t, f, g, source, tc.local)
			if tc.change {
				if err = os.WriteFile(filepath.Join(f.worker.checkout.workdir(source), "README.md"), []byte("changed after review\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cancel, done = startWorker(t, f.worker)
			want := "acknowledged"
			if tc.change {
				want = "invalidated"
			}
			eventually(t, func() bool {
				value, err := f.store.Handoff(t.Context(), f.owner, source.CampID, source.SessionID, h.ID)
				return err == nil && value.State == want
			})
			eventually(t, func() bool {
				value, err := f.worker.checkout.loadHandoff()
				return err == nil && ((tc.change && value == nil) || (!tc.change && value != nil && value.Acknowledged))
			})
			stopWorker(t, cancel, done)
			if !tc.change && ackCalls.Load() != 2 {
				t.Fatal("lost source acknowledgement was not replayed", ackCalls.Load())
			}
			if tc.change && ackCalls.Load() != 0 {
				t.Fatal("changed files were acknowledged")
			}
			data, err := os.ReadFile(record)
			if err != nil || strings.Count(string(data), "turn\n") != 1 {
				t.Fatal("handoff repeated provider work", err)
			}
			current, err := f.store.Session(t.Context(), f.owner, source.CampID, source.SessionID)
			if err != nil || current.ExecutionWorkspaceID != source.ID || current.ExecutionEpoch != session.ExecutionEpoch {
				t.Fatal("source acknowledgement moved assignment", err)
			}
			if tc.change {
				return
			}
			h, err = f.store.Handoff(t.Context(), f.owner, source.CampID, source.SessionID, h.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.RequestHandoffCommit(t.Context(), f.owner, source.CampID, source.SessionID, h.ID, uuid.NewString(), camp.HandoffCommitInput{ExpectedHandoffRevision: h.Revision, ContextContinuationApproved: tc.local}); err != nil {
				t.Fatal(err)
			}
			cancel, done = startWorker(t, f.worker)
			eventually(t, func() bool {
				value, err := f.store.Handoff(t.Context(), f.owner, source.CampID, source.SessionID, h.ID)
				return err == nil && value.State == "committed"
			})
			eventually(t, func() bool { value, err := f.worker.checkout.loadHandoff(); return err == nil && value == nil })
			stopWorker(t, cancel, done)
			current, err = f.store.Session(t.Context(), f.owner, source.CampID, source.SessionID)
			if err != nil || current.ExecutionWorkspaceID != h.WorkspaceID || current.ExecutionEpoch != session.ExecutionEpoch+1 || !commitDropped.Load() {
				t.Fatal("Lost commit was not reconciled", err)
			}
			request, err = f.store.CreateRequest(t.Context(), f.owner, source.CampID, source.SessionID, uuid.NewString(), camp.RequestInput{Prompt: "Continue after transfer"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.local {
				if current.ExecutionLocation != "local" || current.LocalSessionID != h.LocalDestination.SessionID || request.ExecutionLocation != "local" || request.OwnerDeviceID != f.owner.DeviceID {
					t.Fatal("new local conversation not assigned")
				}
				if _, err = g.ClaimRunnerRequest(t.Context(), f.grant.Token, source.ID, uuid.NewString()); err == nil {
					t.Fatal("retired runner claimed local work")
				}
				claimed, err := f.store.ClaimAvailableWork(t.Context(), f.owner, source.CampID, uuid.NewString(), []string{source.SessionID})
				if err != nil || claimed == nil || claimed.ID != request.ID {
					t.Fatal("selected Mac could not claim continuation", err)
				}
				assertProviderExited(t, record)
				return
			}
			cancel, done = startWorker(t, f.worker)
			eventually(t, func() bool {
				value, err := f.store.Request(t.Context(), f.owner, source.CampID, request.ID)
				return err == nil && value.State == "completed"
			})
			stopWorker(t, cancel, done)
			for _, id := range []string{source.ID, h.WorkspaceID} {
				data, err = os.ReadFile(filepath.Join(f.worker.checkout.dir, "workspace-"+id, "README.md"))
				if err != nil || string(data) != "reviewed source contents\n" {
					t.Fatal("Reviewed files were not retained", err)
				}
			}
			data, err = os.ReadFile(record)
			if err != nil || strings.Count(string(data), "turn\n") != 2 {
				t.Fatal("Transfer repeated or skipped provider work", err)
			}
		})
	}
}

func TestRunnerHandoffRejectsSourceChangedAfterAcknowledgement(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run("local="+strconv.FormatBool(local), func(t *testing.T) {
			f, g, source, record := turnFixture(t, "Codex", "complete")
			h, session := prepareSourceHandoff(t, f, g, source, local)
			cancel, done := startWorker(t, f.worker)
			eventually(t, func() bool {
				value, err := f.store.Handoff(t.Context(), f.owner, source.CampID, source.SessionID, h.ID)
				return err == nil && value.State == "acknowledged"
			})
			stopWorker(t, cancel, done)
			h, err := f.store.Handoff(t.Context(), f.owner, source.CampID, source.SessionID, h.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.RequestHandoffCommit(t.Context(), f.owner, source.CampID, source.SessionID, h.ID, uuid.NewString(), camp.HandoffCommitInput{ExpectedHandoffRevision: h.Revision, ContextContinuationApproved: local}); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(f.worker.checkout.workdir(source), "README.md"), []byte("changed during review\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cancel, done = startWorker(t, f.worker)
			eventually(t, func() bool {
				value, err := f.store.Handoff(t.Context(), f.owner, source.CampID, source.SessionID, h.ID)
				return err == nil && value.State == "invalidated"
			})
			stopWorker(t, cancel, done)
			current, err := f.store.Session(t.Context(), f.owner, source.CampID, source.SessionID)
			if err != nil || current.ExecutionEpoch != session.ExecutionEpoch || current.ExecutionWorkspaceID != source.ID {
				t.Fatal("Changed source moved", err)
			}
			if _, err = os.Stat(record); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("Verification started a provider", err)
			}
		})
	}
}
