package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

type exportRecord struct {
	ID             string `json:"id"`
	ClaimID        string `json:"claim_id"`
	Origin         string `json:"origin"`
	InstallationID string `json:"installation_id"`
	Epoch          int64  `json:"epoch"`
	Failed         bool   `json:"failed,omitempty"`
}

func exportPayloadName(id string) string { return "export-" + id + ".json" }

func (c *checkout) loadExport() (*exportRecord, error) {
	if _, err := c.root.Lstat("export.json"); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, ErrState
	}
	var r exportRecord
	if c.readJSON("export.json", &r) != nil || !validUUID(r.ID) || !validUUID(r.ClaimID) || !validUUID(r.InstallationID) || r.Origin == "" || r.Epoch < 1 {
		return nil, ErrState
	}
	return &r, nil
}

func (c *checkout) clearExport(r *exportRecord) error {
	for _, name := range []string{exportPayloadName(r.ID), "export.json"} {
		if err := c.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrState
		}
	}
	dir, err := c.root.Open(".")
	if err != nil {
		return ErrState
	}
	defer dir.Close()
	return dir.Sync()
}

func validExportClaim(value camp.RunnerWorkspaceExport, r *exportRecord, account camp.RunnerAccountState) bool {
	e, w := value.Export, value.Workspace
	return validWorkspace(w) && e.ID == r.ID && e.ClaimID == r.ClaimID && oneOf(e.State, "claimed", "ready") &&
		e.CampID == w.CampID && e.SessionID == w.SessionID && e.OwnerUserID == w.OwnerUserID && e.WorkspaceID == w.ID && e.WorkspaceRevision == w.Revision &&
		w.ProviderInstallationID == account.InstallationID && w.ProviderEpoch == account.Epoch && oneOf(e.Kind, "inspect", "capture")
}

// The worker serializes this with provider turns. Once captured, the private
// payload is reused across process restarts and lost upload receipts.
func (w *Worker) exportTick(ctx context.Context, account camp.RunnerAccountState) (bool, error) {
	r, err := w.checkout.loadExport()
	if err != nil {
		return false, err
	}
	if r != nil && (r.Origin != w.client.origin || r.InstallationID != account.InstallationID || r.Epoch != account.Epoch) {
		return true, w.checkout.clearExport(r)
	}
	if r == nil {
		var next *struct {
			ID string `json:"id"`
		}
		if err = w.client.call(ctx, "GET", "/v1/runner/exports/next", nil, &next); err != nil {
			return false, err
		}
		if next == nil {
			return false, nil
		}
		if !validUUID(next.ID) {
			return false, ErrState
		}
		r = &exportRecord{ID: next.ID, ClaimID: uuid.NewString(), Origin: w.client.origin, InstallationID: account.InstallationID, Epoch: account.Epoch}
		if err = w.checkout.saveJSON("export.json", r); err != nil {
			return false, err
		}
	}
	base := "/v1/runner/exports/" + r.ID
	input := map[string]string{"claim_id": r.ClaimID}
	onError := func(err error) (bool, error) {
		if errors.Is(err, ErrRejected) {
			if cleanup := w.client.call(ctx, "POST", base+"/fail", input, nil); cleanup != nil && !errors.Is(cleanup, ErrRejected) {
				return true, cleanup
			}
			return true, w.checkout.clearExport(r)
		}
		return true, err
	}
	var claim camp.RunnerWorkspaceExport
	if err = w.client.call(ctx, "POST", base+"/claim", input, &claim); err != nil {
		return onError(err)
	}
	if !validExportClaim(claim, r, account) {
		return true, ErrState
	}
	if claim.Export.State == "ready" {
		return true, w.checkout.clearExport(r)
	}
	work, cancel := context.WithDeadline(ctx, claim.Export.ExpiresAt)
	defer cancel()
	var raw []byte
	if !r.Failed {
		name := exportPayloadName(r.ID)
		if _, err = w.checkout.root.Lstat(name); err == nil {
			raw, err = privateFile(filepath.Join(w.checkout.dir, name), transfer.MaxWireBytes)
		} else if errors.Is(err, os.ErrNotExist) {
			raw, err = w.captureExport(work, claim)
			if err == nil {
				err = w.checkout.saveExportPayload(name, raw)
			}
		}
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		if err != nil {
			r.Failed = true
			if err = w.checkout.saveJSON("export.json", r); err != nil {
				return true, err
			}
		}
	}
	if r.Failed {
		if err = w.client.call(work, "POST", base+"/fail", input, nil); err != nil {
			return onError(err)
		}
		return true, w.checkout.clearExport(r)
	}
	var result camp.WorkspaceExport
	meta := camp.WorkspaceExportResultInput{ClaimID: r.ClaimID, SHA256: payloadHash(raw), Bytes: len(raw)}
	if err = w.client.call(work, "POST", base+"/result", meta, &result); err != nil {
		return onError(err)
	}
	if result.ID != r.ID || result.ClaimID != r.ClaimID || result.ResultBytes != len(raw) || result.ResultSHA256 != meta.SHA256 {
		return true, ErrState
	}
	uploaded := map[int]bool{}
	for _, i := range result.Uploaded {
		if i < 0 || i >= (len(raw)+camp.TransferChunkBytes-1)/camp.TransferChunkBytes {
			return true, ErrState
		}
		uploaded[i] = true
	}
	for offset, index := 0, 0; offset < len(raw); index++ {
		end := min(offset+camp.TransferChunkBytes, len(raw))
		if !uploaded[index] {
			part := struct {
				ClaimID string `json:"claim_id"`
				camp.TransferChunk
			}{r.ClaimID, camp.TransferChunk{SHA256: payloadHash(raw[offset:end]), Data: raw[offset:end]}}
			if err = w.client.call(work, "PUT", base+"/chunks/"+strconv.Itoa(index), part, nil); err != nil {
				return onError(err)
			}
		}
		offset = end
	}
	if err = w.client.callTimeout(work, "POST", base+"/complete", input, &result, 30*time.Second); err != nil {
		return onError(err)
	}
	if result.ID != r.ID || result.ClaimID != r.ClaimID || result.State != "ready" || result.ResultSHA256 != meta.SHA256 || result.ResultBytes != len(raw) {
		return true, ErrState
	}
	return true, w.checkout.clearExport(r)
}

func (w *Worker) captureExport(ctx context.Context, claim camp.RunnerWorkspaceExport) ([]byte, error) {
	workspace, e := claim.Workspace, claim.Export
	if err := w.checkout.verify(ctx, workspace); err != nil {
		return nil, err
	}
	var value any
	var branch, base string
	if e.Kind == "inspect" {
		changes, err := transfer.Changes(ctx, w.checkout.workdir(workspace))
		if err != nil {
			return nil, err
		}
		value, branch, base = changes, changes.Branch, changes.BaseCommit
	} else {
		manifest, err := transfer.Snapshot(ctx, w.checkout.workdir(workspace), e.Paths)
		if err != nil {
			return nil, err
		}
		remoteBase := workspace.BaseCommit
		if workspace.RemoteBaseCommit != "" {
			remoteBase = workspace.RemoteBaseCommit
		}
		if manifest.BaseCommit != remoteBase {
			archive, err := transfer.CaptureCommits(ctx, w.checkout.workdir(workspace), remoteBase, manifest.BaseCommit)
			if err != nil {
				return nil, err
			}
			manifest.Commits = &archive
			if err = transfer.VerifySource(ctx, w.checkout.workdir(workspace), manifest); err != nil {
				return nil, err
			}
		}
		value, branch, base = manifest, manifest.Branch, manifest.BaseCommit
	}
	if branch != workspace.Branch {
		return nil, ErrState
	}
	if _, err := w.checkout.git(ctx, workspace, "", "merge-base", "--is-ancestor", workspace.BaseCommit, base); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > transfer.MaxWireBytes {
		return nil, ErrState
	}
	return raw, nil
}

func (c *checkout) saveExportPayload(name string, raw []byte) error {
	if len(raw) < 2 || len(raw) > transfer.MaxWireBytes {
		return ErrState
	}
	tmp := "export-temp-" + uuid.NewString()
	f, err := c.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrState
	}
	defer c.root.Remove(tmp)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil || closed != nil || c.root.Rename(tmp, name) != nil {
		return ErrState
	}
	dir, err := c.root.Open(".")
	if err != nil {
		return ErrState
	}
	defer dir.Close()
	return dir.Sync()
}
