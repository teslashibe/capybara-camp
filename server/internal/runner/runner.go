package runner

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
	"github.com/teslashibe/capybara-camp/server/internal/processguard"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

type loginFunc func(context.Context, string, string, func(localcli.ProviderLoginChallenge) error, <-chan string) (localcli.ProviderLoginResult, error)

type Worker struct {
	stateDir       string
	client         *client
	journal        *journal
	checkout       *checkout
	readyWorkspace string
	login          loginFunc
	interval       time.Duration
	environment    []string
	guarded        bool
}

func New(cfg config.Runner, transport *http.Client) (*Worker, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.WorkspaceDir != "" {
		resolved := cfg
		var err error
		if resolved.StateDir, err = filepath.EvalSymlinks(cfg.StateDir); err != nil {
			return nil, ErrState
		}
		if resolved.WorkspaceDir, err = filepath.EvalSymlinks(cfg.WorkspaceDir); err != nil {
			return nil, ErrState
		}
		if resolved.GrantFile, err = filepath.EvalSymlinks(cfg.GrantFile); err != nil {
			return nil, ErrState
		}
		if resolved.Validate() != nil {
			return nil, ErrState
		}
	}
	raw, err := privateFile(cfg.GrantFile, 128)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(token, "campr_") || len(token) != 49 {
		return nil, ErrRejected
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token[6:])
	if err != nil || len(decoded) != 32 {
		return nil, ErrRejected
	}
	j, err := openJournal(cfg.StateDir, token, cfg.Origin)
	if err != nil {
		return nil, err
	}
	if transport == nil {
		transport = http.DefaultClient
	}
	copyClient := *transport
	copyClient.Timeout = 40 * time.Second
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	w := &Worker{stateDir: cfg.StateDir, client: &client{origin: cfg.Origin, token: token, http: &copyClient}, journal: j, interval: 2 * time.Second, environment: append([]string{}, cfg.Environment...), guarded: true}
	w.login = func(ctx context.Context, provider, base string, challenge func(localcli.ProviderLoginChallenge) error, codes <-chan string) (localcli.ProviderLoginResult, error) {
		adapter := localcli.New(config.Local{Environment: w.environment})
		if w.guarded {
			adapter = localcli.NewConfined(config.Local{Environment: w.environment})
		}
		return adapter.LoginProvider(ctx, provider, base, challenge, codes)
	}
	if cfg.WorkspaceDir != "" {
		w.checkout, err = openCheckout(cfg.WorkspaceDir)
		if err != nil {
			j.close()
			return nil, err
		}
		w.checkout.guarded = true
	}
	return w, nil
}

func (w *Worker) Close() {
	if w.checkout != nil {
		w.checkout.close()
	}
	w.journal.close()
}

// Run serializes provider sign-in and workspace preparation. Container isolation must terminate all
// children before restarting a worker. A started journal never launches again;
// it fails the interrupted attempt unless its final proof was already saved.
func (w *Worker) Run(ctx context.Context) error {
	if w.guarded {
		if err := processguard.Available(); err != nil {
			return err
		}
	}
	if w.checkout != nil && w.checkout.guarded {
		ctx = transfer.Isolated(ctx, w.checkout.dir)
	}
	r, err := w.journal.load()
	if err != nil {
		return err
	}
	if err = w.journal.cleanScratch(); err != nil {
		return err
	}
	if w.checkout != nil {
		if err = w.checkout.cleanCredentials(); err != nil {
			return err
		}
	}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		state, err := w.client.account(ctx)
		if err != nil {
			if !errors.Is(err, ErrUnavailable) {
				return err
			}
			if err = w.wait(ctx); err != nil {
				return err
			}
			continue
		}
		if !validState(state) || r != nil && (r.InstallationID != state.InstallationID || r.Provider != state.Provider) {
			return ErrState
		}
		if state.Action == "erase" {
			if w.checkout != nil {
				if err = w.checkout.cleanAll(); err != nil {
					return err
				}
				if err = w.checkout.eraseRuntime(); err != nil {
					return err
				}
			}
			if err = w.journal.clearWorkspace(); err != nil {
				return err
			}
			if err = w.journal.clear(); err != nil {
				return err
			}
			if err = w.journal.cleanScratch(); err != nil {
				return err
			}
			return w.retry(ctx, func() error { return w.client.call(ctx, "POST", "/v1/runner/erased", nil, nil) })
		}
		if state.Action == "idle" {
			if r != nil {
				if err = w.journal.clear(); err != nil {
					return err
				}
				r = nil
			}
			if w.checkout != nil {
				if err = w.workspaceTick(ctx, state); err != nil && !errors.Is(err, ErrUnavailable) {
					return err
				}
			}
			if err = w.wait(ctx); err != nil {
				return err
			}
			continue
		}
		if r != nil && (r.AttemptID != state.Login.ID || r.Epoch != state.Epoch) {
			if err = w.journal.clear(); err != nil {
				return err
			}
			r = nil
		}
		if r == nil {
			r = &record{InstallationID: state.InstallationID, Provider: state.Provider, Epoch: state.Epoch, AttemptID: state.Login.ID, ClaimID: uuid.NewString(), DeliveryID: uuid.NewString(), Phase: "prepared"}
			if err = w.journal.save(r); err != nil {
				return err
			}
		}
		if r.Phase == "started" {
			// Only a recovered journal reaches this branch. A fresh attempt below
			// enters the provider immediately after persisting its start fence.
			r.Phase, r.Report = "finishing", &camp.RunnerLoginReport{State: "failed"}
			if err = w.journal.save(r); err != nil {
				return err
			}
		}
		if r.Phase == "prepared" {
			if err = w.client.claim(ctx, r); err != nil {
				if errors.Is(err, ErrUnavailable) {
					if err = w.wait(ctx); err != nil {
						return err
					}
					continue
				}
				return err
			}
			r.Phase = "started"
			if err = w.journal.save(r); err != nil {
				return err
			}
			report, interrupted, err := w.runLogin(ctx, state, r)
			if err != nil {
				return err
			}
			if interrupted {
				// Keep the start fence until a fresh server state confirms the
				// attempt is gone. Never create a second local process for it.
				continue
			}
			r.Phase, r.Report = "finishing", &report
			if err = w.journal.save(r); err != nil {
				return err
			}
		}
		if r.Phase == "finishing" {
			out, err := w.client.report(ctx, r, *r.Report)
			if err != nil {
				if !errors.Is(err, ErrUnavailable) {
					return err
				}
			} else {
				if out.ID != r.AttemptID || out.InstallationID != r.InstallationID || out.State != "connected" && out.State != "failed" {
					return ErrState
				}
				r.Phase = "done"
				if err = w.journal.save(r); err != nil {
					return err
				}
			}
		}
		if err = w.wait(ctx); err != nil {
			return err
		}
	}
}

func validState(state camp.RunnerAccountState) bool {
	if _, err := uuid.Parse(state.InstallationID); err != nil || state.Epoch < 1 || state.Provider != "Codex" && state.Provider != "Claude" {
		return false
	}
	if state.Action == "idle" || state.Action == "erase" {
		return state.Login == nil
	}
	if state.Action != "login" || state.Login == nil || state.Login.InstallationID != state.InstallationID || state.Login.ExpiresAt.IsZero() {
		return false
	}
	_, err := uuid.Parse(state.Login.ID)
	return err == nil && (state.Login.State == "queued" || state.Login.State == "running" || state.Login.State == "waiting")
}

func (w *Worker) wait(ctx context.Context) error {
	timer := time.NewTimer(w.interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (w *Worker) retry(ctx context.Context, operation func() error) error {
	until := time.Now().Add(15 * time.Second)
	for {
		err := operation()
		if !errors.Is(err, ErrUnavailable) || time.Now().After(until) {
			return err
		}
		if err = w.wait(ctx); err != nil {
			return err
		}
	}
}

func (w *Worker) runLogin(parent context.Context, state camp.RunnerAccountState, r *record) (camp.RunnerLoginReport, bool, error) {
	ctx, cancel := context.WithDeadline(parent, state.Login.ExpiresAt)
	defer cancel()
	type result struct {
		value localcli.ProviderLoginResult
		err   error
	}
	done := make(chan result, 1)
	codes := make(chan string, 1)
	identity := *r
	go func() {
		value, err := w.login(ctx, state.Provider, w.stateDir, func(c localcli.ProviderLoginChallenge) error {
			report := camp.RunnerLoginReport{State: "waiting", Challenge: &camp.LoginChallenge{URL: c.URL, UserCode: c.UserCode}}
			return w.retry(ctx, func() error { _, err := w.client.report(ctx, &identity, report); return err })
		}, codes)
		done <- result{value, err}
	}()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	tick := time.NewTicker(w.interval)
	defer tick.Stop()
	lastContact := time.Now()
	for {
		select {
		case <-parent.Done():
			return camp.RunnerLoginReport{}, false, parent.Err()
		case <-ctx.Done():
			if err := parent.Err(); err != nil {
				return camp.RunnerLoginReport{}, false, err
			}
			return camp.RunnerLoginReport{State: "failed"}, false, nil
		case output := <-done:
			joined = true
			if err := parent.Err(); err != nil {
				return camp.RunnerLoginReport{}, false, err
			}
			if output.err != nil {
				return camp.RunnerLoginReport{State: "failed"}, false, nil
			}
			return camp.RunnerLoginReport{State: "connected", VerifiedSubject: output.value.Subject, Credentials: output.value.Credentials}, false, nil
		case <-tick.C:
			current, err := w.client.account(ctx)
			if err != nil {
				if !errors.Is(err, ErrUnavailable) || time.Since(lastContact) >= 15*time.Second {
					return camp.RunnerLoginReport{State: "failed"}, false, nil
				}
				continue
			}
			lastContact = time.Now()
			if !validState(current) || current.InstallationID != r.InstallationID || current.Provider != r.Provider {
				return camp.RunnerLoginReport{}, false, ErrState
			}
			if current.Action != "login" || current.Epoch != r.Epoch || current.Login.ID != r.AttemptID {
				return camp.RunnerLoginReport{}, true, nil
			}
			if state.Provider != "Claude" || current.Login.State != "waiting" || !current.Login.CodeSubmitted || r.CodeAttempted {
				continue
			}
			code, err := w.client.takeCode(ctx, r)
			if errors.Is(err, ErrUnavailable) {
				continue
			}
			if err != nil {
				return camp.RunnerLoginReport{}, true, nil
			}
			if code == nil {
				continue
			}
			r.CodeAttempted = true
			if err = w.journal.save(r); err != nil {
				return camp.RunnerLoginReport{}, false, err
			}
			if err = w.retry(ctx, func() error { return w.client.ackCode(ctx, r, code) }); err != nil {
				return camp.RunnerLoginReport{State: "failed"}, false, nil
			}
			select {
			case codes <- code.Code:
			case <-ctx.Done():
				return camp.RunnerLoginReport{State: "failed"}, false, nil
			}
		}
	}
}
