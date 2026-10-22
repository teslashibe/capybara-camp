package config

import (
	"os"
	"path/filepath"
)

// Local config belongs to the bundled desktop helper, not the HTTP service.
type Local struct {
	Home, UsersDir, Support string
	ProfileRoots            []string
}

func LoadLocal() (Local, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Local{}, err
	}
	c := Local{Home: home, UsersDir: "/Users", Support: filepath.Join(home, "Library/Application Support/Capybara Camp")}
	if value := os.Getenv("CAPY_SUPPORT"); value != "" {
		c.Support = value
	}
	for _, name := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if value := os.Getenv(name); value != "" {
			c.ProfileRoots = append(c.ProfileRoots, value)
		}
	}
	return c, nil
}
