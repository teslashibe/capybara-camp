package config

import (
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
