package transfer

import (
	"errors"
	"testing"
)

func TestRemoteAncestorUsesSelectedRepositoryAndCachedBranch(t *testing.T) {
	source, _, base := archiveFixture(t)
	gitTest(t, source, "remote", "add", "origin", "git@github.com:fixture/repo.git")
	gitTest(t, source, "update-ref", "refs/remotes/origin/main", base)
	write(t, source, "unpublished.txt", "local work\n", 0600)
	head := committed(t, source, "Unpublished local work")
	for _, ref := range []string{"main", "refs/heads/main", base} {
		got, err := RemoteAncestor(t.Context(), source, "Fixture/Repo", ref)
		if err != nil || got != base {
			t.Fatal("wrong ancestor", ref, got, err)
		}
	}
	for _, ref := range []string{"missing", "--help", "main~1", "refs/remotes/origin/main", "main\nextra"} {
		if _, err := RemoteAncestor(t.Context(), source, "fixture/repo", ref); err == nil {
			t.Fatal("accepted invalid or absent branch", ref)
		}
	}
	if _, err := RemoteAncestor(t.Context(), source, "other/repo", "main"); !errors.Is(err, ErrAncestor) {
		t.Fatal("foreign remote used", err)
	}
	// A remote branch can advance independently after our local branch forks.
	gitTest(t, source, "checkout", "-qb", "remote-side", base)
	write(t, source, "remote.txt", "remote work\n", 0600)
	remote := committed(t, source, "New remote work")
	gitTest(t, source, "update-ref", "refs/remotes/origin/main", remote)
	gitTest(t, source, "checkout", "--detach", head)
	got, err := RemoteAncestor(t.Context(), source, "fixture/repo", "main")
	if err != nil || got != base {
		t.Fatal("divergent branch lost common ancestor", got, err)
	}
	if _, err = RemoteAncestor(t.Context(), source, "fixture/repo", remote); !errors.Is(err, ErrAncestor) {
		t.Fatal("explicit non-ancestor silently replaced", err)
	}
	// Two cached remotes disagree; do not choose based on name or enumeration order.
	gitTest(t, source, "remote", "add", "second", "https://github.com/fixture/repo.git")
	gitTest(t, source, "update-ref", "refs/remotes/second/main", head)
	if _, err = RemoteAncestor(t.Context(), source, "fixture/repo", "main"); !errors.Is(err, ErrAncestor) {
		t.Fatal("conflicting remotes silently selected", err)
	}
	if got := commitID(t, source); got != head {
		t.Fatal("discovery moved source HEAD")
	}
}
func TestGitHubRepositoryIdentityOnly(t *testing.T) {
	for _, remote := range []string{"git@github.com:Owner/Repo.git", "https://github.com/Owner/Repo.git", "ssh://git@github.com/Owner/Repo.git", "https://username:fixture-password@github.com/Owner/Repo.git"} {
		if got := GitHubRepository(remote); got != "owner/repo" {
			t.Fatal("identity not normalized")
		}
	}
	for _, remote := range []string{"file:///private/repo", "https://github.com.evil/owner/repo", "https://github.com/owner/repo?secret=value", "https://github.com:443/owner/repo", "https://github.com/owner/repo/extra", "https://github.com/owner/../repo"} {
		if got := GitHubRepository(remote); got != "" {
			t.Fatal("unsupported remote accepted")
		}
	}
}
