package gitprovider

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ReadGrant contains only a repository-scoped, read-only user token. Never use
// the owner's broad authorization as a fallback when this operation fails.
type ReadGrant struct {
	Token string `json:"-"`
	// Camp must revoke the token when this grant is released, including when
	// GitHub omitted its own expiry. This is the latest permitted use in Camp.
	ExpiresAt    time.Time `json:"expires_at"`
	RepositoryID int64     `json:"repository_id"`
	UserID       int64     `json:"user_id"`
}

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9_.-]{1,100}$`)
var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

func (c *Client) ResolveCommit(ctx context.Context, token string, repository Repository, ref string) (string, error) {
	parts := strings.Split(repository.FullName, "/")
	if !repository.Read || repository.ID <= 0 || !repositoryPattern.MatchString(repository.FullName) || len(parts) != 2 || parts[1] == "." || parts[1] == ".." {
		return "", ErrAccess
	}
	if ref == "" {
		ref = repository.DefaultBranch
	}
	if len(ref) == 0 || len(ref) > 512 || strings.ContainsFunc(ref, unicode.IsControl) {
		return "", ErrResponse
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "." || part == ".." {
			return "", ErrResponse
		}
	}
	var out struct {
		SHA string `json:"sha"`
	}
	err := c.call(ctx, "GET", c.api+"/repos/"+repository.FullName+"/commits/"+url.PathEscape(ref), token, nil, false, &out)
	if err != nil {
		return "", err
	}
	if !shaPattern.MatchString(out.SHA) {
		return "", ErrResponse
	}
	return out.SHA, nil
}

func (c *Client) ScopeRead(ctx context.Context, rootToken string, rootExpiry time.Time, userID int64, installation Installation, repository Repository) (grant ReadGrant, err error) {
	if userID <= 0 || installation.ID <= 0 || installation.Account.ID <= 0 || installation.Suspended || (installation.Contents != "read" && installation.Contents != "write") || repository.ID <= 0 || repository.InstallationID != installation.ID || !repository.Read || !rootExpiry.After(time.Now().Add(time.Minute)) {
		return grant, ErrAccess
	}
	var out struct {
		Token        string     `json:"token"`
		ExpiresAt    *time.Time `json:"expires_at"`
		User         User       `json:"user"`
		Installation struct {
			Account     User              `json:"account"`
			Selection   string            `json:"repository_selection"`
			Permissions map[string]string `json:"permissions"`
		} `json:"installation"`
	}
	err = c.call(ctx, "POST", c.api+"/applications/"+c.clientID+"/token/scoped", "", map[string]any{"access_token": rootToken, "target_id": installation.Account.ID, "repository_ids": []int64{repository.ID}, "permissions": map[string]string{"contents": "read", "metadata": "read"}}, true, &out)
	if err != nil {
		return grant, err
	}
	// Any validation failure after token creation revokes that candidate token.
	accepted := false
	defer func() {
		if !accepted && validToken(out.Token, "ghu_") && out.Token != rootToken {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			_ = c.Revoke(cleanup, Credentials{AccessToken: out.Token})
		}
	}()
	if !validToken(out.Token, "ghu_") || out.Token == rootToken || out.User.ID != userID || out.Installation.Account.ID != installation.Account.ID || out.Installation.Selection != "selected" {
		return grant, ErrResponse
	}
	for name, level := range out.Installation.Permissions {
		if (name != "contents" && name != "metadata") || level != "read" {
			return grant, ErrResponse
		}
	}
	if out.Installation.Permissions["contents"] != "read" {
		return grant, ErrResponse
	}
	expires := rootExpiry
	if out.ExpiresAt != nil {
		if out.ExpiresAt.Before(expires) {
			expires = *out.ExpiresAt
		}
	}
	if !expires.After(time.Now().Add(30 * time.Second)) {
		return grant, ErrAuthorization
	}
	// GitHub's selected-repository response is checked using the new token.
	var repositories struct {
		Total  int `json:"total_count"`
		Values []struct {
			ID int64 `json:"id"`
		} `json:"repositories"`
	}
	err = c.call(ctx, "GET", c.api+"/user/installations/"+strconv.FormatInt(installation.ID, 10)+"/repositories?per_page=100&page=1", out.Token, nil, false, &repositories)
	if err != nil {
		return grant, err
	}
	if repositories.Total != 1 || len(repositories.Values) != 1 || repositories.Values[0].ID != repository.ID {
		return grant, ErrResponse
	}
	accepted = true
	return ReadGrant{Token: out.Token, ExpiresAt: expires, RepositoryID: repository.ID, UserID: userID}, nil
}
