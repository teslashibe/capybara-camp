package localcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

type TransferStageInput struct {
	Scope         string `json:"scope"`
	TransferID    string `json:"transferID"`
	Destination   string `json:"destination"`
	Branch        string `json:"branch"`
	Payload       []byte `json:"payload"`
	PayloadSHA256 string `json:"payloadSHA256"`
	ReviewSHA256  string `json:"reviewSHA256"`
}

type TransferStageResult struct {
	Scope         string                `json:"scope"`
	TransferID    string                `json:"transferID"`
	PayloadSHA256 string                `json:"payloadSHA256"`
	Receipt       transfer.StageReceipt `json:"receipt"`
}

// Stage only into an explicitly chosen, private checkout at the reviewed base.
// The caller prepares that checkout and keeps execution stopped. This receipt
// proves file staging; it cannot release the cloud source or authorize a local
// provider. Those actions require the authenticated handoff transaction.
func (a *App) stageTransfer(ctx context.Context, input io.Reader) (any, error) {
	const maximum = (transfer.MaxWireBytes+2)/3*4 + 16384
	raw, err := io.ReadAll(io.LimitReader(input, maximum+1))
	if err != nil || len(raw) > maximum {
		return nil, transfer.ErrLimit
	}
	var in TransferStageInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, transfer.ErrInput
	}
	manifest, err := validateStageInput(in)
	if err != nil {
		return nil, err
	}
	stager, _, err := a.openTransferStager(ctx, in)
	if err != nil {
		return nil, err
	}
	defer stager.Close()
	receipt, err := stager.Apply(ctx, manifest, in.ReviewSHA256, in.Branch)
	if err != nil {
		return nil, err
	}
	return TransferStageResult{Scope: in.Scope, TransferID: in.TransferID, PayloadSHA256: in.PayloadSHA256, Receipt: receipt}, nil
}

func validateStageInput(in TransferStageInput) (transfer.Manifest, error) {
	id, err := uuid.Parse(in.TransferID)
	scope, scopeErr := hex.DecodeString(in.Scope)
	if err != nil || id == uuid.Nil || id.String() != in.TransferID || scopeErr != nil || len(scope) != 32 || hex.EncodeToString(scope) != in.Scope ||
		!filepath.IsAbs(in.Destination) || filepath.Clean(in.Destination) != in.Destination || len(in.Destination) > 4096 ||
		len(in.Branch) > 512 || strings.ContainsFunc(in.Branch, unicode.IsControl) || len(in.Payload) > transfer.MaxWireBytes {
		return transfer.Manifest{}, transfer.ErrInput
	}
	sum := sha256.Sum256(in.Payload)
	if hex.EncodeToString(sum[:]) != in.PayloadSHA256 {
		return transfer.Manifest{}, transfer.ErrInput
	}
	manifest, err := transfer.Decode(in.Payload)
	if err != nil {
		return transfer.Manifest{}, err
	}
	review, err := manifest.Digest()
	if err != nil || review != in.ReviewSHA256 {
		return transfer.Manifest{}, transfer.ErrInput
	}
	return manifest, nil
}

func (a *App) openTransferStager(ctx context.Context, in TransferStageInput) (*transfer.Stager, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if !filepath.IsAbs(a.Config.Support) {
		return nil, "", transfer.ErrDestination
	}
	if err := os.MkdirAll(a.Config.Support, 0700); err != nil {
		return nil, "", err
	}
	info, err := os.Lstat(a.Config.Support)
	if err != nil || !info.IsDir() || !ownedByUser(info) || info.Mode().Perm()&0077 != 0 {
		return nil, "", transfer.ErrDestination
	}
	root, err := os.OpenRoot(a.Config.Support)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	name := "restore-" + in.Scope + "-" + in.TransferID
	if err = root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, "", err
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, "", err
	}
	err = errors.Join(directory.Sync(), directory.Close())
	if err != nil {
		return nil, "", err
	}
	stager, err := transfer.OpenStager(in.Destination, filepath.Join(a.Config.Support, name))
	if err != nil {
		return nil, "", err
	}
	return stager, filepath.Join(a.Config.Support, name), nil
}
