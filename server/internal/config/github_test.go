package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGitHubConfigurationIsOptionalAndComplete(t *testing.T) {
	if err := (GitHub{}).Validate(); err != nil {
		t.Fatal(err)
	}
	cfg := GitHub{ClientID: "Iv1.fixture", ClientSecret: strings.Repeat("s", 40), AppSlug: "capybara-camp", EncryptionKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))), WebhookSecret: strings.Repeat("w", 32)}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*GitHub){func(g *GitHub) { g.ClientID = "" }, func(g *GitHub) { g.ClientSecret = "" }, func(g *GitHub) { g.AppSlug = "../other" }, func(g *GitHub) { g.EncryptionKey = "" }} {
		bad := cfg
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("partial GitHub config accepted")
		}
	}
}
