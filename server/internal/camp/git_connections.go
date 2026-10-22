package camp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
)

type GitHubProvider interface {
	AuthorizeURL(string, string) string
	InstallURL() string
	Exchange(context.Context, string, string) (gitprovider.Credentials, error)
	Refresh(context.Context, gitprovider.Credentials) (gitprovider.Credentials, error)
	User(context.Context, string) (gitprovider.User, error)
	Revoke(context.Context, gitprovider.Credentials) error
	Installations(context.Context, string, int) (gitprovider.Installations, error)
	Repositories(context.Context, string, gitprovider.Installation, int) (gitprovider.Repositories, error)
}
type GitStore struct {
	store    *Store
	provider GitHubProvider
	cipher   cipher.AEAD
}

func (g *GitStore) InstallURL() string { return g.provider.InstallURL() }

func NewGitStore(s *Store, provider GitHubProvider, key []byte) (*GitStore, error) {
	if len(key) != 32 || provider == nil {
		return nil, ErrInput
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &GitStore{s, provider, aead}, nil
}

type GitConnection struct {
	ID              string     `json:"id"`
	OwnerUserID     string     `json:"owner_user_id"`
	GitHubUserID    int64      `json:"github_user_id,omitempty"`
	Login           string     `json:"login"`
	State           string     `json:"state"`
	Revision        int64      `json:"revision"`
	Epoch           int64      `json:"epoch"`
	AccessExpiresAt *time.Time `json:"access_expires_at,omitempty"`
}
type GitLogin struct {
	ID           string    `json:"id"`
	ConnectionID string    `json:"connection_id"`
	State        string    `json:"state"`
	FailureCode  string    `json:"failure_code,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	URL          string    `json:"url,omitempty"`
}
type GitStatus struct {
	Connection GitConnection `json:"connection"`
	Login      *GitLogin     `json:"login,omitempty"`
}
type gitAuthContext struct {
	Verifier string `json:"verifier"`
	URL      string `json:"url"`
}
type GitExchange struct {
	ID, OwnerID, ConnectionID, ClaimID, Verifier string
	Epoch                                        int64
}

const gitColumns = `id,owner_user_id,COALESCE(github_user_id,0),github_login,state,revision,epoch,access_expires_at`

func gitConnection(row pgx.Row) (GitConnection, error) {
	var c GitConnection
	err := row.Scan(&c.ID, &c.OwnerUserID, &c.GitHubUserID, &c.Login, &c.State, &c.Revision, &c.Epoch, &c.AccessExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return c, err
}
func (g *GitStore) seal(value any, binding string) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.cipher.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.cipher.Seal(nonce, nonce, raw, []byte(binding)), nil
}
func (g *GitStore) open(raw []byte, binding string, out any) error {
	n := g.cipher.NonceSize()
	if len(raw) < n {
		return ErrInput
	}
	plain, err := g.cipher.Open(nil, raw[:n], raw[n:], []byte(binding))
	if err != nil {
		return ErrInput
	}
	if json.Unmarshal(plain, out) != nil {
		return ErrInput
	}
	return nil
}
func gitCredentialBinding(c GitConnection) string {
	return fmt.Sprintf("git-credentials:v1:%s:%s:%d", c.OwnerUserID, c.ID, c.Epoch)
}
func (g *GitStore) Connections(ctx context.Context, a auth.Actor) ([]GitConnection, error) {
	rows, err := g.store.pool.Query(ctx, `SELECT `+gitColumns+` FROM git_connections WHERE owner_user_id=$1 ORDER BY created_at,id LIMIT 8`, a.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GitConnection{}
	for rows.Next() {
		c, err := gitConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (g *GitStore) lock(ctx context.Context, tx pgx.Tx, a auth.Actor, id string) (GitConnection, error) {
	if !validID(id) {
		return GitConnection{}, ErrDenied
	}
	return gitConnection(tx.QueryRow(ctx, `SELECT `+gitColumns+` FROM git_connections WHERE id=$1 AND owner_user_id=$2 FOR UPDATE`, id, a.UserID))
}
func expireGitLogins(ctx context.Context, tx pgx.Tx, connectionID string) error {
	_, err := tx.Exec(ctx, `UPDATE git_logins SET state='expired',encrypted_context=NULL,updated_at=now() WHERE connection_id=$1 AND state IN ('waiting','exchanging') AND expires_at<=clock_timestamp()`, connectionID)
	return err
}
func (g *GitStore) login(ctx context.Context, tx pgx.Tx, a auth.Actor, id string) (GitLogin, error) {
	var out GitLogin
	var encrypted []byte
	err := tx.QueryRow(ctx, `SELECT id,connection_id,state,failure_code,expires_at,encrypted_context FROM git_logins WHERE id=$1 AND owner_user_id=$2`, id, a.UserID).Scan(&out.ID, &out.ConnectionID, &out.State, &out.FailureCode, &out.ExpiresAt, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	if err != nil {
		return out, err
	}
	if out.State == "waiting" {
		var context gitAuthContext
		if err = g.open(encrypted, "git-login:v1:"+a.UserID+":"+id, &context); err != nil {
			return out, err
		}
		out.URL = context.URL
	}
	return out, nil
}
func (g *GitStore) Status(ctx context.Context, a auth.Actor, id string) (GitStatus, error) {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return GitStatus{}, err
	}
	defer tx.Rollback(ctx)
	c, err := g.lock(ctx, tx, a, id)
	if err != nil {
		return GitStatus{}, err
	}
	if err = expireGitLogins(ctx, tx, id); err != nil {
		return GitStatus{}, err
	}
	out := GitStatus{Connection: c}
	var loginID string
	err = tx.QueryRow(ctx, `SELECT id FROM git_logins WHERE connection_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, id).Scan(&loginID)
	if err == nil {
		login, err := g.login(ctx, tx, a, loginID)
		if err != nil {
			return out, err
		}
		out.Login = &login
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (g *GitStore) Begin(ctx context.Context, a auth.Actor, key, connectionID string) (GitLogin, error) {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return GitLogin{}, err
	}
	defer tx.Rollback(ctx)
	var user string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, a.UserID).Scan(&user); err != nil {
		return GitLogin{}, err
	}
	var out GitLogin
	hit, err := g.store.beginCommand(ctx, tx, a, key, "git.begin", connectionID, &out)
	if err != nil {
		return out, err
	}
	if hit {
		if err = expireGitLogins(ctx, tx, out.ConnectionID); err != nil {
			return out, err
		}
		out, err = g.login(ctx, tx, a, out.ID)
		if err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	var c GitConnection
	if connectionID == "" {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM git_connections WHERE owner_user_id=$1`, a.UserID).Scan(&count); err != nil {
			return out, err
		}
		if count >= 8 {
			return out, ErrInput
		}
		connectionID = uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO git_connections(id,owner_user_id) VALUES($1,$2)`, connectionID, a.UserID)
		if err != nil {
			return out, err
		}
	}
	c, err = g.lock(ctx, tx, a, connectionID)
	if err != nil {
		return out, err
	}
	if err = expireGitLogins(ctx, tx, connectionID); err != nil {
		return out, err
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM git_logins WHERE connection_id=$1 AND state IN ('waiting','exchanging'))`, connectionID).Scan(&active); err != nil {
		return out, err
	}
	if active {
		return out, ErrTransition
	}
	state, err := randomGitSecret()
	if err != nil {
		return out, err
	}
	verifier, err := randomGitSecret()
	if err != nil {
		return out, err
	}
	out = GitLogin{ID: uuid.NewString(), ConnectionID: c.ID, State: "waiting", URL: g.provider.AuthorizeURL(state, verifier)}
	encrypted, err := g.seal(gitAuthContext{verifier, out.URL}, "git-login:v1:"+a.UserID+":"+out.ID)
	if err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO git_logins(id,owner_user_id,connection_id,connection_epoch,state_hash,encrypted_context,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()+interval '10 minutes') RETURNING expires_at`, out.ID, a.UserID, c.ID, c.Epoch, auth.HashToken(state), encrypted).Scan(&out.ExpiresAt)
	if err != nil {
		return out, err
	}
	if err = g.store.finishCommand(ctx, tx, a, key, GitLogin{ID: out.ID, ConnectionID: out.ConnectionID}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func randomGitSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
func (g *GitStore) Claim(ctx context.Context, state string) (GitExchange, error) {
	if len(state) != 43 {
		return GitExchange{}, ErrDenied
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return GitExchange{}, err
	}
	defer tx.Rollback(ctx)
	var out GitExchange
	var encrypted []byte
	var status string
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT id,owner_user_id,connection_id,connection_epoch,state,expires_at,encrypted_context FROM git_logins WHERE state_hash=$1 FOR UPDATE`, auth.HashToken(state)).Scan(&out.ID, &out.OwnerID, &out.ConnectionID, &out.Epoch, &status, &expires, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	if err != nil {
		return out, err
	}
	if status != "waiting" || !expires.After(time.Now()) {
		return out, ErrTransition
	}
	var secret gitAuthContext
	if err = g.open(encrypted, "git-login:v1:"+out.OwnerID+":"+out.ID, &secret); err != nil {
		return out, err
	}
	out.Verifier = secret.Verifier
	out.ClaimID = uuid.NewString()
	_, err = tx.Exec(ctx, `UPDATE git_logins SET state='exchanging',claim_id=$2,encrypted_context=NULL,updated_at=now() WHERE id=$1`, out.ID, out.ClaimID)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (g *GitStore) failLogin(ctx context.Context, x GitExchange, code string) error {
	if code != "provider_rejected" && code != "identity_mismatch" && code != "connection_changed" && code != "exchange_unknown" {
		return ErrInput
	}
	_, err := g.store.pool.Exec(ctx, `UPDATE git_logins SET state='failed',failure_code=$3,encrypted_context=NULL,updated_at=now() WHERE id=$1 AND claim_id=$2 AND state='exchanging'`, x.ID, x.ClaimID, code)
	return err
}

func (g *GitStore) Reject(ctx context.Context, x GitExchange) error {
	return g.failLogin(ctx, x, "provider_rejected")
}
func (g *GitStore) complete(ctx context.Context, x GitExchange, creds gitprovider.Credentials, user gitprovider.User) error {
	if !creds.Valid() || !user.Valid() {
		return ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Connections precede login locks, matching cancel/disconnect order.
	a := auth.Actor{UserID: x.OwnerID}
	c, err := g.lock(ctx, tx, a, x.ConnectionID)
	if err != nil {
		return err
	}
	var current bool
	err = tx.QueryRow(ctx, `SELECT state='exchanging' AND claim_id=$2 AND expires_at>clock_timestamp() FROM git_logins WHERE id=$1 FOR UPDATE`, x.ID, x.ClaimID).Scan(&current)
	if err != nil {
		return err
	}
	if !current || c.Epoch != x.Epoch {
		return ErrTransition
	}
	if c.GitHubUserID != 0 && c.GitHubUserID != user.ID {
		return gitprovider.ErrAuthorization
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM git_connections WHERE owner_user_id=$1 AND github_user_id=$2 AND id<>$3)`, a.UserID, user.ID, c.ID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrConflict
	}
	var old []byte
	if err = tx.QueryRow(ctx, `SELECT encrypted_credentials FROM git_connections WHERE id=$1`, c.ID).Scan(&old); err != nil {
		return err
	}
	if len(old) > 0 {
		var previous gitprovider.Credentials
		if err = g.open(old, gitCredentialBinding(c), &previous); err != nil {
			return err
		}
		if previous.AccessToken != creds.AccessToken {
			if err = g.queueRevocation(ctx, tx, a.UserID, previous); err != nil {
				return err
			}
		}
	}
	c.Epoch++
	encrypted, err := g.seal(creds, gitCredentialBinding(c))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE git_connections SET github_user_id=$2,github_login=$3,state='connected',revision=revision+1,epoch=$4,encrypted_credentials=$5,access_expires_at=$6,refresh_claim_id=NULL,refresh_started_at=NULL,updated_at=now() WHERE id=$1`, c.ID, user.ID, user.Login, c.Epoch, encrypted, creds.ExpiresAt)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE git_logins SET state='connected',updated_at=now() WHERE id=$1`, x.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Exchange is invoked once after Claim. An ambiguous exchange is not replayed.
func (g *GitStore) Exchange(ctx context.Context, x GitExchange, code string) error {
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	creds, err := g.provider.Exchange(work, code, x.Verifier)
	if err != nil {
		failure := "provider_rejected"
		if errors.Is(err, gitprovider.ErrUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			failure = "exchange_unknown"
		}
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer done()
		_ = g.failLogin(cleanup, x, failure)
		return err
	}
	user, err := g.provider.User(work, creds.AccessToken)
	if err == nil {
		err = g.complete(work, x, creds, user)
	}
	if err != nil {
		// Queue cleanup even if the browser has disconnected or cancellation won.
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer done()
		_ = g.enqueueRevocation(cleanup, x.OwnerID, creds)
		failure := "connection_changed"
		if errors.Is(err, gitprovider.ErrAuthorization) || errors.Is(err, ErrConflict) {
			failure = "identity_mismatch"
		}
		_ = g.failLogin(cleanup, x, failure)
	}
	return err
}
func (g *GitStore) Cancel(ctx context.Context, a auth.Actor, connectionID, loginID, key string) (GitLogin, error) {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return GitLogin{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = g.lock(ctx, tx, a, connectionID); err != nil {
		return GitLogin{}, err
	}
	var out GitLogin
	hit, err := g.store.beginCommand(ctx, tx, a, key, "git.cancel:"+loginID, connectionID, &out)
	if err != nil {
		return out, err
	}
	if !hit {
		tag, err := tx.Exec(ctx, `UPDATE git_logins SET state='cancelled',encrypted_context=NULL,updated_at=now() WHERE id=$1 AND connection_id=$2 AND owner_user_id=$3 AND state IN ('waiting','exchanging')`, loginID, connectionID, a.UserID)
		if err != nil {
			return out, err
		}
		if tag.RowsAffected() == 0 {
			return out, ErrTransition
		}
		if err = g.store.finishCommand(ctx, tx, a, key, GitLogin{ID: loginID}); err != nil {
			return out, err
		}
	}
	out, err = g.login(ctx, tx, a, loginID)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (g *GitStore) Disconnect(ctx context.Context, a auth.Actor, id, key string) (GitConnection, error) {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return GitConnection{}, err
	}
	defer tx.Rollback(ctx)
	c, err := g.lock(ctx, tx, a, id)
	if err != nil {
		return c, err
	}
	var receipt GitConnection
	hit, err := g.store.beginCommand(ctx, tx, a, key, "git.disconnect", id, &receipt)
	if err != nil {
		return c, err
	}
	if hit {
		return c, tx.Commit(ctx)
	}
	var encrypted []byte
	if err = tx.QueryRow(ctx, `SELECT encrypted_credentials FROM git_connections WHERE id=$1`, id).Scan(&encrypted); err != nil {
		return c, err
	}
	if len(encrypted) > 0 {
		var creds gitprovider.Credentials
		if err = g.open(encrypted, gitCredentialBinding(c), &creds); err != nil {
			return c, err
		}
		if err = g.queueRevocation(ctx, tx, a.UserID, creds); err != nil {
			return c, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE git_connections SET state='disconnected',epoch=epoch+1,revision=revision+1,encrypted_credentials=NULL,access_expires_at=NULL,refresh_claim_id=NULL,refresh_started_at=NULL,updated_at=now() WHERE id=$1`, id)
	if err != nil {
		return c, err
	}
	_, err = tx.Exec(ctx, `UPDATE git_logins SET state='cancelled',encrypted_context=NULL,updated_at=now() WHERE connection_id=$1 AND state IN ('waiting','exchanging')`, id)
	if err != nil {
		return c, err
	}
	c, err = g.lock(ctx, tx, a, id)
	if err != nil {
		return c, err
	}
	if err = g.store.finishCommand(ctx, tx, a, key, c); err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}
