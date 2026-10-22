package config

import (
	"encoding/base64"
	"errors"
	"os"
	"regexp"
)

type GitHub struct {
	ClientID, ClientSecret, AppSlug, EncryptionKey, WebhookSecret string
}

func (g GitHub) Enabled() bool {
	return g.ClientID != "" || g.ClientSecret != "" || g.AppSlug != "" || g.EncryptionKey != "" || g.WebhookSecret != ""
}

func (g GitHub) Validate() error {
	if !g.Enabled() {
		return nil
	}
	key, err := base64.StdEncoding.Strict().DecodeString(g.EncryptionKey)
	if err != nil || len(key) != 32 || !regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`).MatchString(g.ClientID) ||
		!regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`).MatchString(g.AppSlug) || len(g.ClientSecret) < 20 || len(g.ClientSecret) > 256 || len(g.WebhookSecret) < 32 || len(g.WebhookSecret) > 256 {
		return errors.New("GitHub requires CAMP_GITHUB_CLIENT_ID, CAMP_GITHUB_CLIENT_SECRET, CAMP_GITHUB_APP_SLUG, CAMP_GITHUB_WEBHOOK_SECRET and a base64 32-byte CAMP_GITHUB_ENCRYPTION_KEY")
	}
	return nil
}

func loadGitHub() GitHub {
	return GitHub{ClientID: os.Getenv("CAMP_GITHUB_CLIENT_ID"), ClientSecret: os.Getenv("CAMP_GITHUB_CLIENT_SECRET"), AppSlug: os.Getenv("CAMP_GITHUB_APP_SLUG"), EncryptionKey: os.Getenv("CAMP_GITHUB_ENCRYPTION_KEY"), WebhookSecret: os.Getenv("CAMP_GITHUB_WEBHOOK_SECRET")}
}
