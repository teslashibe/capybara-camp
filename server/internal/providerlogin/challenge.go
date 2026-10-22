// Package providerlogin owns the shared browser challenge contract.
package providerlogin

import (
	"net/url"
	"strconv"
	"strings"
)

// Subscription sign-in client used by the official Claude Code CLI. A new
// provider client needs verification before the browser can grant it access.
func validClientID(value string) bool { return value == "9d1c250a-e61b-44d9-88ed-5944d1962f5e" }

func ValidChallenge(provider, urlValue, userCode string) bool {
	if len(urlValue) > 4096 || len(userCode) > 32 || strings.ContainsAny(urlValue, "\r\n\t") {
		return false
	}
	u, err := url.Parse(urlValue)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" || u.RawPath != "" {
		return false
	}
	if provider == "Codex" {
		if u.Host != "auth.openai.com" || u.Path != "/codex/device" || u.RawQuery != "" || len(userCode) < 4 {
			return false
		}
		for _, r := range userCode {
			if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
		return true
	}
	claudeURL := u.Host == "claude.ai" && u.Path == "/oauth/authorize" || u.Host == "claude.com" && u.Path == "/cai/oauth/authorize"
	if provider != "Claude" || !claudeURL || userCode != "" {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	allowed := map[string]bool{"code": true, "client_id": true, "response_type": true, "redirect_uri": true, "scope": true, "state": true, "code_challenge": true, "code_challenge_method": true}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			return false
		}
	}
	if code := q.Get("code"); code != "" && code != "true" {
		return false
	}
	if q.Get("response_type") != "code" || !validClientID(q.Get("client_id")) || len(q.Get("state")) < 16 || len(q.Get("state")) > 256 || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 {
		return false
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.User != nil || redirect.RawQuery != "" || redirect.Fragment != "" {
		return false
	}
	if redirect.String() == "https://console.anthropic.com/oauth/code/callback" || redirect.String() == "https://platform.claude.com/oauth/code/callback" {
		return true
	}
	port, e := strconv.Atoi(redirect.Port())
	return e == nil && port > 0 && port <= 65535 && redirect.Scheme == "http" && (redirect.Hostname() == "localhost" || redirect.Hostname() == "127.0.0.1") && redirect.Path == "/callback" && redirect.RawPath == ""
}
