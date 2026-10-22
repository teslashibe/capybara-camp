package localcli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	"github.com/google/uuid"
)

// The native coordinator calls this only after reading a terminal uncommitted
// handoff and the unchanged local assignment from the authenticated Camp API.
// Completed transfers keep their marker so the old source cannot restart.
func (a *App) resolveTransferFence(ctx context.Context, input io.Reader) (any, error) {
	data, err := io.ReadAll(io.LimitReader(input, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("Invalid transfer resolution")
	}
	var in struct {
		Session   Session `json:"session"`
		HandoffID string  `json:"handoffID"`
		Key       string  `json:"key"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("Invalid transfer resolution")
	}
	if in.Key != "" {
		if digest, err := hex.DecodeString(in.Key); err != nil || len(digest) != 32 || hex.EncodeToString(digest) != in.Key {
			return nil, errors.New("Invalid transfer key")
		}
		if id, err := uuid.Parse(in.HandoffID); err != nil || id == uuid.Nil || id.String() != in.HandoffID {
			return nil, errors.New("Invalid handoff identity")
		}
		if in.Session.Provider != "" {
			return nil, errors.New("Choose one transfer identity")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		locks, err := a.sessionLocks()
		if err != nil {
			return nil, err
		}
		defer locks.close()
		key := "transfer-key:" + in.Key
		if err = locks.acquireFile(key, in.Key, in.HandoffID); err != nil {
			return nil, err
		}
		if err = locks.releaseHeldFence(key, in.HandoffID); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	}
	s := in.Session
	if (s.Provider != "Codex" && s.Provider != "Claude") || !validSessionID(s.SessionID) || s.IsSubagent || !filepath.IsAbs(s.Root) {
		return nil, errors.New("Choose the original source session")
	}
	s.Root, err = canonical(s.Root, a.Config.Home)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	locks, err := a.sessionLocks()
	if err != nil {
		return nil, err
	}
	defer locks.close()
	if err = locks.releaseFence(s.Provider+":"+s.Root+":"+s.SessionID, in.HandoffID); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}
