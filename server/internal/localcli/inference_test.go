package localcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func inferenceFixtureReply() string { return strings.Repeat("🌱", 600000) }
func checkInferencePrompt(prompt string) {
	digest := sha256.Sum256([]byte(prompt))
	dir, err := os.Getwd()
	profile, profileErr := filepath.EvalSymlinks(os.Getenv("CAMP_LOCAL_TEST_PROFILE"))
	if err != nil || profileErr != nil || !strings.HasPrefix(filepath.Base(dir), "camp-inference-") || profile == "" || os.Getenv("CODEX_HOME") != profile && os.Getenv("CLAUDE_CONFIG_DIR") != profile || hex.EncodeToString(digest[:]) != os.Getenv("CAMP_LOCAL_TEST_PROMPT_HASH") {
		os.Exit(5)
	}
}

func TestInferenceCancellationReapsProviderAndReleasesProfile(t *testing.T) {
	for _, flow := range []string{"stall", "stall-before-initialize"} {
		t.Run(flow, func(t *testing.T) { testInferenceCancellation(t, flow) })
	}
}

func testInferenceCancellation(t *testing.T, flow string) {
	t.Helper()
	a := providerFixture(t, "conversation-codex")
	pidPath := filepath.Join(a.Config.Home, "inference.pid")
	a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_FLOW="+flow, "CAMP_LOCAL_TEST_PID="+pidPath)
	input := InferenceRequest{Session: Session{Provider: "Codex", Root: a.Config.Home}, Model: "model", System: "system", Prompt: "prompt"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.Infer(ctx, input); done <- err }()
	var pid []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		pid, _ = os.ReadFile(pidPath)
		if len(pid) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(pid) == 0 {
		cancel()
		<-done
		t.Fatal("inference process did not start")
	}
	if _, err := a.Infer(t.Context(), input); err == nil || !strings.Contains(err.Error(), "already running") {
		cancel()
		<-done
		t.Fatal("same profile started twice", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	process, _ := strconv.Atoi(string(pid))
	if err := syscall.Kill(process, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatal("inference process was not reaped", err)
	}
	files, _ := filepath.Glob(filepath.Join(a.Config.Support, "camp-inference-*"))
	if len(files) != 0 {
		t.Fatal("cancelled inference retained files", files)
	}
	locks, err := a.sessionLocks()
	if err != nil {
		t.Fatal(err)
	}
	defer locks.close()
	root, _ := filepath.EvalSymlinks(a.Config.Home)
	if err = locks.acquire("inference:Codex:" + root); err != nil {
		t.Fatal("cancelled inference retained profile lock", err)
	}
}

func TestInferenceRejectsToolsAndBoundsOutput(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		var err error
		request := conversationRequest{Mode: "inference", Session: Session{SessionID: "original"}}
		f := &turnFixture{}
		if provider == "Codex" {
			f.events = []map[string]any{{"id": 1, "method": "item/commandExecution/requestApproval"}}
			err = codexTurn(f, request, nil, func(map[string]any) error { return nil })
		} else {
			f.events = []map[string]any{{"type": "control_request", "request_id": "tool"}}
			err = claudeTurn(f, request, nil, func(map[string]any) error { return nil })
		}
		if err == nil || !strings.Contains(err.Error(), "cannot request tools") {
			t.Fatal(provider, err)
		}
	}
	if err := emitConversationText(conversationRequest{Mode: "inference"}, func(map[string]any) error { return nil }, strings.Repeat("x", maxInferenceText+1)); err == nil {
		t.Fatal("oversized output accepted")
	}
	a := providerFixture(t, "conversation-codex")
	for _, body := range []string{`{"unexpected":true}`, `{} {}`} {
		if _, err := a.Run(t.Context(), "memory-inference", strings.NewReader(body)); err == nil {
			t.Fatal("invalid helper input accepted")
		}
	}
}

func TestInferenceUsesIsolatedProviderTurnAndCleansPrivateFiles(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			a := providerFixture(t, "conversation-"+strings.ToLower(provider))
			if err := os.WriteFile(filepath.Join(a.Config.Home, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-5.4","visibility":"list","supported_reasoning_levels":[{"effort":"high"}]}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			prompt := strings.Repeat("memory source ", 15000)
			digest := sha256.Sum256([]byte(prompt))
			a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_FLOW=inference", "CAMP_LOCAL_TEST_PROMPT_HASH="+hex.EncodeToString(digest[:]), "CAMP_LOCAL_TEST_PROFILE="+a.Config.Home)
			original := filepath.Join(a.Config.Home, "project")
			if err := os.MkdirAll(original, 0700); err != nil {
				t.Fatal(err)
			}
			model := "gpt-5.4"
			if provider == "Claude" {
				model = "claude-opus-4-6"
			}
			in := InferenceRequest{Session: Session{Provider: provider, Root: a.Config.Home, CWD: original, SessionID: "original"}, System: "Synthetic memory system", Prompt: prompt, Model: model, Effort: "high", Speed: "standard"}
			ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
			defer stop()
			wire, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			value, err := a.Run(ctx, "memory-inference", bytes.NewReader(wire))
			out, _ := value.(InferenceResult)
			if err != nil || out.Text != inferenceFixtureReply() || out.InputTokens == nil || *out.InputTokens != 12 || out.OutputTokens == nil || *out.OutputTokens != 9 || out.Model != model {
				t.Fatalf("inference: model=%q bytes=%d err=%v", out.Model, len(out.Text), err)
			}
			files, err := filepath.Glob(filepath.Join(a.Config.Support, "camp-inference-*"))
			if err != nil || len(files) != 0 {
				t.Fatal("private inference files retained", files, err)
			}
			files, err = filepath.Glob(filepath.Join(original, "*"))
			if err != nil || len(files) != 0 {
				t.Fatal("original project changed", files, err)
			}
		})
	}
}

func TestInferenceRejectsInvalidOrIncompleteWorkWithoutOutput(t *testing.T) {
	a := providerFixture(t, "conversation-codex")
	in := InferenceRequest{Session: Session{Provider: "Codex", Root: a.Config.Home}, System: "Synthetic memory system", Prompt: "test", Model: "gpt-5.4"}
	for _, change := range []func(*InferenceRequest){func(v *InferenceRequest) { v.System = "" }, func(v *InferenceRequest) { v.Model = "" }, func(v *InferenceRequest) { v.Prompt = strings.Repeat("x", 1<<20) }, func(v *InferenceRequest) { v.Prompt = string([]byte{255}) }, func(v *InferenceRequest) { v.Effort = "unsupported" }} {
		bad := in
		change(&bad)
		out, err := a.Infer(t.Context(), bad)
		if err == nil || out.Text != "" {
			t.Fatal("invalid inference accepted")
		}
	}
	ctx, stop := context.WithCancel(t.Context())
	stop()
	if _, err := a.Infer(ctx, in); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, flow := range []string{"failure", "malformed"} {
		a.Config.Environment = append(a.Config.Environment, "CAMP_LOCAL_TEST_FLOW="+flow)
		out, err := a.Infer(t.Context(), in)
		if err == nil || out.Text != "" {
			t.Fatal("incomplete inference accepted", flow)
		}
		if flow == "failure" && !strings.Contains(err.Error(), "session limit reached") {
			t.Fatal("provider failure reason was lost", err)
		}
	}
	if err := a.runConversation(t.Context(), conversationRequest{Mode: "inference"}, nil, func(map[string]any) error { return nil }); err == nil {
		t.Fatal("public conversation entered inference")
	}
}
