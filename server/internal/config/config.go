package config

import (
	"encoding/base64"
	"errors"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strings"
)

// Config is the only environment-reading boundary in the server.
type Config struct {
	Address, DatabaseURL, PublicURL, AuthSecret, ResendKey, EmailFrom string
	ProviderEncryptionKey                                             string
	GitHub                                                            GitHub
}

func Load(migrateOnly bool) (Config, error) {
	c := Config{Address: "127.0.0.1:8096", DatabaseURL: os.Getenv("CAMP_DATABASE_URL"),
		PublicURL: os.Getenv("CAMP_PUBLIC_URL"), AuthSecret: os.Getenv("CAMP_AUTH_SECRET"),
		ResendKey: os.Getenv("CAMP_RESEND_API_KEY"), EmailFrom: os.Getenv("CAMP_EMAIL_FROM")}
	c.ProviderEncryptionKey = os.Getenv("CAMP_PROVIDER_ENCRYPTION_KEY")
	c.GitHub = loadGitHub()
	if value := os.Getenv("CAMP_ADDRESS"); value != "" {
		c.Address = value
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	return c, c.Validate(migrateOnly)
}

func (c Config) Validate(migrateOnly bool) error {
	if c.DatabaseURL == "" {
		return errors.New("CAMP_DATABASE_URL is required")
	}
	if migrateOnly {
		return nil
	}
	if err := c.GitHub.Validate(); err != nil {
		return err
	}
	if c.ProviderEncryptionKey != "" {
		key, err := base64.StdEncoding.Strict().DecodeString(c.ProviderEncryptionKey)
		if err != nil || len(key) != 32 {
			return errors.New("CAMP_PROVIDER_ENCRYPTION_KEY must be a base64-encoded 32-byte key")
		}
	}
	if _, _, err := net.SplitHostPort(c.Address); err != nil {
		return errors.New("CAMP_ADDRESS must contain host and port")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("CAMP_PUBLIC_URL must be an HTTPS origin")
	}
	if len(strings.TrimSpace(c.AuthSecret)) < 32 {
		return errors.New("CAMP_AUTH_SECRET must contain at least 32 characters")
	}
	if c.ResendKey == "" {
		return errors.New("CAMP_RESEND_API_KEY is required")
	}
	address, err := mail.ParseAddress(c.EmailFrom)
	if err != nil || address.Address == "" || strings.ContainsAny(c.EmailFrom, "\r\n") {
		return errors.New("CAMP_EMAIL_FROM must be a valid sender")
	}
	return nil
}
