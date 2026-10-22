package localcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

type transferEvents chan map[string]string

func (w transferEvents) Write(data []byte) (int, error) {
	var value map[string]string
	if err := json.Unmarshal(data, &value); err != nil {
		return 0, err
	}
	w <- value
	return len(data), nil
}

func holdFixture(t *testing.T) (*App, transferHoldInput) {
	t.Helper()
	a := fixture(t)
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Fixture"}, {"config", "user.email", "fixture@example.com"}} {
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	write(t, filepath.Join(dir, "file.txt"), "before\n")
	for _, args := range [][]string{{"add", "file.txt"}, {"-c", "core.hooksPath=/dev/null", "commit", "-qm", "Fixture"}} {
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	write(t, filepath.Join(dir, "file.txt"), "after\n")
	manifest, err := transfer.Capture(t.Context(), dir, []string{"file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(manifest)
	root := filepath.Join(a.Config.Home, "profile")
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	return a, transferHoldInput{Session: Session{Provider: "Codex", Root: root, CWD: dir, SessionID: "native"}, Payload: raw, ExpiresAt: time.Now().Add(time.Minute).Unix()}
}

func startSourceHold(t *testing.T, a *App, in transferHoldInput) (*io.PipeWriter, transferEvents, <-chan error) {
	t.Helper()
	read, write := io.Pipe()
	events, done := make(transferEvents, 4), make(chan error, 1)
	go func() { done <- a.HoldTransferSource(t.Context(), read, events) }()
	if err := json.NewEncoder(write).Encode(in); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = write.Close() })
	return write, events, done
}

func nextSourceEvent(t *testing.T, events transferEvents) map[string]string {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("Source did not verify")
		return nil
	}
}

func TestTransferHoldSharesConversationLockAndReleasesOnEOF(t *testing.T) {
	a, in := holdFixture(t)
	input, events, done := startSourceHold(t, a, in)
	first := nextSourceEvent(t, events)
	if first["type"] != "verified" || len(first["payloadSHA256"]) != 64 || len(first["reviewSHA256"]) != 64 {
		t.Fatal(first)
	}
	launches := 0
	a.commandPath = func(string, []string) (string, error) { launches++; return "", errors.New("unexpected provider") }
	_, emit := turnOutput()
	err := a.runConversation(t.Context(), conversationRequest{Session: in.Session, Prompt: "Must wait", Mode: "chat"}, nil, emit)
	if err == nil || !strings.Contains(err.Error(), "already running") || launches != 0 {
		t.Fatal("Transfer allowed a provider to start", err, launches)
	}
	if err = json.NewEncoder(input).Encode(map[string]string{"type": "verify"}); err != nil {
		t.Fatal(err)
	}
	if next := nextSourceEvent(t, events); next["payloadSHA256"] != first["payloadSHA256"] {
		t.Fatal("Review changed", next)
	}
	_ = input.Close()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("EOF leaked source hold")
	}
	locks, err := a.sessionLocks()
	if err != nil {
		t.Fatal(err)
	}
	defer locks.close()
	root, _ := filepath.EvalSymlinks(in.Session.Root)
	if err = locks.acquire(in.Session.Provider + ":" + root + ":" + in.Session.SessionID); err != nil {
		t.Fatal("Source lock leaked", err)
	}
}

func TestTransferHoldRejectsChangesAndExpiredOrActiveSources(t *testing.T) {
	t.Run("changed while held", func(t *testing.T) {
		a, in := holdFixture(t)
		input, events, done := startSourceHold(t, a, in)
		nextSourceEvent(t, events)
		write(t, filepath.Join(in.Session.CWD, "file.txt"), "changed after review\n")
		if err := json.NewEncoder(input).Encode(map[string]string{"type": "verify"}); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, transfer.ErrChanged) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("Changed source remained held")
		}
	})
	for _, scenario := range []string{"expired", "subagent", "busy", "invalid payload"} {
		t.Run(scenario, func(t *testing.T) {
			a, in := holdFixture(t)
			if scenario == "expired" {
				in.ExpiresAt = time.Now().Add(-time.Second).Unix()
			}
			if scenario == "subagent" {
				in.Session.IsSubagent = true
			}
			if scenario == "invalid payload" {
				in.Payload = []byte("{}")
			}
			if scenario == "busy" {
				locks, err := a.sessionLocks()
				if err != nil {
					t.Fatal(err)
				}
				defer locks.close()
				root, _ := filepath.EvalSymlinks(in.Session.Root)
				if err = locks.acquire(in.Session.Provider + ":" + root + ":" + in.Session.SessionID); err != nil {
					t.Fatal(err)
				}
			}
			_, events, done := startSourceHold(t, a, in)
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("Invalid source accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("Invalid source did not return")
			}
			if len(events) != 0 {
				t.Fatal("Invalid source produced a verification")
			}
		})
	}
}

func TestTransferHoldDeadlineAndCancellationReleaseReader(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		a, in := holdFixture(t)
		in.ExpiresAt = time.Now().Add(2 * time.Second).Unix()
		ctx, cancel := context.WithCancel(t.Context())
		read, input := io.Pipe()
		events, done := make(transferEvents, 2), make(chan error, 1)
		go func() { done <- a.HoldTransferSource(ctx, read, events) }()
		if err := json.NewEncoder(input).Encode(in); err != nil {
			t.Fatal(err)
		}
		nextSourceEvent(t, events)
		if cancelEarly {
			cancel()
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("Missing termination")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Hold leaked after expiry or cancellation")
		}
		cancel()
		_ = input.Close()
	}
}

func TestTransferHoldRefusesExternalCodexOwner(t *testing.T) {
	a, in := holdFixture(t)
	server := newObserverServer(t)
	if err := os.MkdirAll(filepath.Join(server.root, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	in.Session.Root, in.Session.SessionID = server.root, "parent"
	a.systemCapture = func(context.Context, string, []string, time.Duration, int) (int, []byte, error) { return 0, nil, nil }
	_, events, done := startSourceHold(t, a, in)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "open in Codex") {
			t.Fatal("External session was claimed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ownership check did not finish")
	}
	if len(events) != 0 {
		t.Fatal("External owner received verified receipt")
	}
}

func TestTransferHoldAcceptsSessionWorkingInSubdirectory(t *testing.T) {
	a, in := holdFixture(t)
	sub := filepath.Join(in.Session.CWD, "subdirectory")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	in.Session.CWD = sub
	input, events, done := startSourceHold(t, a, in)
	nextSourceEvent(t, events)
	_ = input.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Source did not release")
	}
}
