package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

type turnRecord struct {
	Workspace camp.Workspace    `json:"workspace"`
	ClaimID   string            `json:"claim_id"`
	RequestID string            `json:"request_id"`
	StartKey  string            `json:"start_key"`
	FinishKey string            `json:"finish_key"`
	Phase     string            `json:"phase"`
	Seq       int64             `json:"seq"`
	Pending   *camp.StreamEvent `json:"pending,omitempty"`
	Delivered []string          `json:"delivered"`
	Outcome   string            `json:"outcome"`
	Output    string            `json:"output,omitempty"`
	Deadline  time.Time         `json:"deadline"`
}

type profileRecord struct {
	InstallationID string `json:"installation_id"`
	Epoch          int64  `json:"epoch"`
	Subject        string `json:"subject"`
	Revision       int64  `json:"revision"`
	Digest         string `json:"digest"`
}

func (c *checkout) saveJSON(name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 512<<10 {
		return ErrState
	}
	tmp := "journal-" + uuid.NewString()
	f, err := c.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrState
	}
	defer c.root.Remove(tmp)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil || closed != nil || c.root.Rename(tmp, name) != nil {
		return ErrState
	}
	dir, err := c.root.Open(".")
	if err != nil {
		return ErrState
	}
	defer dir.Close()
	return dir.Sync()
}

func (c *checkout) readJSON(name string, value any) error {
	raw, err := privateFile(filepath.Join(c.dir, name), 512<<10)
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, value) != nil {
		return ErrState
	}
	return nil
}

func (c *checkout) loadTurn() (*turnRecord, error) {
	if _, err := c.root.Lstat("turn.json"); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var r turnRecord
	if err := c.readJSON("turn.json", &r); err != nil {
		return nil, err
	}
	if !validWorkspace(r.Workspace) || !oneOf(r.Phase, "claim", "claimed", "starting", "running", "finishing", "uncertain") || r.Seq < 0 || len(r.Delivered) > 64 {
		return nil, ErrState
	}
	for _, id := range []string{r.ClaimID, r.StartKey, r.FinishKey} {
		if _, err := uuid.Parse(id); err != nil {
			return nil, ErrState
		}
	}
	if r.Phase != "claim" {
		if _, err := uuid.Parse(r.RequestID); err != nil {
			return nil, ErrState
		}
	}
	return &r, nil
}

func oneOf(value string, values ...string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func (c *checkout) saveTurn(r *turnRecord) error { return c.saveJSON("turn.json", r) }
func (c *checkout) clearTurn() error {
	if err := c.root.Remove("turn.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	return nil
}
func (c *checkout) holdTurn(r *turnRecord) error {
	return c.root.Rename("turn.json", "held-turn-"+r.ClaimID+".json")
}
func turnPath(r *turnRecord) string {
	return "/v1/runner/workspaces/" + r.Workspace.ID + "/requests/" + r.RequestID
}

func (w *Worker) turnAction(ctx context.Context, r *turnRecord, action string) (camp.Request, error) {
	input := camp.RequestAction{Action: action, LeaseToken: r.ClaimID}
	key := uuid.NewString()
	if action == "start" {
		key = r.StartKey
	}
	if action == "finish" || action == "reconcile" {
		key = r.FinishKey
		input.Outcome = r.Outcome
		input.Output = r.Output
		input.Evidence = "Runner observed provider terminal result"
	}
	var out camp.Request
	err := w.client.callKey(ctx, "POST", turnPath(r)+"/actions", key, input, &out)
	return out, err
}

func (w *Worker) profile(ctx context.Context, state camp.RunnerAccountState) (*localcli.App, string, profileRecord, error) {
	var remote camp.RunnerCredentials
	if err := w.client.call(ctx, "GET", "/v1/runner/credentials", nil, &remote); err != nil {
		return nil, "", profileRecord{}, err
	}
	if remote.InstallationID != state.InstallationID || remote.Epoch != state.Epoch || remote.Provider != state.Provider {
		return nil, "", profileRecord{}, ErrState
	}
	for _, name := range []string{"provider-home", "provider-home/profile", "provider-home/support"} {
		if err := w.checkout.root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, "", profileRecord{}, ErrState
		}
		info, err := w.checkout.root.Lstat(name)
		if err != nil || !info.IsDir() || !ownedPrivate(info) {
			return nil, "", profileRecord{}, ErrState
		}
	}
	home := filepath.Join(w.checkout.dir, "provider-home")
	root := filepath.Join(home, "profile")
	var saved profileRecord
	_, existsErr := w.checkout.root.Lstat("profile.json")
	if existsErr == nil {
		if err := w.checkout.readJSON("profile.json", &saved); err != nil {
			return nil, "", saved, err
		}
		if saved.InstallationID != state.InstallationID || saved.Epoch != state.Epoch || saved.Subject != remote.VerifiedSubject {
			return nil, "", saved, ErrState
		}
		if remote.Revision < saved.Revision {
			return nil, "", saved, ErrState
		}
		if remote.Revision == saved.Revision {
			current, err := localcli.CaptureCredentialSnapshot(state.Provider, saved.Subject, root)
			if err != nil {
				return nil, "", saved, err
			}
			hash := sha256.Sum256(current)
			if hex.EncodeToString(hash[:]) != saved.Digest {
				if err = w.client.call(ctx, "POST", "/v1/runner/credentials", camp.RunnerCredentialUpdate{ExpectedRevision: saved.Revision, Credentials: current}, &remote); err != nil {
					return nil, "", saved, err
				}
			}
		}
	}
	if existsErr != nil && !errors.Is(existsErr, os.ErrNotExist) {
		return nil, "", saved, ErrState
	}
	if existsErr != nil || remote.Revision != saved.Revision {
		if err := localcli.RestoreCredentialSnapshot(state.Provider, remote.VerifiedSubject, root, remote.Credentials); err != nil {
			return nil, "", saved, err
		}
	}
	raw, err := localcli.CaptureCredentialSnapshot(state.Provider, remote.VerifiedSubject, root)
	if err != nil {
		return nil, "", saved, err
	}
	hash := sha256.Sum256(raw)
	saved = profileRecord{InstallationID: state.InstallationID, Epoch: state.Epoch, Subject: remote.VerifiedSubject, Revision: remote.Revision, Digest: hex.EncodeToString(hash[:])}
	if err = w.checkout.saveJSON("profile.json", saved); err != nil {
		return nil, "", saved, err
	}
	if state.Provider == "Codex" {
		// Replace a prior entry atomically instead of following a possible symlink.
		file, e := os.CreateTemp(root, ".camp-config-")
		if e != nil {
			return nil, "", saved, ErrState
		}
		name := file.Name()
		_, e = file.WriteString("cli_auth_credentials_store = \"file\"\n")
		if e == nil {
			e = file.Sync()
		}
		closed := file.Close()
		if e != nil || closed != nil || os.Rename(name, filepath.Join(root, "config.toml")) != nil {
			_ = os.Remove(name)
			return nil, "", saved, ErrState
		}
	}
	configuration := config.Local{Home: home, Support: filepath.Join(home, "support"), Environment: localcli.IsolatedEnvironment(w.environment, home, root, state.Provider)}
	app := localcli.NewIsolated(configuration)
	if w.guarded {
		app = localcli.NewConfined(configuration)
	}
	return app, root, saved, nil
}

func (w *Worker) saveProfile(ctx context.Context, state camp.RunnerAccountState, root string, meta profileRecord) error {
	raw, err := localcli.CaptureCredentialSnapshot(state.Provider, meta.Subject, root)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	if digest == meta.Digest {
		return nil
	}
	var remote camp.RunnerCredentials
	if err = w.client.call(ctx, "POST", "/v1/runner/credentials", camp.RunnerCredentialUpdate{ExpectedRevision: meta.Revision, Credentials: raw}, &remote); err != nil {
		return err
	}
	meta.Revision, meta.Digest = remote.Revision, digest
	return w.checkout.saveJSON("profile.json", meta)
}

func (w *Worker) turnTick(ctx context.Context, state camp.RunnerAccountState, workspace camp.Workspace) error {
	r, err := w.checkout.loadTurn()
	if err != nil {
		return err
	}
	recovered := r != nil
	if r == nil {
		r = &turnRecord{Workspace: workspace, ClaimID: uuid.NewString(), StartKey: uuid.NewString(), FinishKey: uuid.NewString(), Phase: "claim", Delivered: []string{}}
		if err = w.checkout.saveTurn(r); err != nil {
			return err
		}
	}
	if r.Workspace.ID != workspace.ID {
		if err = w.checkout.holdTurn(r); err != nil {
			return err
		}
		return nil
	}
	if r.Phase == "claim" {
		var request *camp.Request
		err = w.client.call(ctx, "POST", "/v1/runner/workspaces/"+workspace.ID+"/requests/claim", map[string]string{"claim_id": r.ClaimID}, &request)
		if errors.Is(err, ErrRejected) {
			return w.checkout.clearTurn()
		}
		if err != nil {
			return err
		}
		if request == nil {
			return w.checkout.clearTurn()
		}
		if request.ExecutionWorkspaceID != workspace.ID || request.LeaseToken != r.ClaimID || request.State != "claimed" {
			return ErrState
		}
		r.RequestID, r.Phase = request.ID, "claimed"
		if err = w.checkout.saveTurn(r); err != nil {
			return err
		}
	}
	if recovered && oneOf(r.Phase, "running", "uncertain") {
		if r.Pending != nil {
			if err = w.flushEvent(ctx, r); err != nil && !errors.Is(err, ErrRejected) {
				return err
			}
		}
		var status camp.Request
		err = w.client.call(ctx, "GET", turnPath(r)+"/status", nil, &status)
		if errors.Is(err, ErrRejected) {
			return w.checkout.holdTurn(r)
		}
		if err != nil {
			return err
		}
		if oneOf(status.State, "completed", "failed", "cancelled") {
			return w.checkout.clearTurn()
		}
		return nil // Unknown provider outcome must be reviewed, never re-submitted.
	}
	if r.Phase == "finishing" {
		return w.finishTurn(ctx, r)
	}
	var work camp.RunnerTurn
	if err = w.client.call(ctx, "GET", turnPath(r), nil, &work); err != nil {
		if errors.Is(err, ErrRejected) {
			return w.checkout.holdTurn(r)
		}
		return err
	}
	app, root, meta, err := w.profile(ctx, state)
	if err != nil {
		return err
	}
	input := localcli.ConversationRequest{Session: localcli.Session{Provider: state.Provider, Root: root, CWD: w.checkout.workdir(workspace), SessionID: work.ProviderSessionID}, Prompt: work.Request.Prompt, Model: work.Request.Model, Effort: work.Request.Effort, Speed: work.Request.Speed, Mode: work.Request.Mode, Permissions: work.Request.Permissions, Context: work.Context}
	input.NewSession = input.Session.SessionID == ""
	if input.NewSession {
		input.Session.SessionID = uuid.NewString()
	}
	if input.Mode == "implementation" {
		input.ExecutionApproved = true
		approval, e := app.ExecutionContext(ctx, input.Session, input.Permissions)
		if e != nil {
			return e
		}
		input.ExecutionContext = approval.Fingerprint
	}
	if r.Phase == "claimed" {
		r.Phase = "starting"
		r.Deadline = time.Now().Add(30 * time.Minute)
		if err = w.checkout.saveTurn(r); err != nil {
			return err
		}
	}
	started, err := w.turnAction(ctx, r, "start")
	if err != nil {
		return err
	}
	if started.State != "started" || started.LeaseUntil == nil {
		return ErrState
	}
	if err = w.checkout.saveJSON("used-workspace-"+workspace.ID+".json", workspace); err != nil {
		return err
	}
	r.Phase = "running"
	if err = w.checkout.saveTurn(r); err != nil {
		return err
	}
	result, output, err := w.executeTurn(ctx, r, started, app, input)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		r.Phase = "uncertain"
		return w.checkout.saveTurn(r)
	}
	// The next profile load retries a rotated local snapshot before more work.
	// Credential persistence cannot turn a known provider result into an unknown one.
	_ = w.saveProfile(ctx, state, root, meta)
	r.Phase, r.Outcome = "finishing", result
	if work.Request.Kind == "planning" {
		r.Output = output
	}
	if err = w.checkout.saveTurn(r); err != nil {
		return err
	}
	return w.finishTurn(ctx, r)
}

func (w *Worker) finishTurn(ctx context.Context, r *turnRecord) error {
	_, err := w.turnAction(ctx, r, "finish")
	if errors.Is(err, ErrRejected) {
		var status camp.Request
		if e := w.client.call(ctx, "GET", turnPath(r)+"/status", nil, &status); e != nil {
			return e
		}
		if oneOf(status.State, "completed", "failed", "cancelled") {
			return w.checkout.clearTurn()
		}
		if status.State == "outcome_unknown" {
			_, err = w.turnAction(ctx, r, "reconcile")
		}
	}
	if err != nil {
		return err
	}
	return w.checkout.clearTurn()
}

func (w *Worker) flushEvent(ctx context.Context, r *turnRecord) error {
	if r.Pending == nil {
		return nil
	}
	var ack struct {
		Seq int64 `json:"seq"`
	}
	if err := w.client.call(ctx, "POST", turnPath(r)+"/events", r.Pending, &ack); err != nil {
		return err
	}
	if ack.Seq < r.Pending.Seq {
		return ErrState
	}
	r.Seq = r.Pending.Seq
	r.Pending = nil
	return w.checkout.saveTurn(r)
}

func (w *Worker) executeTurn(parent context.Context, r *turnRecord, request camp.Request, app *localcli.App, input localcli.ConversationRequest) (string, string, error) {
	ctx, cancel := context.WithDeadline(parent, r.Deadline)
	defer cancel()
	var mu sync.Mutex
	text, outcome := "", ""
	emitOne := func(event map[string]any) error {
		raw, err := json.Marshal(event)
		if err != nil {
			return ErrState
		}
		r.Pending = &camp.StreamEvent{Seq: r.Seq + 1, Event: raw}
		if err = w.checkout.saveTurn(r); err != nil {
			return err
		}
		return w.flushEvent(ctx, r)
	}
	emit := func(event map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		kind, _ := event["type"].(string)
		if kind == "done" {
			outcome = "completed"
		}
		if kind == "error" {
			outcome = "failed"
		}
		if kind == "boundary" {
			text = ""
		}
		if kind != "text" {
			return emitOne(event)
		}
		value, _ := event["text"].(string)
		delta := value
		if strings.HasPrefix(value, text) {
			delta = value[len(text):]
		} else {
			if err := emitOne(map[string]any{"type": "reset"}); err != nil {
				return err
			}
		}
		text = value
		for len(delta) > 0 {
			n := len(delta)
			if n > 32<<10 {
				n = 32 << 10
				for !utf8.RuneStart(delta[n]) {
					n--
				}
			}
			if err := emitOne(map[string]any{"type": "delta", "text": delta[:n]}); err != nil {
				return err
			}
			delta = delta[n:]
		}
		return nil
	}
	controls := make(chan map[string]any, 16)
	done := make(chan error, 1)
	go func() { done <- app.RunConversation(ctx, input, controls, emit) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	interval := time.Second
	if w.interval < interval {
		interval = w.interval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	lease := *request.LeaseUntil
	for {
		select {
		case err := <-done:
			joined = true
			if parent.Err() != nil {
				return "", "", parent.Err()
			}
			mu.Lock()
			result, output := outcome, text
			mu.Unlock()
			if err != nil || result == "" {
				return "", "", ErrState
			}
			return result, output, nil
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-tick.C:
			heartbeatCtx, stop := context.WithDeadline(ctx, lease)
			current, err := w.turnAction(heartbeatCtx, r, "heartbeat")
			stop()
			if err != nil {
				if errors.Is(err, ErrUnavailable) && time.Now().Before(lease) {
					continue
				}
				return "", "", err
			}
			if current.State == "cancel_requested" {
				cancel()
				<-done
				joined = true
				return "cancelled", "", nil
			}
			if current.LeaseUntil == nil {
				return "", "", ErrState
			}
			lease = *current.LeaseUntil
			var waiting []camp.RunnerControl
			if err = w.client.call(ctx, "GET", turnPath(r)+"/controls", nil, &waiting); err != nil {
				if errors.Is(err, ErrUnavailable) {
					continue
				}
				return "", "", err
			}
			for _, control := range waiting {
				mu.Lock()
				already := false
				for _, id := range r.Delivered {
					already = already || id == control.ID
				}
				if !already {
					r.Delivered = append(r.Delivered, control.ID)
					err = w.checkout.saveTurn(r)
				}
				mu.Unlock()
				if err != nil {
					return "", "", err
				}
				if already {
					continue
				}
				select {
				case controls <- control.Control:
				case <-ctx.Done():
					return "", "", ctx.Err()
				}
			}
		}
	}
}

func (c *checkout) eraseRuntime() error {
	entries, err := c.entries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "provider-home" || entry.Name() == "profile.json" || entry.Name() == "turn.json" || strings.HasPrefix(entry.Name(), "held-turn-") {
			if err = c.root.RemoveAll(entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}
