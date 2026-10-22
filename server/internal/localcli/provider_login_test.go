package localcli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/config"
)

func fixtureLoginURL() string {
	q := url.Values{"code": {"true"}, "client_id": {"9d1c250a-e61b-44d9-88ed-5944d1962f5e"}, "response_type": {"code"}, "redirect_uri": {"https://platform.claude.com/oauth/code/callback"}, "state": {"0123456789abcdef0123456789abcdef"}, "code_challenge": {strings.Repeat("c", 43)}, "code_challenge_method": {"S256"}, "scope": {"user:inference"}}
	return "https://claude.com/cai/oauth/authorize?" + q.Encode()
}
func TestProviderLoginEnvironmentIsEmptyAndProfileBound(t *testing.T) {
	input := []string{"HOME=/original", "CODEX_HOME=/shared", "CLAUDE_CONFIG_DIR=/other", "OPENAI_API_KEY=must-not-inherit", "CLAUDE_CODE_OAUTH_TOKEN=must-not-inherit", "CAMP_AUTH_SECRET=server-only", "KUBECONFIG=/cluster-admin", "PATH=/usr/bin:/bin"}
	for _, provider := range []string{"Codex", "Claude"} {
		got := strings.Join(loginEnvironment(input, "/isolated/home", "/isolated/profile", provider), "\n")
		for _, bad := range []string{"/original", "/shared", "/other", "must-not-inherit", "server-only", "KUBECONFIG"} {
			if strings.Contains(got, bad) {
				t.Fatal("inherited unrelated login data")
			}
		}
		if !strings.Contains(got, "HOME=/isolated/home") || !strings.Contains(got, "BROWSER=/usr/bin/false") {
			t.Fatal(got)
		}
	}
}
func TestCodexLoginUsesBoundDeviceFlowOnly(t *testing.T) {
	f := &turnFixture{events: []map[string]any{
		{"method": "account/login/completed", "params": map[string]any{"loginId": "different", "success": true}},
		{"method": "account/login/completed", "params": map[string]any{"loginId": "our-login", "success": true}},
	}}
	f.rpc = func(method string, _ map[string]any) (map[string]any, error) {
		switch method {
		case "account/login/start":
			return map[string]any{"type": "chatgptDeviceCode", "loginId": "our-login", "verificationUrl": "https://auth.openai.com/codex/device", "userCode": "ABCD-1234"}, nil
		case "account/read":
			return map[string]any{"account": map[string]any{"type": "chatgpt"}}, nil
		default:
			return map[string]any{}, nil
		}
	}
	count := 0
	if err := codexProviderLogin(f, func(c ProviderLoginChallenge) error {
		count++
		if c.UserCode != "ABCD-1234" {
			t.Fatal(c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal(count)
	}
	calls := []string{}
	for _, call := range f.calls {
		calls = append(calls, call.method)
	}
	if !reflect.DeepEqual(calls, []string{"initialize", "account/login/start", "account/read"}) {
		t.Fatal(calls)
	}
	if f.calls[1].params["type"] != "chatgptDeviceCode" {
		t.Fatal("login used another authentication mode")
	}
	f.events = []map[string]any{{"method": "account/login/completed", "params": map[string]any{"loginId": "our-login", "success": false, "error": "private-provider-detail"}}}
	if err := codexProviderLogin(f, func(ProviderLoginChallenge) error { return nil }); !errors.Is(err, ErrProviderLogin) || strings.Contains(err.Error(), "private-provider") {
		t.Fatal(err)
	}
}
func TestProviderCredentialIdentityAndBoundedFiles(t *testing.T) {
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user-id","https://api.openai.com/auth":{"chatgpt_account_id":"account-id"}}`))
	raw := []byte(`{"tokens":{"account_id":"account-id","id_token":"header.` + claims + `.signature","access_token":"fixture-access","refresh_token":"fixture-refresh"}}`)
	if subject, err := codexLoginSubject(raw); err != nil || subject != "codex:account-id:user-id" {
		t.Fatal(subject, err)
	}
	if _, err := codexLoginSubject([]byte(strings.ReplaceAll(string(raw), `"account_id":"account-id"`, `"account_id":"wrong-account"`))); !errors.Is(err, ErrProviderLogin) {
		t.Fatal("identity mismatch accepted")
	}
	status := []byte(`{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","orgId":"org-id","email":"person@example.com"}`)
	credentials := []byte(`{"claudeAiOauth":{"accessToken":"fixture-access","refreshToken":"fixture-refresh"}}`)
	profile := []byte(`{"oauthAccount":{"accountUuid":"person-id","organizationUuid":"org-id","emailAddress":"person@example.com"},"arbitraryCommand":"not exported"}`)
	subject, minimal, err := claudeLoginSubject(status, credentials, profile)
	if err != nil || subject != "claude:org-id:person-id" || strings.Contains(string(minimal), "arbitraryCommand") {
		t.Fatal(subject, string(minimal), err)
	}
	if _, _, err = claudeLoginSubject([]byte(strings.ReplaceAll(string(status), "org-id", "foreign-org")), credentials, profile); !errors.Is(err, ErrProviderLogin) {
		t.Fatal("organization mismatch accepted")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "state.json"), string(raw))
	if _, err = privateLoginFile(root, "state.json"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(root, "state.json"), filepath.Join(root, "link.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = privateLoginFile(root, "link.json"); !errors.Is(err, ErrProviderLogin) {
		t.Fatal("symlink credential accepted")
	}
	write(t, filepath.Join(root, "large.json"), strings.Repeat("x", 129<<10))
	if _, err = privateLoginFile(root, "large.json"); !errors.Is(err, ErrProviderLogin) {
		t.Fatal("oversized credential accepted")
	}
}
func TestClaudeLoginCapturesURLAndOneCodeInPrivateProfile(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fixture-provider")
	url := fixtureLoginURL()
	body := `#!/bin/sh
if [ "$HOME" = /original ] || [ -n "$OPENAI_API_KEY" ] || [ -n "$CAMP_AUTH_SECRET" ]; then exit 9; fi
if [ "$3" = status ]; then
  printf '%s\n' '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","orgId":"org-id","email":"person@example.com"}'
  exit 0
fi
printf '%s\n' '` + url + `'
IFS= read -r code
[ "$code" = fixture-code#fixture-state ] || exit 8
printf '%s\n' '{"claudeAiOauth":{"accessToken":"fixture-access","refreshToken":"fixture-refresh"}}' > "$CLAUDE_CONFIG_DIR/.credentials.json"
printf '%s\n' '{"oauthAccount":{"accountUuid":"person-id","organizationUuid":"org-id","emailAddress":"person@example.com"}}' > "$CLAUDE_CONFIG_DIR/.claude.json"
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	a := fixture(t)
	a.Config.Environment = append(a.Config.Environment, "HOME=/original", "OPENAI_API_KEY=not-inherited", "CAMP_AUTH_SECRET=not-inherited")
	a.commandPath = func(string, []string) (string, error) { return script, nil }
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	codes := make(chan string, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := a.LoginProvider(ctx, "Claude", base, func(c ProviderLoginChallenge) error {
		if c.URL != url {
			t.Fatal("wrong login URL")
		}
		codes <- "fixture-code#fixture-state"
		return nil
	}, codes)
	if err != nil || result.Subject != "claude:org-id:person-id" {
		t.Fatal(result.Subject, err)
	}
	var snapshot map[string]any
	if err = json.Unmarshal(result.Credentials, &snapshot); err != nil || snapshot["provider"] != "Claude" {
		t.Fatal(err)
	}
	files, err := os.ReadDir(base)
	if err != nil || len(files) != 0 {
		t.Fatal("temporary profile retained", err)
	}
}
func TestClaudeLoginCancellationAndUnsafeURLCleanUp(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "unsafe-url"}[unsafe], func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "fixture-provider")
			loginURL := fixtureLoginURL()
			if unsafe {
				loginURL = "https://attacker.example/authorize"
			}
			if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '"+loginURL+"'\nwhile :; do sleep 1; done\n"), 0700); err != nil {
				t.Fatal(err)
			}
			a := fixture(t)
			a.commandPath = func(string, []string) (string, error) { return script, nil }
			base := t.TempDir()
			os.Chmod(base, 0700)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			called := false
			start := time.Now()
			_, err := a.LoginProvider(ctx, "Claude", base, func(ProviderLoginChallenge) error { called = true; return nil }, nil)
			if err == nil || time.Since(start) > 2*time.Second {
				t.Fatal("login cancellation unbounded", err)
			}
			if unsafe && called {
				t.Fatal("unsafe URL escaped validation")
			}
			files, _ := os.ReadDir(base)
			if len(files) != 0 {
				t.Fatal("cancelled profile retained")
			}
		})
	}
}

func TestProviderLoginProcess(t *testing.T) {
	if os.Getenv("CAMP_PROVIDER_LOGIN_HELPER") != "1" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var request map[string]any
		if decoder.Decode(&request) != nil {
			os.Exit(0)
		}
		id, method := request["id"], str(request["method"])
		switch method {
		case "initialize":
			encoder.Encode(map[string]any{"id": id, "result": map[string]any{}})
		case "account/login/start":
			root := os.Getenv("CODEX_HOME")
			if root == "" || os.Getenv("OPENAI_API_KEY") != "" {
				os.Exit(9)
			}
			claims := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user-id","https://api.openai.com/auth":{"chatgpt_account_id":"account-id"}}`))
			raw := `{"tokens":{"account_id":"account-id","id_token":"header.` + claims + `.signature","access_token":"fixture-access","refresh_token":"fixture-refresh"}}`
			if os.WriteFile(filepath.Join(root, "auth.json"), []byte(raw), 0600) != nil {
				os.Exit(10)
			}
			encoder.Encode(map[string]any{"id": id, "result": map[string]any{"type": "chatgptDeviceCode", "loginId": "our-login", "verificationUrl": "https://auth.openai.com/codex/device", "userCode": "ABCD-1234"}})
			encoder.Encode(map[string]any{"method": "account/login/completed", "params": map[string]any{"loginId": "our-login", "success": true}})
		case "account/read":
			encoder.Encode(map[string]any{"id": id, "result": map[string]any{"account": map[string]any{"type": "chatgpt"}}})
		}
	}
}
func TestCodexLoginCollectsAndErasesPrivateCandidate(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "fixture-provider")
	body := "#!/bin/sh\nexport CAMP_PROVIDER_LOGIN_HELPER=1\nexec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run=TestProviderLoginProcess\n"
	if err = os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	a := fixture(t)
	a.commandPath = func(string, []string) (string, error) { return script, nil }
	base := t.TempDir()
	os.Chmod(base, 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	challenges := 0
	result, err := a.LoginProvider(ctx, "Codex", base, func(c ProviderLoginChallenge) error { challenges++; return nil }, nil)
	if err != nil || result.Subject != "codex:account-id:user-id" || challenges != 1 {
		t.Fatal(result.Subject, challenges, err)
	}
	files, _ := os.ReadDir(base)
	if len(files) != 0 {
		t.Fatal("Codex candidate remained on disk")
	}
}

func TestLoginOutputIsBoundedAndRedacted(t *testing.T) {
	output := &loginOutput{urls: make(chan string, 4), seen: map[string]bool{}}
	if _, err := output.Write([]byte("provider says secret-token\n")); err != nil {
		t.Fatal(err)
	}
	if len(output.urls) != 0 {
		t.Fatal("raw output exposed as challenge")
	}
	raw := fixtureLoginURL() + "\n"
	for _, chunk := range []string{raw[:20], raw[20:]} {
		if _, err := output.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if value := <-output.urls; value != strings.TrimSpace(raw) {
		t.Fatal("partial URL was forwarded")
	}
	if _, err := output.Write([]byte(strings.Repeat("x", 16385))); err == nil {
		t.Fatal("unbounded line accepted")
	}
}

func TestRealClaudeLoginChallengeOnly(t *testing.T) {
	if os.Getenv("CAMP_LIVE_LOGIN_PROBE") != "1" {
		t.Skip("opt-in: opens only an empty-profile CLI challenge, without completing sign-in")
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	a := New(config.Local{Environment: os.Environ()})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seen := false
	_, err := a.LoginProvider(ctx, "Claude", base, func(c ProviderLoginChallenge) error {
		u, e := url.Parse(c.URL)
		if e != nil || u.Host != "claude.com" || u.Query().Get("code") != "true" {
			t.Fatal("Unexpected live challenge shape")
		}
		seen = true
		return errStop
	}, nil)
	if !seen || !errors.Is(err, errStop) {
		t.Fatal("Did not validate and cancel the real CLI challenge", err)
	}
	entries, _ := os.ReadDir(base)
	if len(entries) != 0 {
		t.Fatal("Live challenge profile was retained")
	}
}

func TestLoginCodeWriteCanBeCancelled(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := writeLoginCode(ctx, writer, "unread-authorization-code"); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("blocked code relay was not cancelled", err)
	}
}

func TestCredentialPipeDoesNotBlockRead(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "credentials")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := privateLoginFile(root, "credentials"); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrProviderLogin) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("credential FIFO blocked the worker")
	}
}
