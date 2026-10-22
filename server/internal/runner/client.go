// Package runner signs in to a provider and prepares Git workspaces under a
// single installation grant. It does not accept arbitrary shell commands.
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
	return c.callTimeout(ctx, method, path, input, output, 5*time.Second)
}

func (c *client) callTimeout(ctx context.Context, method, path string, input, output any, timeout time.Duration) error {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return ErrState
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
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

func (c *client) nextWorkspace(ctx context.Context) (*camp.Workspace, error) {
	var out *camp.Workspace
	err := c.call(ctx, "GET", "/v1/runner/workspaces/next", nil, &out)
	return out, err
}

func (c *client) workspaceClaim(ctx context.Context, r *workspaceRecord, operation string) (camp.RunnerWorkspaceClaim, error) {
	var out camp.RunnerWorkspaceClaim
	err := c.call(ctx, "POST", "/v1/runner/workspaces/"+r.Workspace.ID+"/"+operation, map[string]string{"claim_id": r.ClaimID}, &out)
	if err == nil && (out.ClaimID != r.ClaimID || !sameWorkspace(out.Workspace, r.Workspace) || out.Workspace.State != "preparing" || !out.LeaseUntil.After(time.Now()) || out.Deadline.Before(out.LeaseUntil)) {
		err = ErrState
	}
	return out, err
}

func (c *client) workspaceToken(ctx context.Context, r *workspaceRecord) (camp.RunnerWorkspaceToken, error) {
	var out camp.RunnerWorkspaceToken
	err := c.callTimeout(ctx, "POST", "/v1/runner/workspaces/"+r.Workspace.ID+"/token", map[string]string{"claim_id": r.ClaimID}, &out, 35*time.Second)
	if err == nil && (out.WorkspaceID != r.Workspace.ID || out.ClaimID != r.ClaimID || !validGitToken(out.Token) || !out.ExpiresAt.After(time.Now())) {
		err = ErrState
	}
	return out, err
}

func (c *client) workspaceStatus(ctx context.Context, r *workspaceRecord) (camp.Workspace, error) {
	var out camp.Workspace
	err := c.call(ctx, "POST", "/v1/runner/workspaces/"+r.Workspace.ID+"/status", map[string]string{"claim_id": r.ClaimID}, &out)
	if err == nil && !sameWorkspace(out, r.Workspace) {
		err = ErrState
	}
	return out, err
}

func (c *client) workspaceReport(ctx context.Context, r *workspaceRecord) (camp.Workspace, error) {
	var out camp.Workspace
	in := struct {
		ClaimID string `json:"claim_id"`
		camp.WorkspaceReport
	}{r.ClaimID, *r.Report}
	err := c.call(ctx, "POST", "/v1/runner/workspaces/"+r.Workspace.ID+"/report", in, &out)
	if err == nil && (!sameWorkspace(out, r.Workspace) || out.State != r.Report.State || out.FailureCode != r.Report.FailureCode) {
		err = ErrState
	}
	return out, err
}
