package localcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeRepeatedConnectionsReleaseReadersAndDescriptors(t *testing.T) {
	a := providerFixture(t, "rpc")
	s := Session{Provider: "Codex", Root: a.Config.Home, CWD: a.Config.Home}
	warm, err := a.connect(context.Background(), s, []string{"codex"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	warm.Close()
	baseline := runtime.NumGoroutine()
	before, fdErr := os.ReadDir("/dev/fd")
	for i := 0; i < 20; i++ {
		c, err := a.connect(context.Background(), s, []string{"codex"}, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.RPC("initialize", map[string]any{}, time.Second)
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline+4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runtime.NumGoroutine() > baseline+4 {
		t.Fatal("Connection goroutines accumulated")
	}
	if after, err := os.ReadDir("/dev/fd"); fdErr == nil && err == nil && len(after) > len(before)+2 {
		t.Fatalf("Connection descriptors accumulated: %d -> %d", len(before), len(after))
	}
}

func TestNativeCloseEscalatesForIgnoringProcessesAndDescendants(t *testing.T) {
	for _, mode := range []string{"ignore-stop", "ignoring-descendant"} {
		a := providerFixture(t, mode)
		path := filepath.Join(a.Config.Home, "ignoring.pid")
		a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_PID="+path)
		c, err := a.connect(context.Background(), Session{Provider: "Codex", Root: a.Config.Home, CWD: a.Config.Home}, []string{"codex"}, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		var data []byte
		for time.Now().Before(deadline) {
			data, err = os.ReadFile(path)
			if err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if err != nil {
			c.Close()
			t.Fatal("Child never started", err)
		}
		started := time.Now()
		c.Close()
		if time.Since(started) > 3*time.Second {
			t.Fatal("Close did not bound SIGTERM grace time")
		}
		pid, parseErr := strconv.Atoi(string(data))
		if parseErr != nil || pid <= 1 {
			t.Fatal("Child published an invalid PID", mode)
		}
		deadline = time.Now().Add(time.Second)
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if syscall.Kill(pid, 0) == nil {
			t.Fatal("Ignoring child survived cleanup", mode, pid)
		}
	}
}

func TestNativeStartDistinguishesCancellationFromLaunchFailure(t *testing.T) {
	a := fixture(t)
	calls := 0
	a.commandPath = func(string, []string) (string, error) { calls++; return "/camp/missing-provider", nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.connect(ctx, Session{}, []string{"codex"}, false, nil); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("Cancelled request attempted launch", calls, err)
	}
	if _, err := a.connect(context.Background(), Session{}, []string{"codex"}, false, nil); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("Launch failure was reported as cancellation", err)
	}
}

func TestNativeWriteDeadlineStopsAnUnresponsiveReader(t *testing.T) {
	a := providerFixture(t, "wait")
	c, err := a.connect(context.Background(), Session{Provider: "Codex", Root: a.Config.Home, CWD: a.Config.Home}, []string{"codex"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	err = c.writeBefore(map[string]any{"text": strings.Repeat("x", 512<<10)}, time.Now().Add(40*time.Millisecond))
	if err == nil || time.Since(start) > time.Second || c.ctx.Err() == nil {
		t.Fatal("Unresponsive input was not bounded", err)
	}
}

func TestNativeRPCRejectsUnapprovedToolsAndBuffersAllowedRequests(t *testing.T) {
	for _, mode := range []string{"tool", "early-tool", "tool-collision"} {
		t.Run(mode, func(t *testing.T) {
			a := providerFixture(t, "rpc")
			a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_RPC="+mode)
			c, err := a.connect(context.Background(), Session{Provider: "Codex", Root: a.Config.Home, CWD: a.Config.Home}, []string{"codex"}, mode == "early-tool", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			result, err := c.RPC("initialize", map[string]any{}, time.Second)
			if err != nil || result["ok"] != true {
				t.Fatal(result, err)
			}
			if mode == "early-tool" {
				event, err := c.Next()
				if err != nil || str(event["id"]) != "tool-1" {
					t.Fatal(event, err)
				}
			}
		})
	}
}

func TestNativeMalformedResponsesFailClosed(t *testing.T) {
	for _, mode := range []string{"bad-result", "bad-error"} {
		a := providerFixture(t, "rpc")
		a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_RPC="+mode)
		c, err := a.connect(context.Background(), Session{Provider: "Codex", Root: a.Config.Home, CWD: a.Config.Home}, []string{"codex"}, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.RPC("configRequirements/read", map[string]any{}, time.Second)
		c.Close()
		if err == nil {
			t.Fatal("Malformed managed-policy response accepted", mode)
		}
	}
}

func TestNativeConnectionBoundsBacklogAndEvents(t *testing.T) {
	for _, mode := range []string{"large", "rpc"} {
		a := providerFixture(t, mode)
		if mode == "rpc" {
			a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_RPC=overflow")
		}
		c, err := a.connect(context.Background(), Session{Provider: "Codex", Root: a.Config.Home, CWD: a.Config.Home}, []string{"codex"}, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.RPC("initialize", map[string]any{}, time.Second)
		c.Close()
		if err == nil || (mode == "large" && !strings.Contains(err.Error(), "size limit")) || (mode == "rpc" && !strings.Contains(err.Error(), "too many events")) {
			t.Fatal(mode, err)
		}
	}
}

func TestNativeRPCKeepsStreamingEventsWhileWaiting(t *testing.T) {
	a := providerFixture(t, "rpc")
	a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_RPC=busy")
	c, err := a.connect(context.Background(), Session{Provider: "Codex", Root: a.Config.Home, CWD: a.Config.Home}, []string{"codex"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.RPC("initialize", map[string]any{}, 5*time.Second); err != nil {
		t.Fatal("streamed events before the reply aborted the request", err)
	}
	for i := range 200 {
		event, err := c.Next()
		if err != nil || event["method"] != "test/notification" {
			t.Fatal("held event lost", i, event, err)
		}
	}
}

func TestNativeCloseUnblocksBackpressureAndUnacknowledgedRPC(t *testing.T) {
	for _, mode := range []string{"flood", "rpc"} {
		a := providerFixture(t, mode)
		a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_RPC=stall")
		ctx, cancel := context.WithCancel(context.Background())
		c, err := a.connect(ctx, Session{Provider: "Claude", Root: a.Config.Home, CWD: a.Config.Home}, []string{"claude"}, false, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if mode == "flood" {
			deadline := time.Now().Add(time.Second)
			for len(c.events) < cap(c.events) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if len(c.events) != 16 {
				cancel()
				c.Close()
				t.Fatal("Fixture did not fill bounded queue")
			}
			cancel()
		} else {
			go func() { time.Sleep(30 * time.Millisecond); cancel() }()
			start := time.Now()
			if _, err := c.RPC("initialize", map[string]any{}, 5*time.Second); err == nil || time.Since(start) > time.Second {
				t.Fatal("RPC did not cancel promptly", err)
			}
		}
		c.Close()
		if syscall.Kill(c.command.Process.Pid, 0) == nil {
			t.Fatal("Native child survived close")
		}
		select {
		case <-c.waitDone:
		default:
			t.Fatal("Readers survived close")
		}
	}
}
