package providerlogin

import (
	"net/url"
	"strings"
	"testing"
)

func TestCurrentClaudeAuthorizationRoute(t *testing.T) {
	q := url.Values{"code": {"true"}, "client_id": {"9d1c250a-e61b-44d9-88ed-5944d1962f5e"}, "response_type": {"code"}, "redirect_uri": {"https://platform.claude.com/oauth/code/callback"}, "state": {strings.Repeat("s", 32)}, "code_challenge": {strings.Repeat("c", 43)}, "code_challenge_method": {"S256"}, "scope": {"user:inference"}}
	current := "https://claude.com/cai/oauth/authorize?"
	if !ValidChallenge("Claude", current+q.Encode(), "") {
		t.Fatal("current official CLI route rejected")
	}
	q.Set("redirect_uri", "https://attacker.example/callback")
	if ValidChallenge("Claude", current+q.Encode(), "") {
		t.Fatal("external callback allowed")
	}
	q.Set("redirect_uri", "https://platform.claude.com/oauth/code/callback")
	q.Set("code", "private-code")
	if ValidChallenge("Claude", current+q.Encode(), "") {
		t.Fatal("unexpected code query allowed")
	}
	q.Set("code", "true")
	q.Set("client_id", "11111111-1111-4111-8111-111111111111")
	if ValidChallenge("Claude", current+q.Encode(), "") {
		t.Fatal("unverified OAuth client accepted")
	}
}
