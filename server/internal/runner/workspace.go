package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

// Workspace journals hold identifiers and immutable checkout metadata only.
// They are private files, like .git/config, and survive grant rotation. Provider
// credentials and checkout tokens must never be written into this record.
type workspaceRecord struct {
	Workspace  camp.Workspace        `json:"workspace"`
	ClaimID    string                `json:"claim_id"`
	Phase      string                `json:"phase"`
	LeaseUntil time.Time             `json:"lease_until"`
	Deadline   time.Time             `json:"deadline"`
	Report     *camp.WorkspaceReport `json:"report,omitempty"`
}

func sameWorkspace(a, b camp.Workspace) bool {
	return a.ID == b.ID && a.SessionID == b.SessionID && a.CampID == b.CampID && a.OwnerUserID == b.OwnerUserID && a.ProjectID == b.ProjectID && a.ProviderAccountID == b.ProviderAccountID && a.ProviderInstallationID == b.ProviderInstallationID && a.ProviderEpoch == b.ProviderEpoch && a.GitConnectionID == b.GitConnectionID && a.GitEpoch == b.GitEpoch && a.GitHubUserID == b.GitHubUserID && a.GitHubInstallationID == b.GitHubInstallationID && a.RepositoryName == b.RepositoryName && a.RepositoryID == b.RepositoryID && a.BaseCommit == b.BaseCommit && a.Branch == b.Branch && a.Purpose == b.Purpose && a.SourceRevision == b.SourceRevision && a.ProjectRevision == b.ProjectRevision && a.TransferID == b.TransferID
}

func validWorkspaceRecord(r *workspaceRecord) bool {
	if r == nil || !validWorkspace(r.Workspace) {
		return false
	}
	if _, err := uuid.Parse(r.ClaimID); err != nil {
		return false
	}
	if r.Phase == "queued" {
		return r.Report == nil
	}
	if r.Deadline.IsZero() || r.LeaseUntil.IsZero() || r.LeaseUntil.After(r.Deadline) {
		return false
	}
	if r.Phase == "preparing" {
		return r.Report == nil
	}
	return r.Phase == "reporting" && r.Report != nil && (r.Report.State == "ready" && r.Report.BaseCommit == r.Workspace.BaseCommit && r.Report.Branch == r.Workspace.Branch && r.Report.FailureCode == "" || r.Report.State == "failed" && r.Report.FailureCode != "" && r.Report.BaseCommit == "" && r.Report.Branch == "")
}

func (j *journal) loadWorkspace() (*workspaceRecord, error) {
	f, err := j.root.OpenFile("workspace.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrState
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !ownedPrivate(info) || !info.Mode().IsRegular() || info.Size() > 16384 {
		return nil, ErrState
	}
	var r workspaceRecord
	decoder := json.NewDecoder(io.LimitReader(f, 16385))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) != nil || !validWorkspaceRecord(&r) {
		return nil, ErrState
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, ErrState
	}
	return &r, nil
}

func (j *journal) saveWorkspace(r *workspaceRecord) error {
	if !validWorkspaceRecord(r) {
		return ErrState
	}
	data, err := json.Marshal(r)
	if err != nil || len(data) > 16384 {
		return ErrState
	}
	name := "journal-" + uuid.NewString()
	f, err := j.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrState
	}
	defer j.root.Remove(name)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil || closed != nil || j.root.Rename(name, "workspace.json") != nil {
		return ErrState
	}
	return j.syncDir()
}

func (j *journal) clearWorkspace() error {
	if err := j.root.Remove("workspace.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	return j.syncDir()
}

func (w *Worker) discardWorkspace(r *workspaceRecord) error {
	if err := w.checkout.remove(r.Workspace); err != nil {
		return err
	}
	return w.journal.clearWorkspace()
}

func (w *Worker) workspaceTick(ctx context.Context, account camp.RunnerAccountState) error {
	r, err := w.journal.loadWorkspace()
	if err != nil {
		return err
	}
	if r == nil {
		// Reconcile the saved turn before asking the scheduler for another
		// ticket. A lost terminal receipt must not strand the previous turn.
		turn, err := w.checkout.loadTurn()
		if err != nil {
			return err
		}
		if turn != nil {
			if turn.Workspace.ProviderInstallationID != account.InstallationID || turn.Workspace.ProviderEpoch != account.Epoch {
				return w.checkout.holdTurn(turn)
			}
			if err = w.checkout.verify(ctx, turn.Workspace); err != nil {
				return err
			}
			return w.turnTick(ctx, account, turn.Workspace)
		}
		if handled, err := w.handoffTick(ctx, account); err != nil {
			return err
		} else if handled {
			return nil
		}
		if handled, err := w.exportTick(ctx, account); err != nil {
			return err
		} else if handled {
			return nil
		}
		if err = w.cleanupRetiredWorkspaces(ctx); err != nil {
			return err
		}
		next, err := w.client.nextWorkspace(ctx)
		if err != nil {
			return err
		}
		if next == nil {
			w.readyWorkspace = ""
			return nil
		}
		if !validWorkspace(*next) || next.ProviderInstallationID != account.InstallationID || next.ProviderEpoch != account.Epoch {
			return ErrState
		}
		if next.State == "ready" {
			if w.readyWorkspace != next.ID {
				if err = w.checkout.verify(ctx, *next); err != nil {
					return err
				}
				w.readyWorkspace = next.ID
			}
			return w.turnTick(ctx, account, *next)
		}
		if next.State != "queued" && next.State != "preparing" {
			return ErrState
		}
		r = &workspaceRecord{Workspace: *next, ClaimID: uuid.NewString(), Phase: "queued"}
		if err = w.journal.saveWorkspace(r); err != nil {
			return err
		}
	}
	w.readyWorkspace = ""
	if r.Workspace.ProviderInstallationID != account.InstallationID || r.Workspace.ProviderEpoch != account.Epoch {
		return w.discardWorkspace(r)
	}
	if r.Phase == "queued" {
		claim, err := w.client.workspaceClaim(ctx, r, "claim")
		if errors.Is(err, ErrRejected) {
			return w.discardWorkspace(r)
		}
		if err != nil {
			return err
		}
		r.Workspace, r.LeaseUntil, r.Deadline, r.Phase = claim.Workspace, claim.LeaseUntil, claim.Deadline, "preparing"
		if err = w.journal.saveWorkspace(r); err != nil {
			return err
		}
	}
	if r.Phase == "preparing" {
		claim, err := w.client.workspaceClaim(ctx, r, "heartbeat")
		if errors.Is(err, ErrRejected) {
			return w.reconcileWorkspace(ctx, r)
		}
		if err != nil {
			return err
		}
		r.LeaseUntil, r.Deadline = claim.LeaseUntil, claim.Deadline
		if err = w.journal.saveWorkspace(r); err != nil {
			return err
		}
		token, err := w.client.workspaceToken(ctx, r)
		if errors.Is(err, ErrRejected) {
			return w.reconcileWorkspace(ctx, r)
		}
		if err != nil {
			return err
		}
		receipt, prepareErr := w.prepareWorkspace(ctx, r, token)
		err = prepareErr
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrUnavailable) {
			return err
		}
		if errors.Is(err, ErrRejected) {
			return w.reconcileWorkspace(ctx, r)
		}
		report := camp.WorkspaceReport{State: "ready", BaseCommit: r.Workspace.BaseCommit, Branch: r.Workspace.Branch, TransferReceipt: receipt}
		if err != nil {
			code := "checkout_failed"
			if errors.Is(err, errCheckoutUnsafe) || errors.Is(err, transfer.ErrInput) || errors.Is(err, transfer.ErrDestination) {
				code = "checkout_unsafe"
			}
			if errors.Is(err, errCheckoutSize) || errors.Is(err, transfer.ErrLimit) {
				code = "checkout_too_large"
			}
			report = camp.WorkspaceReport{State: "failed", FailureCode: code}
			if cleanupErr := w.checkout.remove(r.Workspace); cleanupErr != nil {
				return cleanupErr
			}
		}
		r.Phase, r.Report = "reporting", &report
		if err = w.journal.saveWorkspace(r); err != nil {
			return err
		}
	}
	if r.Report.State == "ready" {
		if err = w.checkout.verify(ctx, r.Workspace); err != nil {
			return err
		}
	}
	_, err = w.client.workspaceReport(ctx, r)
	if errors.Is(err, ErrRejected) {
		return w.reconcileWorkspace(ctx, r)
	}
	if err != nil {
		return err
	}
	return w.journal.clearWorkspace()
}

// Keep every active ticket's checkout. Only an authenticated server response
// may retire an unused directory, and used work stays until a reviewed transfer
// or account erasure handles it. Process/network failures never mean deletion.
func (w *Worker) cleanupRetiredWorkspaces(ctx context.Context) error {
	entries, err := w.checkout.entries()
	if err != nil {
		return err
	}
	ids := []string{}
	for _, entry := range entries {
		id, ok := strings.CutPrefix(entry.Name(), "workspace-")
		if !ok {
			continue
		}
		if _, err = uuid.Parse(id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	for len(ids) > 0 {
		n := min(64, len(ids))
		batch := ids[:n]
		var retired []string
		if err = w.client.call(ctx, "POST", "/v1/runner/workspaces/retired", map[string]any{"ids": batch}, &retired); err != nil {
			return err
		}
		allowed := map[string]bool{}
		for _, id := range batch {
			allowed[id] = true
		}
		for _, id := range retired {
			if !allowed[id] {
				return ErrState
			}
			delete(allowed, id)
		}
		for _, id := range retired {
			if err = w.checkout.removeUnused(id); err != nil {
				return err
			}
		}
		ids = ids[n:]
	}
	return nil
}

func (w *Worker) reconcileWorkspace(ctx context.Context, r *workspaceRecord) error {
	current, err := w.client.workspaceStatus(ctx, r)
	if errors.Is(err, ErrRejected) {
		return w.discardWorkspace(r)
	}
	if err != nil {
		return err
	}
	switch current.State {
	case "failed", "cancelled":
		return w.discardWorkspace(r)
	case "ready":
		if r.Phase != "reporting" || r.Report.State != "ready" {
			return ErrState
		}
		if err = w.checkout.verify(ctx, r.Workspace); err != nil {
			return err
		}
		return w.journal.clearWorkspace()
	case "preparing":
		if !r.Deadline.After(time.Now()) || !r.LeaseUntil.After(time.Now()) {
			return w.discardWorkspace(r)
		}
		return nil // Issuance may still be in flight; retry the same claim.
	default:
		return ErrState
	}
}

func (w *Worker) prepareWorkspace(parent context.Context, r *workspaceRecord, token camp.RunnerWorkspaceToken) (*transfer.StageReceipt, error) {
	ctx, cancel := context.WithDeadline(parent, r.Deadline)
	defer cancel()
	type result struct {
		receipt *transfer.StageReceipt
		err     error
	}
	done := make(chan result, 1)
	workspace, claim := r.Workspace, r.ClaimID
	go func() {
		if workspace.TransferID != "" {
			receipt, err := w.prepareTransfer(ctx, workspace, claim, token)
			done <- result{receipt, err}
		} else {
			done <- result{err: w.checkout.prepare(ctx, workspace, token)}
		}
	}()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	interval := 10 * time.Second
	if w.interval < interval {
		interval = w.interval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	leaseTimer := time.NewTimer(time.Until(r.LeaseUntil))
	defer leaseTimer.Stop()
	for {
		select {
		case out := <-done:
			joined = true
			return out.receipt, out.err
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-leaseTimer.C:
			return nil, ErrRejected
		case <-tick.C:
			heartbeatCtx, stop := context.WithDeadline(ctx, r.LeaseUntil)
			claim, err := w.client.workspaceClaim(heartbeatCtx, r, "heartbeat")
			stop()
			if errors.Is(err, ErrUnavailable) {
				continue
			}
			if err != nil {
				return nil, err
			}
			r.LeaseUntil = claim.LeaseUntil
			if !leaseTimer.Stop() {
				select {
				case <-leaseTimer.C:
				default:
				}
			}
			leaseTimer.Reset(time.Until(claim.LeaseUntil))
		}
	}
}
