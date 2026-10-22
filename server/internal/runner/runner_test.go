package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/httpapi"
	"github.com/teslashibe/capybara-camp/server/internal/localcli"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

type fixture struct {
	store   *camp.Store
	owner   auth.Actor
	account camp.ProviderAccount
	grant   camp.RunnerGrant
	login   camp.ProviderLogin
	worker  *Worker
	api     *httpapi.API
}

func workerFixture(t *testing.T, provider string, wrap func(http.Handler) http.Handler) fixture {
	t.Helper()
	pool := testdb.Open(t)
	s, err := camp.NewWithProviderKey(pool, "test-receipts", bytes.Repeat([]byte{44}, 32))
	if err != nil {
		t.Fatal(err)
	}
	owner := auth.Actor{UserID: uuid.NewString(), Email: "owner@example.com", Name: "Owner"}
	ctx := context.Background()
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, owner.UserID, owner.Email, owner.Name); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateProviderAccount(ctx, owner, uuid.NewString(), camp.ProviderAccountInput{Provider: provider, Label: "Fixture"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateRunnerGrant(ctx, owner, a.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.BeginProviderLogin(ctx, owner, a.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	api := &httpapi.API{Pool: pool, Auth: auth.New(pool, "fixture-secret", "https://camp.example", nil), Camps: s, Origin: "https://camp.example"}
	handler := api.Handler()
	if wrap != nil {
		handler = wrap(handler)
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	grantFile := filepath.Join(t.TempDir(), "grant")
	if err = os.WriteFile(grantFile, []byte(g.Token), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := New(config.Runner{Origin: server.URL, GrantFile: grantFile, StateDir: privateTemp(t)}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	w.interval = 10 * time.Millisecond
	return fixture{s, owner, a, g, l, w, api}
}

func startWorker(t *testing.T, w *Worker) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("worker did not stop during cleanup")
		}
	})
	return cancel, done
}

func eventually(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become ready")
}

func stopWorker(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not join")
	}
}

func testChallenge(provider string) localcli.ProviderLoginChallenge {
	if provider == "Codex" {
		return localcli.ProviderLoginChallenge{URL: "https://auth.openai.com/codex/device", UserCode: "TEST-1234"}
	}
	return localcli.ProviderLoginChallenge{URL: "https://claude.com/cai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&scope=user%3Ainference&state=fixture-login-state&code_challenge=" + strings.Repeat("a", 43) + "&code_challenge_method=S256"}
}

func TestWorkerProviderLoginAndDisconnect(t *testing.T) {
	for _, provider := range []string{"Codex", "Claude"} {
		t.Run(provider, func(t *testing.T) {
			var acknowledgments atomic.Int32
			f := workerFixture(t, provider, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/code/ack") && acknowledgments.Add(1) == 1 {
						next.ServeHTTP(httptest.NewRecorder(), r)
						w.WriteHeader(503) // Persisted erasure, lost acknowledgment.
						return
					}
					next.ServeHTTP(w, r)
				})
			})
			var calls atomic.Int32
			var stopped atomic.Bool
			f.worker.login = func(ctx context.Context, p, dir string, challenge func(localcli.ProviderLoginChallenge) error, codes <-chan string) (localcli.ProviderLoginResult, error) {
				calls.Add(1)
				defer stopped.Store(true)
				if p != provider || dir != f.worker.stateDir {
					return localcli.ProviderLoginResult{}, ErrState
				}
				if err := challenge(testChallenge(p)); err != nil {
					return localcli.ProviderLoginResult{}, err
				}
				if provider == "Claude" {
					select {
					case code := <-codes:
						if code != "fixture-claude-code" {
							return localcli.ProviderLoginResult{}, ErrState
						}
					case <-ctx.Done():
						return localcli.ProviderLoginResult{}, ctx.Err()
					}
				}
				return localcli.ProviderLoginResult{Subject: "fixture-subject", Credentials: json.RawMessage(`{"test":"credential"}`)}, nil
			}
			cancel, done := startWorker(t, f.worker)
			ctx := context.Background()
			if provider == "Claude" {
				eventually(t, func() bool {
					l, err := f.store.ProviderLogin(ctx, f.owner, f.account.ID, f.login.ID)
					return err == nil && l.State == "waiting"
				})
				if _, err := f.store.SubmitProviderLoginCode(ctx, f.owner, f.account.ID, f.login.ID, uuid.NewString(), camp.ProviderCodeInput{Code: "fixture-claude-code"}); err != nil {
					t.Fatal(err)
				}
			}
			eventually(t, func() bool {
				accounts, err := f.store.ProviderAccounts(ctx, f.owner)
				return err == nil && accounts[0].State == "connected"
			})
			if calls.Load() != 1 || !stopped.Load() {
				t.Fatal("provider did not finish exactly once")
			}
			if provider == "Claude" && acknowledgments.Load() != 2 {
				t.Fatal("lost code acknowledgment was not recovered")
			}
			if _, err := f.store.DisconnectProviderAccount(ctx, f.owner, f.account.ID, uuid.NewString()); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("disconnect did not finish")
			}
			cancel()
			accounts, err := f.store.ProviderAccounts(ctx, f.owner)
			if err != nil || accounts[0].State != "disconnected" {
				t.Fatal("erasure not acknowledged", err)
			}
			if _, err := os.Stat(filepath.Join(f.worker.stateDir, "login.enc")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("journal retained after erasure")
			}
		})
	}
}

func TestWorkerRecoversStartedAndUncertainFinalReport(t *testing.T) {
	for _, phase := range []string{"prepared", "started", "finishing", "committed"} {
		t.Run(phase, func(t *testing.T) {
			var requests atomic.Int32
			var payloadMu sync.Mutex
			var payloads [][]byte
			f := workerFixture(t, "Codex", func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/report") {
						data, _ := io.ReadAll(r.Body)
						r.Body = io.NopCloser(bytes.NewReader(data))
						payloadMu.Lock()
						payloads = append(payloads, data)
						payloadMu.Unlock()
						if requests.Add(1) == 1 {
							if phase == "committed" {
								next.ServeHTTP(httptest.NewRecorder(), r)
							}
							w.WriteHeader(503)
							return
						}
					}
					next.ServeHTTP(w, r)
				})
			})
			r := &record{InstallationID: f.account.InstallationID, Provider: "Codex", Epoch: 1, AttemptID: f.login.ID, ClaimID: uuid.NewString(), DeliveryID: uuid.NewString(), Phase: phase}
			if phase == "committed" {
				r.Phase = "finishing"
			}
			if _, err := f.store.ClaimRunnerLogin(context.Background(), f.grant.Token, f.login.ID, r.ClaimID); err != nil {
				t.Fatal(err)
			}
			if r.Phase == "finishing" {
				r.Report = &camp.RunnerLoginReport{State: "connected", VerifiedSubject: "saved-subject", Credentials: json.RawMessage(`{"saved":"private-proof"}`)}
			}
			if err := f.worker.journal.save(r); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			f.worker.login = func(context.Context, string, string, func(localcli.ProviderLoginChallenge) error, <-chan string) (localcli.ProviderLoginResult, error) {
				calls.Add(1)
				return localcli.ProviderLoginResult{}, ErrState
			}
			cancel, done := startWorker(t, f.worker)
			want := "failed"
			if phase == "finishing" || phase == "committed" {
				want = "connected"
			}
			eventually(t, func() bool {
				l, err := f.store.ProviderLogin(context.Background(), f.owner, f.account.ID, f.login.ID)
				return err == nil && l.State == want
			})
			stopWorker(t, cancel, done)
			wantCalls := int32(0)
			if phase == "prepared" {
				wantCalls = 1
			}
			wantReports := int32(2)
			if phase == "committed" {
				wantReports = 1
			}
			if calls.Load() != wantCalls || requests.Load() != wantReports {
				t.Fatal("unexpected provider or report replay", calls.Load(), requests.Load())
			}
			payloadMu.Lock()
			if len(payloads) > 1 && !bytes.Equal(payloads[0], payloads[1]) {
				t.Error("report payload changed on retry")
			}
			payloadMu.Unlock()
		})
	}
}

func TestWorkerCancelsProviderBeforeErasure(t *testing.T) {
	var stopped atomic.Bool
	f := workerFixture(t, "Claude", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/runner/erased" && !stopped.Load() {
				t.Error("erasure acknowledged before provider stopped")
			}
			next.ServeHTTP(w, r)
		})
	})
	f.worker.login = func(ctx context.Context, _ string, _ string, challenge func(localcli.ProviderLoginChallenge) error, _ <-chan string) (localcli.ProviderLoginResult, error) {
		defer stopped.Store(true)
		if err := challenge(testChallenge("Claude")); err != nil {
			return localcli.ProviderLoginResult{}, err
		}
		<-ctx.Done()
		return localcli.ProviderLoginResult{}, ctx.Err()
	}
	cancel, done := startWorker(t, f.worker)
	eventually(t, func() bool {
		l, err := f.store.ProviderLogin(context.Background(), f.owner, f.account.ID, f.login.ID)
		return err == nil && l.State == "waiting"
	})
	if _, err := f.store.DisconnectProviderAccount(context.Background(), f.owner, f.account.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil || !stopped.Load() {
			t.Fatal("disconnect failed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not stop worker")
	}
	cancel()
}

func TestWorkerShutdownJoinsProvider(t *testing.T) {
	f := workerFixture(t, "Codex", nil)
	started, stopped := make(chan struct{}), make(chan struct{})
	f.worker.login = func(ctx context.Context, _, _ string, _ func(localcli.ProviderLoginChallenge) error, _ <-chan string) (localcli.ProviderLoginResult, error) {
		close(started)
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		close(stopped)
		return localcli.ProviderLoginResult{}, ctx.Err()
	}
	cancel, done := startWorker(t, f.worker)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start")
	}
	stopWorker(t, cancel, done)
	select {
	case <-stopped:
	default:
		t.Fatal("provider was orphaned")
	}
	r, err := f.worker.journal.load()
	if err != nil || r == nil || r.Phase != "started" {
		t.Fatal("shutdown lost recovery fence", err)
	}
}
