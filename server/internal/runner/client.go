// Package runner executes fixed provider sign-in operations under a single
// installation grant. It does not execute prompts or accept shell commands.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

var (
	ErrUnavailable = errors.New("Runner could not reach its Camp service")
	ErrRejected    = errors.New("Runner authorization or operation was rejected")
	ErrState       = errors.New("Runner state could not be recovered safely")
)

type client struct {
	origin, token string
	http          *http.Client
}

func (c *client) call(ctx context.Context, method, path string, input, output any) error {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return ErrState
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, bytes.NewReader(body))
	if err != nil {
		return ErrState
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 429 || res.StatusCode >= 500 {
		return ErrUnavailable
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ErrRejected
	}
	if output == nil || res.StatusCode == 204 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (384<<10)+1))
	if err != nil || len(data) > 384<<10 || json.Unmarshal(data, output) != nil {
		return ErrUnavailable
	}
	return nil
}

func (c *client) account(ctx context.Context) (camp.RunnerAccountState, error) {
	var out camp.RunnerAccountState
	err := c.call(ctx, "GET", "/v1/runner/account", nil, &out)
	return out, err
}

func (c *client) claim(ctx context.Context, r *record) error {
	var out camp.ProviderLogin
	if err := c.call(ctx, "POST", "/v1/runner/logins/"+r.AttemptID+"/claim", map[string]string{"claim_id": r.ClaimID}, &out); err != nil {
		return err
	}
	if out.ID != r.AttemptID || out.InstallationID != r.InstallationID || (out.State != "running" && out.State != "waiting") {
		return ErrRejected
	}
	return nil
}

func (c *client) report(ctx context.Context, r *record, report camp.RunnerLoginReport) (camp.ProviderLogin, error) {
	var out camp.ProviderLogin
	input := struct {
		ClaimID string `json:"claim_id"`
		camp.RunnerLoginReport
	}{r.ClaimID, report}
	err := c.call(ctx, "POST", "/v1/runner/logins/"+r.AttemptID+"/report", input, &out)
	return out, err
}

func (c *client) takeCode(ctx context.Context, r *record) (*camp.RunnerLoginCode, error) {
	var out *camp.RunnerLoginCode
	err := c.call(ctx, "POST", "/v1/runner/logins/"+r.AttemptID+"/code", map[string]string{"claim_id": r.ClaimID, "delivery_id": r.DeliveryID}, &out)
	return out, err
}

func (c *client) ackCode(ctx context.Context, r *record, code *camp.RunnerLoginCode) error {
	return c.call(ctx, "POST", "/v1/runner/logins/"+r.AttemptID+"/code/ack", map[string]string{"claim_id": r.ClaimID, "delivery_id": r.DeliveryID, "code_id": code.CodeID}, nil)
}
