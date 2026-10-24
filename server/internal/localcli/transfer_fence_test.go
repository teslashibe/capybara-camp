package localcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func resolveFixtureFence(a *App, in transferHoldInput, id string) error {
	raw, _ := json.Marshal(map[string]any{"session": in.Session, "handoffID": id})
	_, err := a.Run(context.Background(), "transfer-resolve", bytes.NewReader(raw))
	return err
}

func TestTransferFenceSurvivesHoldExitAndRequiresMatchingResolution(t *testing.T) {
	for _, expire := range []bool{false, true} {
		name := "closed"
		if expire {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			a, in := holdFixture(t)
			in.HandoffID = uuid.NewString()
			if expire {
				in.ExpiresAt = time.Now().Add(2 * time.Second).Unix()
			}
			input, events, done := startSourceHold(t, a, in)
			nextSourceEvent(t, events)
			if err := json.NewEncoder(input).Encode(map[string]string{"type": "prepareCommit"}); err != nil {
				t.Fatal(err)
			}
			nextSourceEvent(t, events)
			if !expire {
				_ = input.Close()
			}
			select {
			case err := <-done:
				if !expire && err != nil {
					t.Fatal(err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("Source helper did not exit")
			}
			restarted := New(a.Config)
			launches := 0
			restarted.commandPath = func(string, []string) (string, error) { launches++; return "", errors.New("unexpected launch") }
			_, emit := turnOutput()
			err := restarted.runConversation(t.Context(), conversationRequest{Session: in.Session, Prompt: "Must remain blocked", Mode: "chat"}, nil, emit)
			if err == nil || !strings.Contains(err.Error(), "held for a cloud transfer") || launches != 0 {
				t.Fatal("Restarted source bypassed fence", err, launches)
			}
			if err = resolveFixtureFence(restarted, in, uuid.NewString()); err == nil {
				t.Fatal("Another handoff cleared the fence")
			}
			// Reopening the same handoff is allowed, without releasing the fence.
			in.ExpiresAt = time.Now().Add(time.Minute).Unix()
			again, againEvents, againDone := startSourceHold(t, restarted, in)
			nextSourceEvent(t, againEvents)
			_ = again.Close()
			if err = <-againDone; err != nil {
				t.Fatal(err)
			}
			if err = resolveFixtureFence(restarted, in, in.HandoffID); err != nil {
				t.Fatal(err)
			}
			if err = resolveFixtureFence(restarted, in, in.HandoffID); err != nil {
				t.Fatal("Resolution did not replay", err)
			}
			locks, err := restarted.sessionLocks()
			if err != nil {
				t.Fatal(err)
			}
			defer locks.close()
			root, _ := filepath.EvalSymlinks(in.Session.Root)
			if err = locks.acquire(in.Session.Provider + ":" + root + ":" + in.Session.SessionID); err != nil {
				t.Fatal("Confirmed cancellation did not restore local execution", err)
			}
		})
	}
}

func TestTransferFenceRejectsMalformedOrLinkedState(t *testing.T) {
	a, in := holdFixture(t)
	root, _ := filepath.EvalSymlinks(in.Session.Root)
	key := in.Session.Provider + ":" + root + ":" + in.Session.SessionID
	locks, err := a.sessionLocks()
	if err != nil {
		t.Fatal(err)
	}
	if err = locks.acquire(key); err != nil {
		t.Fatal(err)
	}
	path := locks.files[key].Name()
	locks.close()
	if err = os.WriteFile(path, []byte("incomplete transfer marker"), 0600); err != nil {
		t.Fatal(err)
	}
	check, err := a.sessionLocks()
	if err != nil {
		t.Fatal(err)
	}
	defer check.close()
	if err = check.acquire(key); err == nil {
		t.Fatal("Malformed marker was treated as unlocked")
	}
	if err = resolveFixtureFence(a, in, uuid.NewString()); err == nil {
		t.Fatal("Malformed marker was discarded")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err = os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err = check.acquire(key); err == nil {
		t.Fatal("Symlink accepted for source lock")
	}
}

func TestTransferFenceInventoryAndOpaqueResolutionAfterProfileMove(t *testing.T) {
	a, in := holdFixture(t)
	in.HandoffID = uuid.NewString()
	input, events, done := startSourceHold(t, a, in)
	nextSourceEvent(t, events)
	if err := json.NewEncoder(input).Encode(map[string]string{"type": "prepareCommit"}); err != nil {
		t.Fatal(err)
	}
	nextSourceEvent(t, events)
	active, err := a.transferFences(t.Context())
	if err != nil || len(active) != 0 {
		t.Fatal("Active source exposed as orphan", err)
	}
	_ = input.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	fences, err := a.transferFences(t.Context())
	if err != nil || len(fences) != 1 || fences[0].HandoffID != in.HandoffID {
		t.Fatal("Lost transfer not discovered", err)
	}
	raw, _ := json.Marshal(fences)
	if strings.Contains(string(raw), in.Session.Root) || strings.Contains(string(raw), in.Session.SessionID) {
		t.Fatal("Inventory exposed source paths or sessions")
	}
	if err = os.Rename(in.Session.Root, in.Session.Root+"-moved"); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"key": fences[0].Key, "handoffID": in.HandoffID})
	if _, err = a.Run(t.Context(), "transfer-resolve", bytes.NewReader(body)); err != nil {
		t.Fatal("Opaque resolution needed the missing source profile", err)
	}
	if _, err = a.Run(t.Context(), "transfer-resolve", bytes.NewReader(body)); err != nil {
		t.Fatal("Opaque resolution did not replay", err)
	}
	fences, err = a.transferFences(t.Context())
	if err != nil || len(fences) != 0 {
		t.Fatal("Resolved marker remained in inventory", err)
	}
}
