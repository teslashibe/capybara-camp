package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func payloadHash(data []byte) string            { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func transferDirectory(w camp.Workspace) string { return "transfer-" + w.ID }
func transferMarker(w camp.Workspace) string    { return "transfer-workspace-" + w.ID + ".json" }

func (c *checkout) transferHeld(w camp.Workspace) (bool, error) {
	if _, err := c.root.Lstat(transferMarker(w)); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, ErrState
	}
	var saved camp.Workspace
	if c.readJSON(transferMarker(w), &saved) != nil || !sameWorkspace(saved, w) {
		return false, ErrState
	}
	return true, nil
}

func (c *checkout) transferState(w camp.Workspace) (string, error) {
	name := transferDirectory(w)
	if err := c.root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", ErrState
	}
	info, err := c.root.Lstat(name)
	if err != nil || !info.IsDir() || !ownedPrivate(info) {
		return "", ErrState
	}
	return filepath.Join(c.dir, name), nil
}

func (c *checkout) loadTransferPayload(w camp.Workspace) ([]byte, error) {
	info, err := c.root.Lstat(transferDirectory(w))
	if err != nil || !info.IsDir() || !ownedPrivate(info) {
		return nil, ErrState
	}
	directory, err := c.root.OpenRoot(transferDirectory(w))
	if err != nil {
		return nil, ErrState
	}
	defer directory.Close()
	f, err := directory.OpenFile("manifest.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrState
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedPrivate(info) || info.Size() > transfer.MaxWireBytes || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return nil, ErrState
	}
	raw, err := io.ReadAll(io.LimitReader(f, transfer.MaxWireBytes+1))
	if err != nil || len(raw) > transfer.MaxWireBytes {
		return nil, ErrState
	}
	return raw, nil
}

func (c *checkout) saveTransferPayload(w camp.Workspace, raw []byte) error {
	directory, err := c.root.OpenRoot(transferDirectory(w))
	if err != nil {
		return ErrState
	}
	defer directory.Close()
	name := "stage-temp-" + uuid.NewString()
	f, err := directory.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrState
	}
	defer directory.Remove(name)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil || closed != nil || directory.Rename(name, "manifest.json") != nil {
		return ErrState
	}
	dir, err := directory.Open(".")
	if err != nil {
		return ErrState
	}
	defer dir.Close()
	return dir.Sync()
}

func (w *Worker) downloadTransfer(ctx context.Context, workspace camp.Workspace, claim string) (camp.Transfer, []byte, error) {
	var metadata camp.Transfer
	base := "/v1/runner/workspaces/" + workspace.ID + "/transfer"
	input := map[string]string{"claim_id": claim}
	if err := w.client.call(ctx, "POST", base, input, &metadata); err != nil {
		return metadata, nil, err
	}
	if metadata.ID != workspace.TransferID || metadata.CampID != workspace.CampID || metadata.SessionID != workspace.SessionID || metadata.OwnerUserID != workspace.OwnerUserID || metadata.TargetAccountID != workspace.ProviderAccountID || metadata.TargetAccountEpoch != workspace.ProviderEpoch || metadata.TargetLocation != "cloud" || metadata.State != "ready" || metadata.BaseCommit != workspace.BaseCommit || metadata.SourceRevision != workspace.SourceRevision || metadata.PayloadBytes < 2 || metadata.PayloadBytes > transfer.MaxWireBytes || metadata.UploadedBytes != metadata.PayloadBytes || metadata.ChunkBytes != camp.TransferChunkBytes {
		return metadata, nil, ErrState
	}
	if _, err := w.checkout.root.Lstat(filepath.Join(transferDirectory(workspace), "manifest.json")); err == nil {
		raw, err := w.checkout.loadTransferPayload(workspace)
		if err != nil || len(raw) != metadata.PayloadBytes || payloadHash(raw) != metadata.PayloadSHA256 {
			return metadata, nil, ErrState
		}
		return metadata, raw, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return metadata, nil, ErrState
	}
	raw := make([]byte, 0, metadata.PayloadBytes)
	for index := 0; len(raw) < metadata.PayloadBytes; index++ {
		var part camp.TransferChunk
		if err := w.client.call(ctx, "POST", base+"/chunks/"+strconv.Itoa(index), input, &part); err != nil {
			return metadata, nil, err
		}
		expected := min(camp.TransferChunkBytes, metadata.PayloadBytes-len(raw))
		if len(part.Data) != expected || payloadHash(part.Data) != part.SHA256 {
			return metadata, nil, ErrState
		}
		raw = append(raw, part.Data...)
	}
	if payloadHash(raw) != metadata.PayloadSHA256 {
		return metadata, nil, ErrState
	}
	return metadata, raw, nil
}

func (w *Worker) prepareTransfer(ctx context.Context, workspace camp.Workspace, claim string, token camp.RunnerWorkspaceToken) (*transfer.StageReceipt, error) {
	metadata, raw, err := w.downloadTransfer(ctx, workspace, claim)
	if err != nil {
		return nil, err
	}
	manifest, err := transfer.Decode(raw)
	if err != nil {
		return nil, err
	}
	review, err := manifest.Digest()
	if err != nil || review != metadata.ReviewSHA256 || manifest.BaseCommit != workspace.BaseCommit || len(manifest.Files) != metadata.FileCount {
		return nil, ErrState
	}
	held, err := w.checkout.transferHeld(workspace)
	if err != nil {
		return nil, err
	}
	if !held {
		if err = w.checkout.prepare(ctx, workspace, token); err != nil {
			return nil, err
		}
		if err = w.checkout.saveJSON(transferMarker(workspace), workspace); err != nil {
			return nil, err
		}
	}
	state, err := w.checkout.transferState(workspace)
	if err != nil {
		return nil, err
	}
	if err = w.checkout.saveTransferPayload(workspace, raw); err != nil {
		return nil, err
	}
	stage, err := transfer.OpenStager(w.checkout.workdir(workspace), state)
	if err != nil {
		return nil, err
	}
	defer stage.Close()
	receipt, err := stage.Apply(ctx, manifest, review, workspace.Branch)
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (c *checkout) verifyTransfer(ctx context.Context, w camp.Workspace) error {
	held, err := c.transferHeld(w)
	if err != nil || !held {
		return ErrState
	}
	state := filepath.Join(c.dir, transferDirectory(w))
	if info, err := os.Lstat(filepath.Join(state, "stage.json")); err != nil || !info.Mode().IsRegular() || !ownedPrivate(info) {
		return ErrState
	}
	raw, err := c.loadTransferPayload(w)
	if err != nil {
		return err
	}
	manifest, err := transfer.Decode(raw)
	if err != nil || manifest.BaseCommit != w.BaseCommit {
		return ErrState
	}
	review, err := manifest.Digest()
	if err != nil {
		return ErrState
	}
	stage, err := transfer.OpenStager(c.workdir(w), state)
	if err != nil {
		return err
	}
	defer stage.Close()
	_, err = stage.Apply(ctx, manifest, review, w.Branch)
	return err
}
