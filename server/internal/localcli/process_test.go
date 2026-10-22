package localcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary acts as a fake provider process. Production has no test
// switches, and these child processes receive only their fixture environment.
func TestMain(m *testing.M) {
	mode := os.Getenv("CAMP_LOCAL_TEST_PROCESS")
	if mode == "" {
		os.Exit(m.Run())
	}
	switch mode {
	case "reply":
		fmt.Print(os.Getenv("CAMP_LOCAL_TEST_REPLY"))
	case "failure":
		fmt.Fprintln(os.Stderr, "private-provider-output-must-not-escape")
		os.Exit(1)
	case "capture":
		data, _ := json.Marshal(map[string]any{"args": os.Args[1:], "environment": os.Environ()})
		fmt.Print(string(data))
	case "large":
		fmt.Print(strings.Repeat("x", 3<<20))
	case "flood":
		for i := 0; i < 10000; i++ {
			fmt.Println(`{"type":"notice","text":"` + strings.Repeat("x", 1000) + `"}`)
		}
	case "rpc":
		runRPCFixture()
	case "context-tool":
		if err := (&App{}).ServeContext(context.Background(), os.Getenv("CAMP_LOCAL_TEST_MANIFEST"), os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
	case "conversation-codex":
		runConversationCodexFixture()
	case "conversation-claude":
		runConversationClaudeFixture()
	case "wait", "ignore-stop":
		if mode == "ignore-stop" {
			signal.Ignore(syscall.SIGTERM)
		}
		if path := os.Getenv("CAMP_LOCAL_TEST_PID"); path != "" {
			// File existence is the parent's readiness signal. Publish the PID
			// atomically so it never observes a newly created, empty file.
			temporary := path + "." + strconv.Itoa(os.Getpid())
			if os.WriteFile(temporary, []byte(strconv.Itoa(os.Getpid())), 0600) != nil || os.Rename(temporary, path) != nil {
				os.Exit(2)
			}
		}
		for {
			time.Sleep(time.Second)
		}
	case "descendant", "ignoring-descendant":
		child := exec.Command(os.Args[0])
		child.Env = append([]string{}, os.Environ()...)
		for i, entry := range child.Env {
			if strings.HasPrefix(entry, "CAMP_LOCAL_TEST_PROCESS=") {
				child.Env[i] = "CAMP_LOCAL_TEST_PROCESS=wait"
				if mode == "ignoring-descendant" {
					child.Env[i] = "CAMP_LOCAL_TEST_PROCESS=ignore-stop"
				}
			}
		}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		// Wait for proof that the descendant started before the parent exits.
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(os.Getenv("CAMP_LOCAL_TEST_PID")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

func providerFixture(t *testing.T, mode string) *App {
	t.Helper()
	a := fixture(t)
	bin := filepath.Join(a.Config.Home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		if err := os.Symlink(self, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	a.Config.Environment = []string{"PATH=" + bin, "CAMP_LOCAL_TEST_PROCESS=" + mode, "GORACE=atexit_sleep_ms=0"}
	a.commandPath = func(name string, _ []string) (string, error) {
		if name != "claude" && name != "codex" {
			return "", errors.New("Unexpected fixture command")
		}
		return filepath.Join(bin, name), nil
	}
	return a
}

func runRPCFixture() {
	decoder := json.NewDecoder(os.Stdin)
	decoder.UseNumber()
	for {
		var request map[string]any
		if decoder.Decode(&request) != nil {
			return
		}
		id, exists := request["id"]
		if !exists {
			continue
		}
		mode := os.Getenv("CAMP_LOCAL_TEST_RPC")
		if mode == "stall" {
			for {
				time.Sleep(time.Second)
			}
		}
		if mode == "overflow" {
			for i := 0; i < 40; i++ {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"method": "test/notification", "params": map[string]any{"n": i}})
			}
		}
		if mode == "tool" || mode == "early-tool" || mode == "tool-collision" {
			var toolID any = "tool-1"
			if mode == "tool-collision" {
				toolID = id
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"id": toolID, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "one", "turnId": "turn"}})
			if mode != "early-tool" {
				var reply map[string]any
				if decoder.Decode(&reply) != nil || !sameID(reply["id"], toolID) || object(reply["error"]) == nil {
					os.Exit(5)
				}
			}
		}
		if mode == "bad-result" || mode == "bad-error" {
			response := map[string]any{"id": id, "result": []string{"invalid"}}
			if mode == "bad-error" {
				response = map[string]any{"id": id, "error": "invalid"}
			}
			_ = json.NewEncoder(os.Stdout).Encode(response)
			continue
		}
		result := map[string]any{}
		switch str(request["method"]) {
		case "config/read":
			var config map[string]any
			_ = json.Unmarshal([]byte(os.Getenv("CAMP_LOCAL_TEST_CONFIG")), &config)
			if config == nil {
				config = map[string]any{}
			}
			result["config"] = config
		case "configRequirements/read":
			var requirements map[string]any
			_ = json.Unmarshal([]byte(os.Getenv("CAMP_LOCAL_TEST_REQUIREMENTS")), &requirements)
			if requirements == nil {
				requirements = map[string]any{}
			}
			result["requirements"] = requirements
		default:
			result["ok"] = true
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"id": id, "result": result})
	}
}

func TestStatusProbeUsesSafeArgsAndSelectedProfile(t *testing.T) {
	a := providerFixture(t, "capture")
	out, err := a.probe(context.Background(), Session{Provider: "Claude", Root: "/fixture/profile"}, []string{"claude", "--safe-mode", "agents", "--json"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Args        []string `json:"args"`
		Environment []string `json:"environment"`
	}
	if err := json.Unmarshal(out, &capture); err != nil || strings.Join(capture.Args, " ") != "--safe-mode agents --json" {
		t.Fatal(capture, err)
	}
	found := false
	for _, entry := range capture.Environment {
		found = found || entry == "CLAUDE_CONFIG_DIR=/fixture/profile"
	}
	if !found {
		t.Fatal("Probe used a different profile")
	}
}

func TestProbeHidesPrivateErrorsAndBoundsOutput(t *testing.T) {
	for _, mode := range []string{"failure", "large"} {
		a := providerFixture(t, mode)
		_, err := a.probe(context.Background(), Session{Provider: "Claude", Root: a.Config.Home}, []string{"claude"}, time.Second)
		if err == nil || strings.Contains(err.Error(), "private-provider-output") {
			t.Fatal("Unsafe error", err)
		}
		if mode == "large" && !errors.Is(err, errProcessOutputLimit) {
			t.Fatal("Output bound was not enforced", err)
		}
	}
}

func TestProbeReapsOwnedChildrenOnDeadlineAndParentExit(t *testing.T) {
	for _, mode := range []string{"wait", "descendant"} {
		t.Run(mode, func(t *testing.T) {
			a := providerFixture(t, mode)
			path := filepath.Join(a.Config.Home, "child.pid")
			a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_PID="+path)
			start := time.Now()
			_, err := a.probe(context.Background(), Session{Provider: "Claude", Root: a.Config.Home}, []string{"claude"}, 300*time.Millisecond)
			if err == nil || time.Since(start) > 2*time.Second {
				t.Fatal("Probe did not stop promptly", err)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			pid, _ := strconv.Atoi(string(data))
			deadline := time.Now().Add(time.Second)
			for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if syscall.Kill(pid, 0) == nil {
				t.Fatal("Owned child survived", pid)
			}
		})
	}
}
