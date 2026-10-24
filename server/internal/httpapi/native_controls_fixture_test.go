package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

// Test-only native UI driver. It invokes no provider or project tools.
type nativeControlTurn struct {
	workspace camp.Workspace
	request   camp.Request
	claim     string
	seq       int64
	started   time.Time
	seen      map[string]bool
}

func advanceNativeControls(ctx context.Context, git *camp.GitStore, grant camp.RunnerGrant, turn *nativeControlTurn) *nativeControlTurn {
	if turn == nil {
		w, err := git.NextRunnerWorkspace(ctx, grant.Token)
		if err != nil || w == nil || w.State != "ready" {
			return nil
		}
		claim := uuid.NewString()
		r, err := git.ClaimRunnerRequest(ctx, grant.Token, w.ID, claim)
		if err != nil || r == nil {
			return nil
		}
		started, err := git.ActOnRunnerRequest(ctx, grant.Token, w.ID, r.ID, uuid.NewString(), camp.RequestAction{Action: "start", LeaseToken: claim})
		if err != nil {
			return nil
		}
		turn = &nativeControlTurn{workspace: *w, request: started, claim: claim, started: time.Now(), seen: map[string]bool{}}
		turn.emit(ctx, git, grant, map[string]any{"type": "settings", "model": "fixture-model", "effort": "high", "speed": "standard"})
		turn.emit(ctx, git, grant, map[string]any{"type": "delta", "text": "Synthetic cloud reply is streaming. You can steer, queue a follow-up, or stop this turn."})
		if r.Kind != "planning" && strings.Contains(strings.ToLower(r.Prompt), "approval") {
			turn.emit(ctx, git, grant, map[string]any{"type": "approval", "id": uuid.NewString(), "kind": "command", "title": "Allow the synthetic action", "detail": "This fixture executes no command", "questions": []any{}, "canApprove": true, "acceptLabel": "Allow"})
		}
		return turn
	}
	r, err := git.ActOnRunnerRequest(ctx, grant.Token, turn.workspace.ID, turn.request.ID, uuid.NewString(), camp.RequestAction{Action: "heartbeat", LeaseToken: turn.claim})
	if err != nil {
		return nil
	}
	if r.State == "cancel_requested" {
		_, _ = git.ActOnRunnerRequest(ctx, grant.Token, turn.workspace.ID, r.ID, uuid.NewString(), camp.RequestAction{Action: "finish", LeaseToken: turn.claim, Outcome: "cancelled", Evidence: "Synthetic UI driver stopped"})
		return nil
	}
	controls, err := git.RunnerControls(ctx, grant.Token, turn.workspace.ID, r.ID)
	if err != nil {
		return turn
	}
	for _, control := range controls {
		if turn.seen[control.ID] {
			continue
		}
		turn.seen[control.ID] = true
		id, _ := control.Control["id"].(string)
		if control.Control["type"] == "steer" {
			text, _ := control.Control["text"].(string)
			if strings.HasPrefix(strings.ToLower(text), "reject") {
				turn.emit(ctx, git, grant, map[string]any{"type": "steerRejected", "requestID": id, "message": "Synthetic rejection"})
				continue
			}
			turn.emit(ctx, git, grant, map[string]any{"type": "boundary"})
			turn.emit(ctx, git, grant, map[string]any{"type": "steerAccepted", "requestID": id})
			runes := []rune(text)
			turn.emit(ctx, git, grant, map[string]any{"type": "delta", "text": "Following the updated direction: " + string(runes[:min(200, len(runes))])})
		} else {
			turn.emit(ctx, git, grant, map[string]any{"type": "approvalResolved", "requestID": id})
			turn.emit(ctx, git, grant, map[string]any{"type": "delta", "text": " The synthetic approval was received."})
		}
	}
	if strings.Contains(strings.ToLower(r.Prompt), "finish") && time.Since(turn.started) > 2*time.Second {
		turn.emit(ctx, git, grant, map[string]any{"type": "done"})
		result := camp.RequestAction{Action: "finish", LeaseToken: turn.claim, Outcome: "completed", Evidence: "Synthetic UI driver completed"}
		if r.Kind == "planning" {
			result.Output = "Synthetic planning result with scope, implementation steps and an unresolved choice to review"
		}
		_, _ = git.ActOnRunnerRequest(ctx, grant.Token, turn.workspace.ID, r.ID, uuid.NewString(), result)
		return nil
	}
	return turn
}

func (t *nativeControlTurn) emit(ctx context.Context, git *camp.GitStore, grant camp.RunnerGrant, value map[string]any) {
	raw, _ := json.Marshal(value)
	if _, err := git.AppendRunnerEvent(ctx, grant.Token, t.workspace.ID, t.request.ID, camp.StreamEvent{Seq: t.seq + 1, Event: raw}); err == nil {
		t.seq++
	}
}
