package config

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Runner is intentionally separate from the server: a provider workload never
// receives database, mail or human Camp credentials.
type Runner struct {
	Origin, GrantFile, StateDir string
	Environment                 []string
}

func LoadRunner() (Runner, error) {
	c := Runner{Origin: strings.TrimRight(os.Getenv("CAMP_RUNNER_ORIGIN"), "/"), GrantFile: os.Getenv("CAMP_RUNNER_GRANT_FILE"), StateDir: os.Getenv("CAMP_RUNNER_STATE_DIR"), Environment: os.Environ()}
	return c, c.Validate()
}

func (c Runner) Validate() error {
	u, err := url.Parse(c.Origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("CAMP_RUNNER_ORIGIN must be an HTTPS origin")
	}
	if !filepath.IsAbs(c.GrantFile) || !filepath.IsAbs(c.StateDir) || filepath.Clean(c.GrantFile) != c.GrantFile || filepath.Clean(c.StateDir) != c.StateDir {
		return errors.New("Runner grant and state paths must be absolute")
	}
	if rel, err := filepath.Rel(c.StateDir, c.GrantFile); err != nil || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("The runner grant file must be outside its state directory")
	}
	return nil
}
