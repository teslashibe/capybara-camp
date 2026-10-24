package localcli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/providerlogin"
)

var ErrProviderLogin = errors.New("The provider did not complete sign-in. Start a new sign-in attempt")

type ProviderLoginChallenge struct {
	URL      string
	UserCode string
}
type ProviderLoginResult struct {
	Subject     string
	Credentials json.RawMessage
}

// LoginProvider always starts with an empty private home. The caller validates
// and publishes challenges, and may relay one Claude authorization code. No
// shell command, project, provider password or existing Mac profile is accepted.
func (a *App) LoginProvider(ctx context.Context, provider, base string, challenge func(ProviderLoginChallenge) error, codes <-chan string) (result ProviderLoginResult, resultErr error) {
	if err := ctx.Err(); err != nil {
		return ProviderLoginResult{}, err
	}
	if provider != "Codex" && provider != "Claude" || challenge == nil || !filepath.IsAbs(base) {
		return ProviderLoginResult{}, ErrProviderLogin
	}
	info, err := os.Lstat(base)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ProviderLoginResult{}, errors.New("The login workspace must be a private directory")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return ProviderLoginResult{}, errors.New("The login workspace must belong to this worker")
	}
	home, err := os.MkdirTemp(base, "provider-login-")
	if err != nil {
		return ProviderLoginResult{}, errors.New("Could not prepare the login workspace")
	}
	defer func() {
		if os.RemoveAll(home) != nil {
			result = ProviderLoginResult{}
			resultErr = errors.New("Could not remove the temporary login profile")
		}
	}()
	root := filepath.Join(home, "profile")
	if err = os.Mkdir(root, 0700); err != nil {
		return ProviderLoginResult{}, ErrProviderLogin
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	validatedChallenge := func(c ProviderLoginChallenge) error {
		if !providerlogin.ValidChallenge(provider, c.URL, c.UserCode) {
			return ErrProviderLogin
		}
		return challenge(c)
	}
	env := loginEnvironment(a.Config.Environment, home, root, provider)
	candidate := &App{Config: config.Local{Home: home, Environment: env}, commandPath: a.commandPath, confined: a.confined, isolated: a.isolated}
	s := Session{Provider: provider, Root: root, CWD: home}
	if provider == "Codex" {
		if err = os.WriteFile(filepath.Join(root, "config.toml"), []byte("cli_auth_credentials_store = \"file\"\n"), 0600); err != nil {
			return ProviderLoginResult{}, ErrProviderLogin
		}
		connection, err := candidate.connect(ctx, s, []string{"codex", "app-server"}, false, env)
		if err != nil {
			return ProviderLoginResult{}, ErrProviderLogin
		}
		defer connection.Close()
		if err = codexProviderLogin(connection, validatedChallenge); err != nil {
			if ctx.Err() != nil {
				return ProviderLoginResult{}, ctx.Err()
			}
			return ProviderLoginResult{}, err
		}
		raw, err := privateLoginFile(root, "auth.json")
		if err != nil {
			return ProviderLoginResult{}, err
		}
		subject, err := codexLoginSubject(raw)
		if err != nil {
			return ProviderLoginResult{}, err
		}
		snapshot, _ := json.Marshal(map[string]any{"version": 1, "provider": provider, "files": map[string]json.RawMessage{"auth.json": raw}})
		return boundedLoginResult(subject, snapshot)
	}
	if err = candidate.claudeProviderLogin(ctx, s, env, validatedChallenge, codes); err != nil {
		return ProviderLoginResult{}, err
	}
	status, err := candidate.probeAt(ctx, s, []string{"claude", "--safe-mode", "auth", "status", "--json"}, 15*time.Second, home)
	if err != nil {
		return ProviderLoginResult{}, ErrProviderLogin
	}
	credentials, err := privateLoginFile(root, ".credentials.json")
	if err != nil {
		return ProviderLoginResult{}, err
	}
	// Claude uses the explicit config directory on Linux; older builds may keep
	// the account descriptor in the private HOME. Read only these fixed paths.
	account, err := privateLoginFile(root, ".claude.json")
	if err != nil {
		account, err = privateLoginFile(home, ".claude.json")
	}
	if err != nil {
		return ProviderLoginResult{}, err
	}
	subject, profile, err := claudeLoginSubject(status, credentials, account)
	if err != nil {
		return ProviderLoginResult{}, err
	}
	snapshot, _ := json.Marshal(map[string]any{"version": 1, "provider": provider, "files": map[string]json.RawMessage{".credentials.json": credentials, ".claude.json": profile}})
	return boundedLoginResult(subject, snapshot)
}

func boundedLoginResult(subject string, snapshot []byte) (ProviderLoginResult, error) {
	if len(subject) > 256 || len(snapshot) > 256<<10 {
		return ProviderLoginResult{}, ErrProviderLogin
	}
	return ProviderLoginResult{Subject: subject, Credentials: snapshot}, nil
}

func loginEnvironment(source []string, home, root, provider string) []string {
	env := map[string]string{"HOME": home, "TMPDIR": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"), "XDG_CACHE_HOME": filepath.Join(home, "cache"), "PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin", "TERM": "dumb", "NO_COLOR": "1", "BROWSER": "/usr/bin/false"}
	for _, entry := range source {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case "PATH", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY":
			env[key] = value
		}
	}
	if provider == "Codex" {
		env["CODEX_HOME"] = root
	} else {
		env["CLAUDE_CONFIG_DIR"] = root
		env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func codexProviderLogin(c turnConnection, challenge func(ProviderLoginChallenge) error) error {
	if _, err := c.RPC("initialize", map[string]any{"clientInfo": map[string]any{"name": "capybara-camp-login", "version": "1"}, "capabilities": map[string]any{}}, 15*time.Second); err != nil {
		return ErrProviderLogin
	}
	if err := c.Write(map[string]any{"method": "initialized"}); err != nil {
		return ErrProviderLogin
	}
	started, err := c.RPC("account/login/start", map[string]any{"type": "chatgptDeviceCode"}, 30*time.Second)
	if err != nil {
		return ErrProviderLogin
	}
	id := str(started["loginId"])
	if !validSessionID(id) || str(started["type"]) != "chatgptDeviceCode" {
		return ErrProviderLogin
	}
	if err = challenge(ProviderLoginChallenge{URL: str(started["verificationUrl"]), UserCode: str(started["userCode"])}); err != nil {
		return err
	}
	for {
		event, err := c.Next()
		if err != nil {
			return ErrProviderLogin
		}
		if event == nil {
			continue
		}
		if str(event["method"]) != "account/login/completed" {
			continue
		}
		result := object(event["params"])
		if str(result["loginId"]) != id {
			continue
		}
		if result["success"] != true {
			return ErrProviderLogin
		}
		status, err := c.RPC("account/read", map[string]any{"refreshToken": false}, 15*time.Second)
		if err != nil || str(object(status["account"])["type"]) != "chatgpt" {
			return ErrProviderLogin
		}
		return nil
	}
}

func privateLoginFile(root, name string) (json.RawMessage, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrProviderLogin
	}
	directory, err := os.OpenRoot(root)
	if err != nil {
		return nil, ErrProviderLogin
	}
	defer directory.Close()
	file, err := directory.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrProviderLogin
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<10 {
		return nil, ErrProviderLogin
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return nil, ErrProviderLogin
	}
	if err = file.Chmod(0600); err != nil {
		return nil, ErrProviderLogin
	}
	raw, err := io.ReadAll(io.LimitReader(file, 128<<10+1))
	if err != nil || len(raw) > 128<<10 || !json.Valid(raw) {
		return nil, ErrProviderLogin
	}
	return raw, nil
}
func identityPart(s string) bool {
	return s != "" && len(s) <= 120 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\r\n\x00:")
}
func codexLoginSubject(raw []byte) (string, error) {
	var d map[string]any
	if json.Unmarshal(raw, &d) != nil || str(d["OPENAI_API_KEY"]) != "" {
		return "", ErrProviderLogin
	}
	tokens := object(d["tokens"])
	id := str(tokens["account_id"])
	jwt := strings.Split(str(tokens["id_token"]), ".")
	if !identityPart(id) || len(jwt) != 3 || str(tokens["access_token"]) == "" || str(tokens["refresh_token"]) == "" {
		return "", ErrProviderLogin
	}
	payload, err := base64.RawURLEncoding.DecodeString(jwt[1])
	if err != nil || len(payload) > 32768 {
		return "", ErrProviderLogin
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return "", ErrProviderLogin
	}
	subject := str(claims["sub"])
	claimAccount := str(object(claims["https://api.openai.com/auth"])["chatgpt_account_id"])
	if !identityPart(subject) || claimAccount != "" && claimAccount != id {
		return "", ErrProviderLogin
	}
	// This is identity extraction AFTER the fresh official CLI login and account
	// check succeed. It is not standalone JWT validation for an untrusted file.
	return "codex:" + id + ":" + subject, nil
}
func claudeLoginSubject(status, credentials, profile []byte) (string, json.RawMessage, error) {
	var s, d, p map[string]any
	if json.Unmarshal(status, &s) != nil || s["loggedIn"] != true || str(s["authMethod"]) != "claude.ai" || str(s["apiProvider"]) != "firstParty" || json.Unmarshal(credentials, &d) != nil || json.Unmarshal(profile, &p) != nil {
		return "", nil, ErrProviderLogin
	}
	oauth := object(d["claudeAiOauth"])
	account := object(p["oauthAccount"])
	id, org := str(account["accountUuid"]), str(account["organizationUuid"])
	if !identityPart(id) || !identityPart(org) || str(oauth["accessToken"]) == "" || str(oauth["refreshToken"]) == "" {
		return "", nil, ErrProviderLogin
	}
	if got := str(s["orgId"]); got != "" && got != org {
		return "", nil, ErrProviderLogin
	}
	if got := str(s["email"]); got != "" && got != str(account["emailAddress"]) {
		return "", nil, ErrProviderLogin
	}
	minimal, _ := json.Marshal(map[string]any{"oauthAccount": account})
	return "claude:" + org + ":" + id, minimal, nil
}

var loginURLPattern = regexp.MustCompile(`https://[^\s\x1b]+`)

type loginOutput struct {
	mu      sync.Mutex
	pending []byte
	urls    chan string
	seen    map[string]bool
	count   int
}

func (w *loginOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.count += len(p)
	if w.count > 128<<10 {
		return 0, errors.New("Provider login output exceeded its limit")
	}
	w.pending = append(w.pending, p...)
	if len(w.pending) > 16384 {
		return 0, errors.New("Provider login line exceeded its limit")
	}
	for {
		at := bytes.IndexByte(w.pending, '\n')
		if at < 0 {
			break
		}
		line := w.pending[:at]
		w.pending = w.pending[at+1:]
		for _, raw := range loginURLPattern.FindAll(line, -1) {
			value := strings.TrimRight(string(raw), "\r")
			parsed, err := url.Parse(value)
			if err != nil || !strings.HasSuffix(parsed.Path, "/oauth/authorize") {
				continue
			}
			if len(value) > 4096 || w.seen[value] {
				continue
			}
			w.seen[value] = true
			select {
			case w.urls <- value:
			default:
				return 0, errors.New("Provider login returned too many URLs")
			}
		}
	}
	return len(p), nil
}
func (a *App) claudeProviderLogin(ctx context.Context, s Session, env []string, challenge func(ProviderLoginChallenge) error, codes <-chan string) error {
	lookup := a.commandPath
	if lookup == nil {
		lookup = executable
	}
	path, err := lookup("claude", env)
	if err != nil {
		return ErrProviderLogin
	}
	path, commandArgs, err := a.providerCommand(path, []string{"claude", "--safe-mode", "auth", "login", "--claudeai"}, s.CWD, true)
	if err != nil {
		return err
	}
	command := exec.Command(path, commandArgs...)
	command.Dir = s.CWD
	command.Env = env
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := command.StdinPipe()
	if err != nil {
		return ErrProviderLogin
	}
	output := &loginOutput{urls: make(chan string, 4), seen: map[string]bool{}}
	stdout, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		return ErrProviderLogin
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		input.Close()
		stdout.Close()
		return ErrProviderLogin
	}
	if err = command.Start(); err != nil {
		input.Close()
		stdout.Close()
		stderr.Close()
		return ErrProviderLogin
	}
	var readers sync.WaitGroup
	readers.Add(2)
	for _, stream := range []io.ReadCloser{stdout, stderr} {
		go func(stream io.ReadCloser) {
			defer readers.Done()
			defer stream.Close()
			if _, err := io.Copy(output, stream); err != nil {
				_ = killGroup(command.Process.Pid)
			}
		}(stream)
	}
	done := make(chan error, 1)
	var processMu sync.Mutex
	reaped := false
	go func() {
		readers.Wait()
		processMu.Lock()
		_ = killGroup(command.Process.Pid)
		err := command.Wait()
		reaped = true
		processMu.Unlock()
		done <- err
	}()
	stopped := false
	defer func() {
		input.Close()
		if !stopped {
			processMu.Lock()
			if !reaped {
				_ = killGroup(command.Process.Pid)
			}
			processMu.Unlock()
			<-done
		}
	}()
	sent := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			stopped = true
			if err != nil {
				return ErrProviderLogin
			}
			return nil
		case raw := <-output.urls:
			if err = challenge(ProviderLoginChallenge{URL: raw}); err != nil {
				return err
			}
		case code, ok := <-codes:
			if !ok {
				codes = nil
				continue
			}
			if sent || len(code) < 8 || len(code) > 4096 || strings.ContainsAny(code, "\r\n\x00") {
				return ErrProviderLogin
			}
			sent = true
			if err = writeLoginCode(ctx, input, code); err != nil {
				return ErrProviderLogin
			}
		}
	}
}

func writeLoginCode(ctx context.Context, input io.WriteCloser, code string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := fmt.Fprintln(input, code); done <- err }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		input.Close()
		<-done
		return ctx.Err()
	}
}
