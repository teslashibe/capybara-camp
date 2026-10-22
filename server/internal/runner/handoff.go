package runner

import (
	"context"
	"errors"
	"os"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

type handoffRecord struct {
	ID             string `json:"id"`
	ClaimID        string `json:"claim_id"`
	Origin         string `json:"origin"`
	InstallationID string `json:"installation_id"`
	Epoch          int64  `json:"epoch"`
}

func (c *checkout) loadHandoff() (*handoffRecord, error) {
	if _, err := c.root.Lstat("handoff.json"); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, ErrState
	}
	var r handoffRecord
	if err := c.readJSON("handoff.json", &r); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(r.ID); err != nil {
		return nil, ErrState
	}
	if _, err := uuid.Parse(r.ClaimID); err != nil {
		return nil, ErrState
	}
	if _, err := uuid.Parse(r.InstallationID); err != nil || r.Epoch < 1 || r.Origin == "" {
		return nil, ErrState
	}
	return &r, nil
}

func (c *checkout) clearHandoff() error {
	if err := c.root.Remove("handoff.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir, err := c.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func validHandoffSource(source camp.HandoffSource, r handoffRecord, state camp.RunnerAccountState) bool {
	h, t, s, w := source.Handoff, source.Transfer, source.Session, source.Workspace
	return w != nil && validWorkspace(*w) && h.ID == r.ID && h.ClaimID == r.ClaimID && oneOf(h.State, "claimed", "acknowledged") && h.SourceLocation == "cloud" && h.SourceWorkspaceID == w.ID && h.SourceEpoch == s.ExecutionEpoch &&
		h.CampID == w.CampID && h.SessionID == w.SessionID && h.OwnerUserID == w.OwnerUserID &&
		t.ID == h.TransferID && t.CampID == h.CampID && t.SessionID == h.SessionID && t.OwnerUserID == h.OwnerUserID && t.SourceLocation == "cloud" && t.SourceWorkspaceID == w.ID && t.SourceEpoch == h.SourceEpoch && t.SourceRevision == s.Revision && t.SourceTranscriptRevision == s.TranscriptRevision && t.SourcePermissionRevision == s.PermissionRevision && t.State == "ready" &&
		s.ID == w.SessionID && s.OwnerUserID == w.OwnerUserID && s.ExecutionLocation == "cloud" && s.ExecutionWorkspaceID == w.ID &&
		w.ProviderInstallationID == state.InstallationID && w.ProviderEpoch == state.Epoch
}

// Called only after the serialized workspace/turn journals are reconciled.
// RunConversation has joined and closed its child processes before a turn is
// cleared. The server's hold prevents a new turn from being claimed meanwhile.
func (w *Worker) handoffTick(ctx context.Context, state camp.RunnerAccountState) (bool, error) {
	r, err := w.checkout.loadHandoff()
	if err != nil {
		return false, err
	}
	if r != nil && (r.Origin != w.client.origin || r.InstallationID != state.InstallationID || r.Epoch != state.Epoch) {
		return true, w.checkout.clearHandoff()
	}
	if r == nil {
		var next *struct {
			ID string `json:"id"`
		}
		if err = w.client.call(ctx, "GET", "/v1/runner/handoffs/next", nil, &next); err != nil {
			return false, err
		}
		if next == nil {
			return false, nil
		}
		if _, err = uuid.Parse(next.ID); err != nil {
			return false, ErrState
		}
		r = &handoffRecord{ID: next.ID, ClaimID: uuid.NewString(), Origin: w.client.origin, InstallationID: state.InstallationID, Epoch: state.Epoch}
		if err = w.checkout.saveJSON("handoff.json", r); err != nil {
			return false, err
		}
	}
	base := "/v1/runner/handoffs/" + r.ID
	input := map[string]string{"claim_id": r.ClaimID}
	var claimed camp.Handoff
	if err = w.client.call(ctx, "POST", base+"/claim", input, &claimed); err != nil {
		return w.handoffError(err)
	}
	if claimed.ID != r.ID || claimed.ClaimID != r.ClaimID {
		return true, ErrState
	}
	work, cancel := context.WithDeadline(ctx, claimed.ExpiresAt)
	defer cancel()
	var source camp.HandoffSource
	if err = w.client.call(work, "POST", base+"/source", input, &source); err != nil {
		return w.handoffError(err)
	}
	if !validHandoffSource(source, *r, state) {
		return true, ErrState
	}
	raw, err := w.client.transferPayload(work, base, r.ClaimID, source.Transfer)
	if err != nil {
		return w.handoffError(err)
	}
	manifest, err := transfer.Decode(raw)
	if err == nil {
		var digest string
		digest, err = manifest.Digest()
		if err == nil && (digest != source.Transfer.ReviewSHA256 || manifest.BaseCommit != source.Transfer.BaseCommit || manifest.Branch != source.Workspace.Branch || len(manifest.Files) != source.Transfer.FileCount) {
			err = ErrState
		}
	}
	if err != nil {
		return w.rejectHandoff(ctx, base, input)
	}
	turn, err := w.checkout.loadTurn()
	if err != nil {
		return true, err
	}
	if turn != nil {
		return true, ErrState
	}
	info, err := w.checkout.root.Lstat("workspace-" + source.Workspace.ID)
	if err == nil && (!info.IsDir() || !ownedPrivate(info)) {
		err = ErrState
	}
	if err == nil {
		_, err = w.checkout.git(work, *source.Workspace, "", "merge-base", "--is-ancestor", source.Workspace.BaseCommit, manifest.BaseCommit)
	}
	if err == nil {
		err = transfer.VerifySource(work, w.checkout.workdir(*source.Workspace), manifest)
	}
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	if err != nil {
		return w.rejectHandoff(ctx, base, input)
	}
	ack := camp.HandoffAck{ClaimID: r.ClaimID, Stopped: true, PayloadSHA256: source.Transfer.PayloadSHA256, ReviewSHA256: source.Transfer.ReviewSHA256}
	var accepted camp.Handoff
	if err = w.client.call(work, "POST", base+"/ack", ack, &accepted); err != nil {
		return w.handoffError(err)
	}
	if accepted.ID != r.ID || accepted.ClaimID != r.ClaimID || accepted.State != "acknowledged" {
		return true, ErrState
	}
	return true, w.checkout.clearHandoff()
}

func (w *Worker) rejectHandoff(ctx context.Context, base string, input map[string]string) (bool, error) {
	err := w.client.call(ctx, "POST", base+"/reject", input, nil)
	if err != nil && !errors.Is(err, ErrRejected) {
		return true, err
	}
	return true, w.checkout.clearHandoff()
}

func (w *Worker) handoffError(err error) (bool, error) {
	if errors.Is(err, ErrRejected) {
		return true, w.checkout.clearHandoff()
	}
	return true, err
}
