package transfer

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/gitcheckout"
)

var ErrAncestor = errors.New("The selected repository revision has no usable local ancestor. Choose a branch present in this clone or its exact ancestor commit")

// GitHubRepository extracts identity only; credentials and URLs are never
// returned to the caller or included in errors.
func GitHubRepository(remote string) string {
	var path string
	if strings.HasPrefix(remote, "git@github.com:") {
		path = strings.TrimPrefix(remote, "git@github.com:")
	} else {
		u, err := url.Parse(remote)
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "ssh") {
			return ""
		}
		path = strings.TrimPrefix(u.Path, "/")
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	if !gitcheckout.ValidRepository(path) {
		return ""
	}
	return strings.ToLower(path)
}

// RemoteAncestor reads cached remote-tracking refs only. The cloud must still
// resolve the returned exact commit through the owner's repository access.
func RemoteAncestor(ctx context.Context, dir, repository, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !gitcheckout.ValidRepository(repository) || ref == "" || len(ref) > 512 || strings.ContainsAny(ref, "\r\n\x00") {
		return "", ErrInput
	}
	if err := verifyRoot(ctx, dir); err != nil {
		return "", err
	}
	head, _, err := revision(ctx, dir)
	if err != nil {
		return "", err
	}
	names, err := git(ctx, dir, 8192, "remote")
	if err != nil {
		return "", err
	}
	matching := []string{}
	if len(strings.Fields(string(names))) > 32 {
		return "", ErrLimit
	}
	for _, name := range strings.Fields(string(names)) {
		raw, e := git(ctx, dir, 8192, "config", "--get", "remote."+name+".url")
		if e == nil && GitHubRepository(strings.TrimSpace(string(raw))) == strings.ToLower(repository) {
			matching = append(matching, name)
			if len(matching) > 16 {
				return "", ErrLimit
			}
		}
	}
	if len(matching) == 0 {
		return "", ErrAncestor
	}
	candidates := []string{}
	if commitPattern.MatchString(ref) {
		candidates = append(candidates, ref)
	} else {
		branch := strings.TrimPrefix(ref, "refs/heads/")
		if _, err = git(ctx, dir, 1024, "check-ref-format", "refs/heads/"+branch); err != nil {
			return "", ErrInput
		}
		for _, remote := range matching {
			raw, e := git(ctx, dir, 1024, "rev-parse", "--verify", "refs/remotes/"+remote+"/"+branch+"^{commit}")
			if e == nil && commitPattern.MatchString(strings.TrimSpace(string(raw))) {
				candidates = append(candidates, strings.TrimSpace(string(raw)))
			}
		}
	}
	base := ""
	for _, candidate := range candidates {
		raw, e := git(ctx, dir, 1024, "merge-base", "--all", head, candidate)
		if e != nil {
			continue
		}
		ids := strings.Fields(string(raw))
		if len(ids) != 1 || !commitPattern.MatchString(ids[0]) {
			return "", ErrAncestor
		}
		if commitPattern.MatchString(ref) && ids[0] != ref {
			return "", ErrAncestor
		}
		// Distinct cached remotes for one repository must not choose silently.
		if base != "" && base != ids[0] {
			return "", ErrAncestor
		}
		base = ids[0]
	}
	if base == "" {
		return "", ErrAncestor
	}
	latest, _, err := revision(ctx, dir)
	if err != nil {
		return "", err
	}
	if latest != head {
		return "", ErrChanged
	}
	return base, nil
}
