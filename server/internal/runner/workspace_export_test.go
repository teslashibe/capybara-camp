package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func TestRunnerWorkspaceExportRestartAndLostResponses(t *testing.T) {
	for _, kind := range []string{"inspect", "capture"} {
		t.Run(kind, func(t *testing.T) {
			var mu sync.Mutex
			dropped := map[string]bool{}
			f, _, workspace, calls := turnFixture(t, "Codex", "complete", func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(r.URL.Path, "/v1/runner/exports/") && r.Method != "GET" {
						stage := filepath.Base(r.URL.Path)
						mu.Lock()
						drop := !dropped[stage]
						dropped[stage] = true
						mu.Unlock()
						if drop {
							next.ServeHTTP(discardResponse{}, r)
							w.WriteHeader(503)
							return
						}
					}
					next.ServeHTTP(w, r)
				})
			})
			// A used checkout permits existing, uncommitted provider work.
			if err := f.worker.checkout.saveJSON("used-workspace-"+workspace.ID+".json", workspace); err != nil {
				t.Fatal(err)
			}
			original := []byte(strings.Repeat("selected safe fixture content\n", 12000))
			dir := f.worker.checkout.workdir(workspace)
			if err := os.WriteFile(filepath.Join(dir, "README.md"), original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PRIVATE_FIXTURE=excluded"), 0600); err != nil {
				t.Fatal(err)
			}
			session, err := f.store.Session(t.Context(), f.owner, workspace.CampID, workspace.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			ownerToken := "camp_" + strings.Repeat("o", 43)
			if _, err = f.api.Pool.Exec(t.Context(), `UPDATE devices SET token_hash=$2 WHERE id=$1`, f.owner.DeviceID, auth.HashToken(ownerToken)); err != nil {
				t.Fatal(err)
			}
			owner := &client{origin: f.worker.client.origin, token: ownerToken, http: f.worker.client.http}
			base := "/v1/camps/" + workspace.CampID + "/sessions/" + workspace.SessionID + "/exports"
			input := camp.WorkspaceExportInput{Kind: kind, Paths: []string{}, ExpectedEpoch: session.ExecutionEpoch, ExpectedRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision, ExpectedPermissionRevision: session.PermissionRevision}
			if kind == "capture" {
				input.Paths = []string{"README.md", ".env"}
			}
			var e camp.WorkspaceExport
			if err = owner.callKey(t.Context(), "POST", base, uuid.NewString(), input, &e); err != nil {
				t.Fatal(err)
			}
			state, err := f.store.RunnerAccount(t.Context(), f.grant.Token)
			if err != nil {
				t.Fatal(err)
			}
			finished := false
			for attempt := 0; attempt < 20; attempt++ {
				handled, err := f.worker.exportTick(t.Context(), state)
				if !handled || err != nil && !errors.Is(err, ErrUnavailable) {
					t.Fatal("export tick", handled, err)
				}
				if err == nil {
					finished = true
					break
				}
				// Reopen all export state after every ambiguous HTTP response.
				checkoutDir := f.worker.checkout.dir
				f.worker.checkout.close()
				f.worker.checkout, err = openCheckout(checkoutDir)
				if err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				chunkSent := dropped["0"]
				mu.Unlock()
				if chunkSent {
					if err = os.WriteFile(filepath.Join(dir, "README.md"), []byte("newer work stays local\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !finished {
				t.Fatal("did not reconcile")
			}
			if err = owner.call(t.Context(), "GET", base+"/"+e.ID, nil, &e); err != nil || e.State != "ready" {
				t.Fatal("result", err, e.State)
			}
			raw := []byte{}
			for i := 0; len(raw) < e.ResultBytes; i++ {
				var part camp.TransferChunk
				if err = owner.call(t.Context(), "GET", base+"/"+e.ID+"/chunks/"+strconv.Itoa(i), nil, &part); err != nil {
					t.Fatal(err)
				}
				if payloadHash(part.Data) != part.SHA256 {
					t.Fatal("chunk digest")
				}
				raw = append(raw, part.Data...)
			}
			if len(raw) != e.ResultBytes || payloadHash(raw) != e.ResultSHA256 {
				t.Fatal("result digest")
			}
			if kind == "capture" {
				m, err := transfer.Decode(raw)
				if err != nil || len(m.Files) != 1 || !bytes.Equal(m.Files[0].Data, original) || len(m.Excluded) != 1 || m.Excluded[0].Path != ".env" {
					t.Fatal("capture changed or exported credentials", err)
				}
			} else {
				var changes transfer.ChangeSet
				if json.Unmarshal(raw, &changes) != nil || len(changes.Files) != 2 || changes.Files[0].Path != ".env" || changes.Files[0].Excluded == "" {
					t.Fatal("inspection missing exclusion")
				}
			}
			if _, err = os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("export launched provider", err)
			}
			if saved, err := f.worker.checkout.loadExport(); err != nil || saved != nil {
				t.Fatal("journal retained", err)
			}
			if _, err = os.Lstat(filepath.Join(f.worker.checkout.dir, exportPayloadName(e.ID))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("payload retained", err)
			}
			after, err := f.store.Session(t.Context(), f.owner, workspace.CampID, workspace.SessionID)
			if err != nil || after.ExecutionEpoch != session.ExecutionEpoch || after.ExecutionWorkspaceID != session.ExecutionWorkspaceID {
				t.Fatal("export moved ownership", err)
			}
		})
	}
}

func TestRunnerWorkspaceExportPrivateCleanup(t *testing.T) {
	c, err := openCheckout(privateTemp(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	r := &exportRecord{ID: uuid.NewString(), ClaimID: uuid.NewString(), Origin: "https://camp.example", InstallationID: uuid.NewString(), Epoch: 1}
	if err = c.saveJSON("export.json", r); err != nil {
		t.Fatal(err)
	}
	if err = c.saveExportPayload(exportPayloadName(r.ID), []byte(`{"fixture":"private"}`)); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(c.dir, "export-temp-"+r.ID), []byte("interrupted private capture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = c.cleanCredentials(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(c.dir, "export-temp-"+r.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("abandoned export retained", err)
	}
	if saved, err := c.loadExport(); err != nil || saved == nil {
		t.Fatal("startup removed restart journal", err)
	}
	if err = c.eraseRuntime(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"export.json", exportPayloadName(r.ID)} {
		if _, err = os.Stat(filepath.Join(c.dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("erasure retained export", name, err)
		}
	}
}
