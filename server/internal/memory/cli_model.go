package memory

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/teslashibe/capybara-camp/server/internal/localcli"
	"github.com/teslashibe/cortex"
	"github.com/teslashibe/cortex/llm"
)

// CLIModel preserves Cortex's exact system/user request for a host-authorized
// dispatch. The dispatcher binds its saved job/account and checks the hash
// against the one supplied by Cortex's inference guard before sending.
type CLIModel struct {
	Name     string
	Effort   string
	Speed    string
	Dispatch func(context.Context, localcli.InferenceRequest, string) (localcli.InferenceResult, error)
}

func (m CLIModel) Model() string { return m.Name }
func (m CLIModel) Complete(ctx context.Context, messages []llm.Message) (llm.Response, error) {
	var response llm.Response
	if m.Dispatch == nil || strings.TrimSpace(m.Name) == "" || len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" {
		return response, errors.New("Memory inference requires the exact Cortex system and user messages")
	}
	if err := llm.ValidateRequest(m.Name, messages); err != nil {
		return response, err
	}
	hash, err := cortex.ConsolidationPromptHash(m.Name, messages)
	if err != nil {
		return response, err
	}
	if err = ctx.Err(); err != nil {
		return response, err
	}
	result, err := m.Dispatch(ctx, localcli.InferenceRequest{Model: m.Name, Effort: m.Effort, Speed: m.Speed, System: messages[0].Content, Prompt: messages[1].Content}, hash)
	if err != nil {
		return response, err
	}
	if len(result.Text) > 4<<20 || strings.TrimSpace(result.Text) == "" {
		return response, errors.New("Memory inference returned invalid output")
	}
	response.Text, response.Model = result.Text, result.Model
	for _, count := range []*int64{result.InputTokens, result.OutputTokens} {
		if count != nil && (*count < 0 || *count > math.MaxInt32) {
			return llm.Response{}, errors.New("Memory inference returned invalid token counts")
		}
	}
	// Missing provider counts stay unreported in the job result. Cortex's
	// aggregate is populated only when both counts were actually returned.
	if result.InputTokens != nil && result.OutputTokens != nil {
		input, output := *result.InputTokens, *result.OutputTokens
		if input < 0 || output < 0 || input > math.MaxInt32 || output > math.MaxInt32-input {
			return llm.Response{}, errors.New("Memory inference returned invalid token counts")
		}
		response.Usage = llm.Usage{PromptTokens: int(input), CompletionTokens: int(output), TotalTokens: int(input + output)}
	}
	return response, nil
}
