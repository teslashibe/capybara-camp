package localcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConversationOwnershipChecksWritersAndMetadata(t *testing.T) {
	for _, kind := range []string{"same-path", "same-id", "other-id", "corrupt", "inspection-failed"} {
		a := fixture(t)
		requireApprovalOK(t, os.MkdirAll(a.Config.Home, 0700))
		root, err := filepath.EvalSymlinks(a.Config.Home)
		requireApprovalOK(t, err)
		path := filepath.Join(root, "sessions", "active.jsonl")
		id := "target"
		if kind == "other-id" {
			id = "other"
		}
		write(t, path, fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":%q}}\n", id))
		if kind == "corrupt" {
			write(t, path, "unfinished\n")
		}
		info, _ := os.Stat(path)
		stat := info.Sys().(*syscall.Stat_t)
		a.systemCapture = func(_ context.Context, command string, _ []string, _ time.Duration, _ int) (int, []byte, error) {
			if kind == "inspection-failed" {
				return 1, nil, errors.New("failed")
			}
			if command == "/bin/ps" {
				return 0, []byte("123 /fixture/codex\n"), nil
			}
			return 0, []byte(fmt.Sprintf("p123\x00\nf3\x00aw\x00tREG\x00D%x\x00i%d\x00n%s\x00", stat.Dev, stat.Ino, path)), nil
		}
		session := Session{Provider: "Codex", Root: root, SessionID: "target"}
		if kind == "same-path" {
			session.Path = path
		}
		err = a.assertCodexAvailable(context.Background(), session)
		if kind == "other-id" && err != nil || kind != "other-id" && err == nil {
			t.Fatal(kind, err)
		}
	}
}

func TestConversationOwnershipRejectsLoadedSessionsWithoutResuming(t *testing.T) {
	for _, id := range []string{"parent", "other"} {
		server := newObserverServer(t)
		if err := os.MkdirAll(filepath.Join(server.root, "sessions"), 0700); err != nil {
			t.Fatal(err)
		}
		a := fixture(t)
		a.systemCapture = func(context.Context, string, []string, time.Duration, int) (int, []byte, error) {
			return 0, nil, nil
		}
		err := a.assertCodexAvailable(context.Background(), Session{Provider: "Codex", Root: server.root, SessionID: id})
		if id == "parent" && (err == nil || !strings.Contains(err.Error(), "open in Codex")) || id == "other" && err != nil {
			t.Fatal(id, err)
		}
		<-server.done
		server.lock.Lock()
		methods := append([]string{}, server.methods...)
		server.lock.Unlock()
		for _, method := range methods {
			if method != "initialize" && method != "initialized" && method != "thread/loaded/list" {
				t.Fatal("Ownership probe did unnecessary provider work", methods)
			}
		}
	}
}

func TestCodexSteeringDistinguishesRejectedAndUncertain(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		f := &turnFixture{events: []map[string]any{codexCompleted()}}
		f.rpc = func(method string, params map[string]any) (map[string]any, error) {
			if method == "config/read" {
				return map[string]any{"config": map[string]any{}}, nil
			}
			if method == "turn/start" {
				return map[string]any{"turn": map[string]any{"id": "active"}}, nil
			}
			if method == "turn/steer" {
				if uncertain {
					return nil, context.DeadlineExceeded
				}
				return nil, &nativeRejection{message: "Turn already ended"}
			}
			return map[string]any{}, nil
		}
		controls := make(chan map[string]any, 1)
		events, record := turnOutput()
		emit := func(event map[string]any) error {
			if event["type"] == "started" {
				controls <- map[string]any{"type": "steer", "id": "one", "text": "New direction"}
			}
			return record(event)
		}
		err := codexTurn(f, conversationRequest{Session: Session{SessionID: "original"}}, controls, emit)
		if uncertain && (err == nil || !strings.Contains(err.Error(), "uncertain")) || !uncertain && err != nil {
			t.Fatal(uncertain, err)
		}
		data, _ := json.Marshal(*events)
		if strings.Contains(string(data), "steerAccepted") || !strings.Contains(string(data), "steerRejected") {
			t.Fatal(string(data))
		}
	}
}

func TestCodexOwnershipPaginationHasFiniteBounds(t *testing.T) {
	for _, longCursor := range []bool{false, true} {
		client, server := net.Pipe()
		daemon := &codexDaemon{conn: client}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for page := 0; ; page++ {
				_, data, err := clientFrame(server)
				if err != nil {
					return
				}
				var request map[string]any
				_ = json.Unmarshal(data, &request)
				cursor := fmt.Sprint(page)
				if longCursor {
					cursor = strings.Repeat("x", 4097)
				}
				response, _ := json.Marshal(map[string]any{"id": request["id"], "result": map[string]any{"data": []string{}, "nextCursor": cursor}})
				if _, err := server.Write(serverFrame(1, true, response)); err != nil {
					return
				}
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := daemon.loadedIDs(ctx)
		cancel()
		client.Close()
		server.Close()
		<-done
		if err == nil || !strings.Contains(err.Error(), "cursor") && !strings.Contains(err.Error(), "pages") {
			t.Fatal("Unbounded ownership pagination", err)
		}
	}
}
