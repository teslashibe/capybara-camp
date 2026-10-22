package camp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

// Planning output belongs to the saved plan, even when its anchor is shared.
func (s *Store) PlanningStream(ctx context.Context, a auth.Actor, campID, planID string, version int64, ordinal int) (*StreamHead, error) {
	if version < 1 || ordinal < 0 || ordinal > 4 {
		return nil, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, false); err != nil {
		return nil, err
	}
	p, err := s.loadPlanning(ctx, tx, a, campID, planID, version)
	if err != nil {
		return nil, err
	}
	if err = s.checkPlanningSources(ctx, tx, a, p); err != nil {
		return nil, err
	}
	for _, step := range p.Version.Steps {
		if step.Ordinal != ordinal || step.RequestID == "" {
			continue
		}
		r, err := loadRequest(ctx, tx, campID, step.RequestID)
		if err != nil {
			return nil, err
		}
		head := &StreamHead{ID: r.ID, State: r.State, Seq: r.StreamSeq, Model: r.Model, Effort: r.Effort, Speed: r.Speed}
		if err = readStreamSettings(ctx, tx, head); err != nil {
			return nil, err
		}
		return head, nil
	}
	return nil, nil
}

type LocalPlanningEvent struct {
	StreamEvent
	LeaseToken string `json:"lease_token"`
}

// A device may publish only the isolated planning request it already owns.
// This does not grant a Mac the cloud runner's command or approval transport.
func (s *Store) AppendLocalPlanningEvent(ctx context.Context, a auth.Actor, campID, id string, input LocalPlanningEvent) (int64, error) {
	event, raw, err := validStreamEvent(input.Event)
	if err != nil || input.Seq < 1 || !validID(input.LeaseToken) || !oneOf(strEvent(event, "type"), "text", "settings") {
		return 0, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.lock(ctx, tx, a, campID, true); err != nil {
		return 0, err
	}
	r, err := loadRequest(ctx, tx, campID, id)
	if err != nil {
		return 0, err
	}
	if r.Kind != "planning" || r.ExecutionLocation != "local" || r.OwnerUserID != a.UserID || r.OwnerDeviceID != a.DeviceID || subtle.ConstantTimeCompare([]byte(r.LeaseToken), []byte(input.LeaseToken)) != 1 {
		return 0, ErrDenied
	}
	if strEvent(event, "type") == "text" && len(strEvent(event, "text")) > 8192 {
		return 0, ErrInput
	}
	if strEvent(event, "type") == "settings" {
		for _, key := range []string{"model", "effort", "speed"} {
			if len(strEvent(event, key)) > 200 {
				return 0, ErrInput
			}
		}
	}
	// Keep the accepted event canonical even if callers sent whitespace or key reordering.
	input.Event = json.RawMessage(raw)
	seq, err := s.appendRequestEvent(ctx, tx, r, input.StreamEvent, event, raw, "")
	if err != nil {
		return 0, err
	}
	return seq, tx.Commit(ctx)
}
