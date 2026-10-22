package camp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

var ErrCloudUnavailable = errors.New("cloud accounts are not configured")

// Provider credentials use a separate operator-supplied key, never the login or
// command-receipt key. A server without it can still serve local shared Camps.
func NewWithProviderKey(pool *pgxpool.Pool, secret string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, ErrCloudUnavailable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := New(pool, secret)
	s.providerCipher = aead
	return s, nil
}

type ProviderAccount struct {
	ID                  string     `json:"id"`
	OwnerUserID         string     `json:"owner_user_id"`
	Provider            string     `json:"provider"`
	Label               string     `json:"label"`
	VerifiedSubject     string     `json:"verified_subject,omitempty"`
	Revision            int64      `json:"revision"`
	InstallationID      string     `json:"installation_id"`
	Location            string     `json:"location"`
	State               string     `json:"state"`
	Epoch               int64      `json:"epoch"`
	CredentialExpiresAt *time.Time `json:"credential_expires_at,omitempty"`
}
type ProviderAccountInput struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
}
type RunnerGrant struct {
	InstallationID string    `json:"installation_id"`
	Token          string    `json:"token"`
	Epoch          int64     `json:"epoch"`
	ExpiresAt      time.Time `json:"expires_at"`
}
type ProviderLogin struct {
	ID             string    `json:"id"`
	InstallationID string    `json:"installation_id"`
	State          string    `json:"state"`
	FailureCode    string    `json:"failure_code,omitempty"`
	URL            string    `json:"url,omitempty"`
	UserCode       string    `json:"user_code,omitempty"`
	CodeSubmitted  bool      `json:"code_submitted,omitempty"`
	CodeConsumed   bool      `json:"code_consumed,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type ProviderAccountStatus struct {
	Account ProviderAccount `json:"account"`
	Login   *ProviderLogin  `json:"login,omitempty"`
}

func (s *Store) ProviderAccountStatus(ctx context.Context, actor auth.Actor, id string) (ProviderAccountStatus, error) {
	if err := s.cloudEnabled(); err != nil {
		return ProviderAccountStatus{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProviderAccountStatus{}, err
	}
	defer tx.Rollback(ctx)
	a, err := s.lockAccount(ctx, tx, actor.UserID, id)
	if err != nil {
		return ProviderAccountStatus{}, err
	}
	if err = s.expireLogins(ctx, tx, a.InstallationID); err != nil {
		return ProviderAccountStatus{}, err
	}
	out := ProviderAccountStatus{Account: a}
	var attempt string
	err = tx.QueryRow(ctx, `SELECT id FROM provider_login_attempts WHERE installation_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, a.InstallationID).Scan(&attempt)
	if err == nil {
		login, e := s.readLogin(ctx, tx, a, attempt)
		if e != nil {
			return out, e
		}
		out.Login = &login
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	return out, tx.Commit(ctx)
}

const accountColumns = `a.id,a.owner_user_id,a.provider,a.label,COALESCE(a.verified_subject,''),a.revision,i.id,i.location,i.state,i.epoch,i.credential_expires_at`

func scanAccount(row pgx.Row) (ProviderAccount, error) {
	var a ProviderAccount
	err := row.Scan(&a.ID, &a.OwnerUserID, &a.Provider, &a.Label, &a.VerifiedSubject, &a.Revision, &a.InstallationID, &a.Location, &a.State, &a.Epoch, &a.CredentialExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return a, err
}
func (s *Store) cloudEnabled() error {
	if s.providerCipher == nil {
		return ErrCloudUnavailable
	}
	return nil
}
func (s *Store) lockAccount(ctx context.Context, tx pgx.Tx, owner, id string) (ProviderAccount, error) {
	if !validID(id) || !validID(owner) {
		return ProviderAccount{}, ErrDenied
	}
	return scanAccount(tx.QueryRow(ctx, `SELECT `+accountColumns+` FROM provider_accounts a JOIN provider_installations i ON i.account_id=a.id WHERE a.id=$1 AND a.owner_user_id=$2 FOR UPDATE OF a,i`, id, owner))
}
func (s *Store) ProviderAccounts(ctx context.Context, actor auth.Actor) ([]ProviderAccount, error) {
	if err := s.cloudEnabled(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+accountColumns+` FROM provider_accounts a JOIN provider_installations i ON i.account_id=a.id WHERE a.owner_user_id=$1 ORDER BY a.created_at,a.id LIMIT 32`, actor.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderAccount{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func safeAccountText(value string, limit int) bool {
	return len(value) > 0 && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}
func (s *Store) CreateProviderAccount(ctx context.Context, actor auth.Actor, key string, input ProviderAccountInput) (ProviderAccount, error) {
	if err := s.cloudEnabled(); err != nil {
		return ProviderAccount{}, err
	}
	input.Label = strings.TrimSpace(input.Label)
	if (input.Provider != "Codex" && input.Provider != "Claude") || !safeAccountText(input.Label, 120) {
		return ProviderAccount{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProviderAccount{}, err
	}
	defer tx.Rollback(ctx)
	// Serialize a person's account additions to enforce the bound under concurrency.
	var owner string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, actor.UserID).Scan(&owner); err != nil {
		return ProviderAccount{}, err
	}
	var out ProviderAccount
	if hit, err := s.beginCommand(ctx, tx, actor, key, "provider.create", input, &out); err != nil {
		return out, err
	} else if hit {
		return scanAccount(tx.QueryRow(ctx, `SELECT `+accountColumns+` FROM provider_accounts a JOIN provider_installations i ON i.account_id=a.id WHERE a.id=$1 AND a.owner_user_id=$2`, out.ID, actor.UserID))
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM provider_accounts WHERE owner_user_id=$1`, actor.UserID).Scan(&count); err != nil {
		return out, err
	}
	if count >= 32 {
		return out, ErrInput
	}
	out = ProviderAccount{ID: uuid.NewString(), OwnerUserID: actor.UserID, Provider: input.Provider, Label: input.Label, Revision: 1, InstallationID: uuid.NewString(), Location: "cloud", State: "pending", Epoch: 1}
	if _, err = tx.Exec(ctx, `INSERT INTO provider_accounts(id,owner_user_id,provider,label) VALUES($1,$2,$3,$4)`, out.ID, actor.UserID, out.Provider, out.Label); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO provider_installations(id,account_id,owner_user_id,location) VALUES($1,$2,$3,'cloud')`, out.InstallationID, out.ID, actor.UserID); err != nil {
		return out, err
	}
	if err = s.finishCommand(ctx, tx, actor, key, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) CreateRunnerGrant(ctx context.Context, actor auth.Actor, id, key string) (RunnerGrant, error) {
	if err := s.cloudEnabled(); err != nil {
		return RunnerGrant{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RunnerGrant{}, err
	}
	defer tx.Rollback(ctx)
	a, err := s.lockAccount(ctx, tx, actor.UserID, id)
	if err != nil {
		return RunnerGrant{}, err
	}
	var out RunnerGrant
	if hit, err := s.beginCommand(ctx, tx, actor, key, "runner.grant:"+id, struct{}{}, &out); err != nil {
		return out, err
	} else if hit {
		// A replay may recover the secret only while this exact grant is still valid.
		var active bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runner_grants WHERE installation_id=$1 AND token_hash=$2 AND epoch=$3 AND revoked_at IS NULL AND NOT cleanup_only AND expires_at>clock_timestamp())`, a.InstallationID, auth.HashToken(out.Token), a.Epoch).Scan(&active)
		if err == nil && !active {
			err = ErrTransition
		}
		return out, err
	}
	if a.State == "disconnecting" || a.State == "disconnected" {
		return out, ErrTransition
	}
	if _, err = tx.Exec(ctx, `UPDATE runner_grants SET revoked_at=now() WHERE installation_id=$1 AND revoked_at IS NULL`, a.InstallationID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE provider_login_attempts SET state='failed',failure_code='runner_replaced',encrypted_challenge=NULL,encrypted_code=NULL,updated_at=now() WHERE installation_id=$1 AND state IN ('queued','running','waiting')`, a.InstallationID); err != nil {
		return out, err
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return out, err
	}
	out = RunnerGrant{InstallationID: a.InstallationID, Token: "campr_" + base64.RawURLEncoding.EncodeToString(random[:]), Epoch: a.Epoch}
	err = tx.QueryRow(ctx, `INSERT INTO runner_grants(id,installation_id,token_hash,epoch,expires_at) VALUES($1,$2,$3,$4,now()+interval '24 hours') RETURNING expires_at`, uuid.NewString(), a.InstallationID, auth.HashToken(out.Token), a.Epoch).Scan(&out.ExpiresAt)
	if err != nil {
		return out, err
	}
	if err = s.finishCommand(ctx, tx, actor, key, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) BeginProviderLogin(ctx context.Context, actor auth.Actor, id, key string) (ProviderLogin, error) {
	if err := s.cloudEnabled(); err != nil {
		return ProviderLogin{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProviderLogin{}, err
	}
	defer tx.Rollback(ctx)
	a, err := s.lockAccount(ctx, tx, actor.UserID, id)
	if err != nil {
		return ProviderLogin{}, err
	}
	var out ProviderLogin
	if hit, err := s.beginCommand(ctx, tx, actor, key, "provider.login:"+id, struct{}{}, &out); err != nil {
		return out, err
	} else if hit {
		out, err = s.readLogin(ctx, tx, a, out.ID)
		if err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	if a.State == "disconnecting" || a.State == "disconnected" {
		return out, ErrTransition
	}
	if err = s.expireLogins(ctx, tx, a.InstallationID); err != nil {
		return out, err
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provider_login_attempts WHERE installation_id=$1 AND state IN ('queued','running','waiting'))`, a.InstallationID).Scan(&busy); err != nil {
		return out, err
	}
	if busy {
		return out, ErrTransition
	}
	var grant string
	err = tx.QueryRow(ctx, `SELECT id FROM runner_grants WHERE installation_id=$1 AND epoch=$2 AND revoked_at IS NULL AND NOT cleanup_only AND expires_at>clock_timestamp()+interval '10 minutes' ORDER BY created_at DESC LIMIT 1`, a.InstallationID, a.Epoch).Scan(&grant)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrTransition
	}
	if err != nil {
		return out, err
	}
	out = ProviderLogin{ID: uuid.NewString(), InstallationID: a.InstallationID, State: "queued"}
	err = tx.QueryRow(ctx, `INSERT INTO provider_login_attempts(id,installation_id,grant_id,epoch,expires_at) VALUES($1,$2,$3,$4,now()+interval '10 minutes') RETURNING expires_at`, out.ID, a.InstallationID, grant, a.Epoch).Scan(&out.ExpiresAt)
	if err != nil {
		return out, err
	}
	if err = s.finishCommand(ctx, tx, actor, key, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) expireLogins(ctx context.Context, tx pgx.Tx, installation string) error {
	_, err := tx.Exec(ctx, `UPDATE provider_login_attempts SET state='expired',encrypted_challenge=NULL,encrypted_code=NULL,updated_at=now() WHERE installation_id=$1 AND state IN ('queued','running','waiting') AND expires_at<=clock_timestamp()`, installation)
	return err
}

// ReapProviderLogins removes expired one-time challenges even when neither the
// owner nor the runner returns. Each pass is bounded and safe across API pods.
func (s *Store) ReapProviderLogins(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `WITH expired AS (
 SELECT id FROM provider_login_attempts WHERE state IN ('queued','running','waiting') AND expires_at<=clock_timestamp()
 ORDER BY expires_at,id LIMIT 250 FOR UPDATE SKIP LOCKED
) UPDATE provider_login_attempts p SET state='expired',encrypted_challenge=NULL,encrypted_code=NULL,updated_at=now()
 FROM expired e WHERE p.id=e.id`)
	return err
}
func (s *Store) readLogin(ctx context.Context, tx pgx.Tx, a ProviderAccount, id string) (ProviderLogin, error) {
	if !validID(id) {
		return ProviderLogin{}, ErrDenied
	}
	if err := s.expireLogins(ctx, tx, a.InstallationID); err != nil {
		return ProviderLogin{}, err
	}
	var out ProviderLogin
	var encrypted []byte
	err := tx.QueryRow(ctx, `SELECT id,installation_id,state,failure_code,expires_at,encrypted_challenge,code_id IS NOT NULL,code_consumed_at IS NOT NULL FROM provider_login_attempts WHERE id=$1 AND installation_id=$2`, id, a.InstallationID).Scan(&out.ID, &out.InstallationID, &out.State, &out.FailureCode, &out.ExpiresAt, &encrypted, &out.CodeSubmitted, &out.CodeConsumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrDenied
	}
	if err != nil {
		return out, err
	}
	if len(encrypted) > 0 && out.State == "waiting" {
		plain, err := s.openProvider(encrypted, loginBinding(a, out.ID))
		if err != nil {
			return out, err
		}
		var challenge LoginChallenge
		if err = json.Unmarshal(plain, &challenge); err != nil {
			return out, err
		}
		out.URL, out.UserCode = challenge.URL, challenge.UserCode
	}
	return out, nil
}
func (s *Store) ProviderLogin(ctx context.Context, actor auth.Actor, id, attempt string) (ProviderLogin, error) {
	if err := s.cloudEnabled(); err != nil {
		return ProviderLogin{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProviderLogin{}, err
	}
	defer tx.Rollback(ctx)
	a, err := s.lockAccount(ctx, tx, actor.UserID, id)
	if err != nil {
		return ProviderLogin{}, err
	}
	out, err := s.readLogin(ctx, tx, a, attempt)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) CancelProviderLogin(ctx context.Context, actor auth.Actor, id, attempt, key string) (ProviderLogin, error) {
	if err := s.cloudEnabled(); err != nil {
		return ProviderLogin{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProviderLogin{}, err
	}
	defer tx.Rollback(ctx)
	a, err := s.lockAccount(ctx, tx, actor.UserID, id)
	if err != nil {
		return ProviderLogin{}, err
	}
	out, err := s.readLogin(ctx, tx, a, attempt)
	if err != nil {
		return out, err
	}
	var previous ProviderLogin
	if hit, err := s.beginCommand(ctx, tx, actor, key, "provider.cancel:"+id, attempt, &previous); err != nil {
		return out, err
	} else if hit {
		return out, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE provider_login_attempts SET state='cancelled',encrypted_challenge=NULL,encrypted_code=NULL,updated_at=now() WHERE id=$1 AND state IN ('queued','running','waiting')`, attempt); err != nil {
		return out, err
	}
	out, err = s.readLogin(ctx, tx, a, attempt)
	if err != nil {
		return out, err
	}
	if err = s.finishCommand(ctx, tx, actor, key, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) DisconnectProviderAccount(ctx context.Context, actor auth.Actor, id, key string) (ProviderAccount, error) {
	if err := s.cloudEnabled(); err != nil {
		return ProviderAccount{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProviderAccount{}, err
	}
	defer tx.Rollback(ctx)
	a, err := s.lockAccount(ctx, tx, actor.UserID, id)
	if err != nil {
		return a, err
	}
	var previous ProviderAccount
	if hit, err := s.beginCommand(ctx, tx, actor, key, "provider.disconnect:"+id, struct{}{}, &previous); err != nil {
		return a, err
	} else if hit {
		return a, nil
	}
	if a.State != "disconnecting" && a.State != "disconnected" {
		var provisioned bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runner_grants WHERE installation_id=$1)`, a.InstallationID).Scan(&provisioned); err != nil {
			return a, err
		}
		state := "disconnecting"
		if !provisioned {
			state = "disconnected"
		}
		// Existing runner grants retain only the ability to fetch and acknowledge
		// cleanup. All credential reads and login completion are fenced immediately.
		_, err = tx.Exec(ctx, `UPDATE provider_installations SET state=$2,epoch=epoch+1,encrypted_credentials=NULL,credential_expires_at=NULL,updated_at=now() WHERE id=$1`, a.InstallationID, state)
		if err != nil {
			return a, err
		}
		if _, err = tx.Exec(ctx, `UPDATE provider_accounts SET revision=revision+1 WHERE id=$1`, a.ID); err != nil {
			return a, err
		}
		if _, err = tx.Exec(ctx, `UPDATE runner_grants SET cleanup_only=true,epoch=$2 WHERE installation_id=$1 AND revoked_at IS NULL`, a.InstallationID, a.Epoch+1); err != nil {
			return a, err
		}
		if _, err = tx.Exec(ctx, `UPDATE provider_login_attempts SET state='cancelled',encrypted_challenge=NULL,encrypted_code=NULL,updated_at=now() WHERE installation_id=$1 AND state IN ('queued','running','waiting')`, a.InstallationID); err != nil {
			return a, err
		}
	}
	a, err = s.lockAccount(ctx, tx, actor.UserID, id)
	if err != nil {
		return a, err
	}
	if err = s.finishCommand(ctx, tx, actor, key, a); err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}
func (s *Store) sealProvider(plain []byte, binding string) ([]byte, error) {
	nonce := make([]byte, s.providerCipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.providerCipher.Seal(nonce, nonce, plain, []byte(binding)), nil
}
func (s *Store) openProvider(encrypted []byte, binding string) ([]byte, error) {
	n := s.providerCipher.NonceSize()
	if len(encrypted) < n {
		return nil, errors.New("invalid provider envelope")
	}
	plain, err := s.providerCipher.Open(nil, encrypted[:n], encrypted[n:], []byte(binding))
	if err != nil {
		return nil, errors.New("invalid provider envelope")
	}
	return plain, nil
}
func loginBinding(a ProviderAccount, id string) string {
	return "login:v1:" + a.OwnerUserID + ":" + a.ID + ":" + a.InstallationID + ":" + id
}
