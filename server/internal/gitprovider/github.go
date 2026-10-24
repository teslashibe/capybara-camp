// Package gitprovider implements the user-authorized GitHub App boundary.
// It never falls back to an installation token or an operator's Git identity.
package gitprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/config"
)

var (
	ErrUnavailable   = errors.New("GitHub is temporarily unavailable")
	ErrAuthorization = errors.New("GitHub authorization must be renewed")
	ErrAccess        = errors.New("GitHub repository access is unavailable")
	ErrResponse      = errors.New("GitHub returned an invalid response")
)

// Credentials are private storage/transport data, never an HTTP response model.
type Credentials struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

func (c Credentials) Valid() bool {
	return validToken(c.AccessToken, "ghu_") && validToken(c.RefreshToken, "ghr_") &&
		c.ExpiresAt.After(time.Now()) && c.RefreshExpiresAt.After(c.ExpiresAt)
}
func validToken(token, prefix string) bool {
	return strings.HasPrefix(token, prefix) && len(token) >= 12 && len(token) <= 512 && regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(token)
}

type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}
type Installation struct {
	ID           int64  `json:"id"`
	Account      User   `json:"account"`
	Contents     string `json:"contents"`
	PullRequests string `json:"pull_requests"`
	Suspended    bool   `json:"suspended"`
}
type Installations struct {
	Values   []Installation `json:"installations"`
	NextPage int            `json:"next_page,omitempty"`
}
type Repository struct {
	ID             int64  `json:"id"`
	InstallationID int64  `json:"installation_id"`
	FullName       string `json:"full_name"`
	DefaultBranch  string `json:"default_branch"`
	Private        bool   `json:"private"`
	Read           bool   `json:"read"`
	Write          bool   `json:"write"`
	PullRequests   bool   `json:"pull_requests"`
}
type Repositories struct {
	Values   []Repository `json:"repositories"`
	NextPage int          `json:"next_page,omitempty"`
}

type Client struct {
	clientID, secret, slug, callback string
	web, api                         string
	http                             *http.Client
}

func New(g config.GitHub, origin string, transport *http.Client) (*Client, error) {
	if !g.Enabled() || g.Validate() != nil {
		return nil, ErrAuthorization
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, ErrResponse
	}
	if transport == nil {
		transport = http.DefaultClient
	}
	h := *transport
	h.Timeout = 8 * time.Second
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{clientID: g.ClientID, secret: g.ClientSecret, slug: g.AppSlug, callback: origin + "/v1/git/github/callback", web: "https://github.com", api: "https://api.github.com", http: &h}, nil
}
func (c *Client) AuthorizeURL(state, verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {c.clientID}, "redirect_uri": {c.callback}, "state": {state}, "scope": {"offline_access"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}, "prompt": {"select_account"}}
	return c.web + "/login/oauth/authorize?" + q.Encode()
}
func (c *Client) InstallURL() string {
	return "https://github.com/apps/" + c.slug + "/installations/new"
}

func (c *Client) call(ctx context.Context, method, target, token string, body any, basic bool, out any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return ErrResponse
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(data))
	if err != nil {
		return ErrResponse
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if target == c.web+"/login/oauth/access_token" {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "Capybara-Camp")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if basic {
		req.SetBasicAuth(c.clientID, c.secret)
	} else if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 401 {
		return ErrAuthorization
	}
	if res.StatusCode == 403 && (res.Header.Get("Retry-After") != "" || res.Header.Get("X-RateLimit-Remaining") == "0") {
		return ErrUnavailable
	}
	if res.StatusCode == 403 || res.StatusCode == 404 {
		return ErrAccess
	}
	if res.StatusCode == 429 || res.StatusCode >= 500 {
		return ErrUnavailable
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ErrResponse
	}
	if out == nil || res.StatusCode == 204 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 || json.Unmarshal(raw, out) != nil {
		return ErrResponse
	}
	return nil
}
func (c *Client) token(ctx context.Context, values map[string]string) (Credentials, error) {
	values["client_id"], values["client_secret"] = c.clientID, c.secret
	var out struct {
		AccessToken    string `json:"access_token"`
		RefreshToken   string `json:"refresh_token"`
		Expires        int64  `json:"expires_in"`
		RefreshExpires int64  `json:"refresh_token_expires_in"`
		Type           string `json:"token_type"`
		Error          string `json:"error"`
	}
	err := c.call(ctx, "POST", c.web+"/login/oauth/access_token", "", values, false, &out)
	if err != nil {
		return Credentials{}, err
	}
	if out.Error != "" {
		return Credentials{}, ErrAuthorization
	}
	if out.Type != "bearer" || out.Expires < 60 || out.Expires > 86400 || out.RefreshExpires <= out.Expires || out.RefreshExpires > 200*86400 {
		return Credentials{}, ErrResponse
	}
	now := time.Now()
	creds := Credentials{out.AccessToken, out.RefreshToken, now.Add(time.Duration(out.Expires) * time.Second), now.Add(time.Duration(out.RefreshExpires) * time.Second)}
	if !creds.Valid() {
		return Credentials{}, ErrResponse
	}
	return creds, nil
}
func (c *Client) Exchange(ctx context.Context, code, verifier string) (Credentials, error) {
	return c.token(ctx, map[string]string{"code": code, "code_verifier": verifier, "redirect_uri": c.callback})
}
func (c *Client) Refresh(ctx context.Context, creds Credentials) (Credentials, error) {
	return c.token(ctx, map[string]string{"grant_type": "refresh_token", "refresh_token": creds.RefreshToken})
}
func (c *Client) Revoke(ctx context.Context, creds Credentials) error {
	err := c.call(ctx, "DELETE", c.api+"/applications/"+c.clientID+"/token", "", map[string]string{"access_token": creds.AccessToken}, true, nil)
	if errors.Is(err, ErrAccess) {
		return nil
	}
	return err
}

var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func (u User) Valid() bool { return u.ID > 0 && loginPattern.MatchString(u.Login) }
func (c *Client) User(ctx context.Context, token string) (User, error) {
	var u User
	err := c.call(ctx, "GET", c.api+"/user", token, nil, false, &u)
	if err != nil {
		return u, err
	}
	if !u.Valid() {
		return User{}, ErrResponse
	}
	return u, nil
}
func pageQuery(page int) (string, error) {
	if page < 1 || page > 10000 {
		return "", ErrResponse
	}
	return "?per_page=100&page=" + strconv.Itoa(page), nil
}
func nextPage(page, count, total int) int {
	if count == 100 && page*100 < total {
		return page + 1
	}
	return 0
}
func (c *Client) Installations(ctx context.Context, token string, page int) (Installations, error) {
	q, err := pageQuery(page)
	if err != nil {
		return Installations{}, err
	}
	var raw struct {
		Total  int `json:"total_count"`
		Values []struct {
			ID          int64      `json:"id"`
			Account     User       `json:"account"`
			Slug        string     `json:"app_slug"`
			SuspendedAt *time.Time `json:"suspended_at"`
			Permissions struct {
				Contents     string `json:"contents"`
				PullRequests string `json:"pull_requests"`
			} `json:"permissions"`
		} `json:"installations"`
	}
	if err = c.call(ctx, "GET", c.api+"/user/installations"+q, token, nil, false, &raw); err != nil {
		return Installations{}, err
	}
	if len(raw.Values) > 100 || raw.Total < 0 || raw.Total > 1000000 {
		return Installations{}, ErrResponse
	}
	out := Installations{Values: []Installation{}, NextPage: nextPage(page, len(raw.Values), raw.Total)}
	for _, v := range raw.Values {
		if v.ID <= 0 || !v.Account.Valid() || v.Slug != c.slug {
			return Installations{}, ErrResponse
		}
		out.Values = append(out.Values, Installation{v.ID, v.Account, v.Permissions.Contents, v.Permissions.PullRequests, v.SuspendedAt != nil})
	}
	return out, nil
}
func (c *Client) Repositories(ctx context.Context, token string, installation Installation, page int) (Repositories, error) {
	if installation.ID <= 0 || installation.Suspended {
		return Repositories{}, ErrAccess
	}
	q, err := pageQuery(page)
	if err != nil {
		return Repositories{}, err
	}
	var raw struct {
		Total  int `json:"total_count"`
		Values []struct {
			ID          int64                            `json:"id"`
			Name        string                           `json:"full_name"`
			Branch      string                           `json:"default_branch"`
			Private     bool                             `json:"private"`
			Archived    bool                             `json:"archived"`
			Disabled    bool                             `json:"disabled"`
			Permissions struct{ Pull, Push, Admin bool } `json:"permissions"`
		} `json:"repositories"`
	}
	if err = c.call(ctx, "GET", c.api+"/user/installations/"+strconv.FormatInt(installation.ID, 10)+"/repositories"+q, token, nil, false, &raw); err != nil {
		return Repositories{}, err
	}
	if len(raw.Values) > 100 || raw.Total < 0 || raw.Total > 1000000 {
		return Repositories{}, ErrResponse
	}
	out := Repositories{Values: []Repository{}, NextPage: nextPage(page, len(raw.Values), raw.Total)}
	for _, v := range raw.Values {
		parts := strings.Split(v.Name, "/")
		if v.ID <= 0 || len(parts) != 2 || !loginPattern.MatchString(parts[0]) || !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`).MatchString(parts[1]) || parts[1] == "." || parts[1] == ".." || len(v.Branch) > 512 || strings.ContainsAny(v.Branch, "\x00\r\n") {
			return Repositories{}, ErrResponse
		}
		read := (v.Permissions.Pull || v.Permissions.Push || v.Permissions.Admin) && (installation.Contents == "read" || installation.Contents == "write") && !v.Disabled
		write := read && (v.Permissions.Push || v.Permissions.Admin) && installation.Contents == "write" && !v.Archived
		out.Values = append(out.Values, Repository{v.ID, installation.ID, v.Name, v.Branch, v.Private, read, write, write && installation.PullRequests == "write"})
	}
	return out, nil
}
