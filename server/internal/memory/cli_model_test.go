package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/teslashibe/capybara-camp/server/internal/localcli"
	"github.com/teslashibe/cortex"
	"github.com/teslashibe/cortex/llm"
)

func TestCLIModelPreservesExactRequestAndDispatchesOnce(t *testing.T) {
	messages := []llm.Message{{Role: "system", Content: "Exact system\n🌱"}, {Role: "user", Content: "Exact JSON: {\"source\":\"one\"}"}}
	hash, err := cortex.ConsolidationPromptHash("model-alias", messages)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	input, output := int64(30), int64(12)
	m := CLIModel{Name: "model-alias", Effort: "high", Speed: "fast", Dispatch: func(ctx context.Context, r localcli.InferenceRequest, h string) (localcli.InferenceResult, error) {
		calls++
		if r.System != messages[0].Content || r.Prompt != messages[1].Content || r.Model != "model-alias" || r.Effort != "high" || r.Speed != "fast" || r.Session.Root != "" || h != hash {
			t.Fatal("dispatch changed the approved request")
		}
		return localcli.InferenceResult{Text: `{"ops":[]}`, Model: "resolved-version", InputTokens: &input, OutputTokens: &output}, nil
	}}
	result, err := m.Complete(t.Context(), messages)
	if err != nil || calls != 1 || result.Model != "resolved-version" || result.Usage.TotalTokens != 42 {
		t.Fatal(result, calls, err)
	}
	failure := errors.New("uncertain provider outcome")
	m.Dispatch = func(context.Context, localcli.InferenceRequest, string) (localcli.InferenceResult, error) {
		calls++
		return localcli.InferenceResult{}, failure
	}
	if _, err = m.Complete(t.Context(), messages); !errors.Is(err, failure) || calls != 2 {
		t.Fatal("dispatch retried or hid failure", calls, err)
	}
}

func TestCLIModelRejectsUnapprovedInputAndInvalidResults(t *testing.T) {
	calls := 0
	m := CLIModel{Name: "model", Dispatch: func(context.Context, localcli.InferenceRequest, string) (localcli.InferenceResult, error) {
		calls++
		return localcli.InferenceResult{Text: "output"}, nil
	}}
	valid := []llm.Message{{Role: "system", Content: "system"}, {Role: "user", Content: "input"}}
	for _, messages := range [][]llm.Message{nil, {{Role: "user", Content: "wrong role"}}, {{Role: "system", Content: "system"}, {Role: "assistant", Content: "wrong role"}}, {{Role: "system", Content: "system"}, {Role: "user", Content: strings.Repeat("x", 1<<20)}}} {
		if _, err := m.Complete(t.Context(), messages); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.Complete(ctx, valid); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal(calls, err)
	}
	negative := int64(-1)
	for _, output := range []localcli.InferenceResult{{}, {Text: strings.Repeat("x", 4<<20+1)}, {Text: "text", InputTokens: &negative}} {
		m.Dispatch = func(context.Context, localcli.InferenceRequest, string) (localcli.InferenceResult, error) {
			return output, nil
		}
		if _, err := m.Complete(t.Context(), valid); err == nil {
			t.Fatal("invalid output accepted")
		}
	}
	m.Dispatch = func(context.Context, localcli.InferenceRequest, string) (localcli.InferenceResult, error) {
		return localcli.InferenceResult{Text: "text"}, nil
	}
	result, err := m.Complete(t.Context(), valid)
	if err != nil || result.Model != "" || result.Usage.TotalTokens != 0 {
		t.Fatal("missing reporting was invented", result, err)
	}
}
