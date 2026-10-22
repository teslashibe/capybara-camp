package localcli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const maxInferenceText = 4 << 20
const maxInferenceWire = 8 << 20

// InferenceRequest is a single tool-disabled call, separate from every saved
// conversation. The caller must authorize and persist the job before calling.
type InferenceRequest struct {
	Session Session `json:"session"`
	Model   string  `json:"model"`
	Effort  string  `json:"effort"`
	Speed   string  `json:"speed"`
	System  string  `json:"system"`
	Prompt  string  `json:"prompt"`
}

type InferenceResult struct {
	Text         string `json:"text"`
	Model        string `json:"model"`
	Effort       string `json:"effort"`
	Speed        string `json:"speed"`
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
}

// Infer uses the same official CLI transport and model capability validation as
// chat. It never resumes a working thread, steers, repairs output or retries.
func (a *App) Infer(ctx context.Context, input InferenceRequest) (InferenceResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var result InferenceResult
	wire, err := json.Marshal(struct {
		System string `json:"system"`
		Prompt string `json:"prompt"`
	}{input.System, input.Prompt})
	if err != nil || len(wire) > 1<<20 || !utf8.ValidString(input.System) || !utf8.ValidString(input.Prompt) || strings.TrimSpace(input.System) == "" || strings.TrimSpace(input.Prompt) == "" || strings.TrimSpace(input.Model) == "" {
		return result, errors.New("Choose a model and a complete inference request under 1 MiB")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = os.MkdirAll(a.Config.Support, 0700); err != nil {
		return result, errors.New("Could not open Camp's local state")
	}
	work, err := os.MkdirTemp(a.Config.Support, "camp-inference-")
	if err != nil {
		return result, errors.New("Could not create the private inference folder")
	}
	defer os.RemoveAll(work)
	instructions := filepath.Join(work, "instructions.txt")
	if err = os.WriteFile(instructions, []byte(input.System), 0600); err != nil {
		return result, errors.New("Could not prepare inference instructions")
	}
	input.Session.CWD, input.Session.SessionID = work, uuid.NewString()
	request := conversationRequest{Session: input.Session, Model: input.Model, Effort: input.Effort, Speed: input.Speed, Prompt: input.Prompt, Mode: "inference", inferenceSystem: input.System, inferenceSystemFile: instructions}
	complete := false
	err = a.runConversation(ctx, request, nil, func(event map[string]any) error {
		switch str(event["type"]) {
		case "text":
			result.Text = str(event["text"])
		case "settings":
			result.Model, result.Effort, result.Speed = str(event["model"]), str(event["effort"]), str(event["speed"])
		case "usage":
			if n, ok := event["inputTokens"].(int64); ok {
				result.InputTokens = &n
			}
			if n, ok := event["outputTokens"].(int64); ok {
				result.OutputTokens = &n
			}
		case "done":
			complete = true
		case "error":
			message := str(event["message"])
			if message == "" || len(message) > 1024 {
				return errors.New("The selected provider did not complete inference")
			}
			return errors.New("The selected provider did not complete inference: " + message)
		case "session", "steerAccepted", "approval":
			return errors.New("Inference attempted an unsupported conversation action")
		}
		return nil
	})
	// Cancellation closes the provider pipes as well as its context. An RPC
	// waiting for its startup acknowledgement can observe EOF first. Preserve
	// cancellation after runConversation has joined and reaped the process.
	if ctx.Err() != nil {
		return InferenceResult{}, ctx.Err()
	}
	if err != nil {
		return InferenceResult{}, err
	}
	if !complete || strings.TrimSpace(result.Text) == "" {
		return InferenceResult{}, errors.New("The selected provider returned no completed inference")
	}
	return result, nil
}
