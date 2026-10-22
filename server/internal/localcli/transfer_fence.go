package localcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
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
	}
	if json.Unmarshal(data, &in) != nil {
		return nil, errors.New("Invalid transfer resolution")
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
