package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestConfigRefusesUnsafeOrigin(t *testing.T) {
	base := Config{Address: "127.0.0.1:8096", DatabaseURL: "postgres://example/camp", PublicURL: "https://camp.example", AuthSecret: strings.Repeat("x", 32), ResendKey: "fixture", EmailFrom: "camp@example.com"}
	if err := base.Validate(false); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://camp.example", "https://key:secret@camp.example", "https://camp.example/path", "https://camp.example?key=secret"} {
		c := base
		c.PublicURL = origin
		if c.Validate(false) == nil {
			t.Fatalf("accepted origin %s", origin)
		}
	}
	if err := (Config{DatabaseURL: "postgres://example/camp"}).Validate(true); err != nil {
		t.Fatal(err)
	}
}

func TestProviderKeyIsSeparateAndOptional(t *testing.T) {
	base := Config{Address: "127.0.0.1:8096", DatabaseURL: "postgres://example/camp", PublicURL: "https://camp.example", AuthSecret: strings.Repeat("x", 32), ResendKey: "fixture", EmailFrom: "camp@example.com"}
	for _, bad := range []string{"short", strings.Repeat("a", 32), strings.Repeat("=", 44)} {
		c := base
		c.ProviderEncryptionKey = bad
		if c.Validate(false) == nil {
			t.Fatal("invalid provider key accepted")
		}
	}
	base.ProviderEncryptionKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	if err := base.Validate(false); err != nil {
		t.Fatal(err)
	}
}

func TestAllowedEmailsAreNormalized(t *testing.T) {
	t.Setenv("CAMP_DATABASE_URL", "postgres://example/camp")
	t.Setenv("CAMP_ALLOWED_EMAILS", " Brendan@Example.com, ,cate@example.com ")
	c, err := Load(true)
	if err != nil || strings.Join(c.AllowedEmails, ",") != "brendan@example.com,cate@example.com" {
		t.Fatalf("allowed emails %v: %v", c.AllowedEmails, err)
	}
	t.Setenv("CAMP_ALLOWED_EMAILS", "Brendan <brendan@example.com>")
	if _, err = Load(true); err == nil {
		t.Fatal("accepted a display-name address")
	}
}
