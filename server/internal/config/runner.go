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
	Origin, GrantFile, StateDir, WorkspaceDir string
	Environment                               []string
}

func LoadRunner() (Runner, error) {
	c := Runner{Origin: strings.TrimRight(os.Getenv("CAMP_RUNNER_ORIGIN"), "/"), GrantFile: os.Getenv("CAMP_RUNNER_GRANT_FILE"), StateDir: os.Getenv("CAMP_RUNNER_STATE_DIR"), WorkspaceDir: os.Getenv("CAMP_RUNNER_WORKSPACE_DIR"), Environment: os.Environ()}
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
	if c.WorkspaceDir != "" {
		if !filepath.IsAbs(c.WorkspaceDir) || filepath.Clean(c.WorkspaceDir) != c.WorkspaceDir {
			return errors.New("Runner workspace path must be absolute")
		}
		for _, other := range []string{c.StateDir, c.GrantFile} {
			for _, pair := range [][2]string{{c.WorkspaceDir, other}, {other, c.WorkspaceDir}} {
				rel, err := filepath.Rel(pair[0], pair[1])
				if err != nil || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return errors.New("Runner workspace, state and grant paths must be separate")
				}
			}
		}
	}
	return nil
}

func GitCredentialFile() string { return os.Getenv("CAMP_GIT_CREDENTIAL_FILE") }
