package config

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Set only by build.sh --development. Production helpers ignore fixture paths.
var developmentBuild string

// Local config belongs to the bundled desktop helper, not the HTTP service.
type Local struct {
	Home, UsersDir, Support string
	ProfileRoots            []string
	Environment             []string
}

func LoadLocal() (Local, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Local{}, err
	}
	c := Local{Home: home, UsersDir: "/Users", Support: filepath.Join(home, "Library/Application Support/Capybara Camp"), Environment: os.Environ()}
	if value := os.Getenv("CAPY_SUPPORT"); value != "" {
		c.Support = value
	}
	if root := os.Getenv("CAMP_DEV_PROFILE_ROOT"); developmentBuild == "1" && root != "" {
		info, err := os.Lstat(root)
		if !filepath.IsAbs(root) || err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return Local{}, errors.New("The development profile root must be a private directory")
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
			return Local{}, errors.New("The development profile root must belong to this user")
		}
		c.Home, c.UsersDir = filepath.Join(root, "home"), filepath.Join(root, "users")
		return c, nil
	}
	for _, name := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if value := os.Getenv(name); value != "" {
			c.ProfileRoots = append(c.ProfileRoots, value)
		}
	}
	return c, nil
}
