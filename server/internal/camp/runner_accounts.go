package camp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

type LoginChallenge struct {
	URL      string `json:"url"`
	UserCode string `json:"user_code,omitempty"`
}
type RunnerAccountState struct {
	InstallationID string         `json:"installation_id"`
	Provider       string         `json:"provider"`
	Epoch          int64          `json:"epoch"`
	Action         string         `json:"action"`
	Login          *ProviderLogin `json:"login,omitempty"`
}
type RunnerLoginReport struct {
	State               string          `json:"state"`
	Challenge           *LoginChallenge `json:"challenge,omitempty"`
	VerifiedSubject     string          `json:"verified_subject,omitempty"`
	Credentials         json.RawMessage `json:"credentials,omitempty"`
	CredentialExpiresAt *time.Time      `json:"credential_expires_at,omitempty"`
}
type RunnerCredentials struct {
	InstallationID  string          `json:"installation_id"`
	Provider        string          `json:"provider"`
	VerifiedSubject string          `json:"verified_subject"`
	Epoch           int64           `json:"epoch"`
	Revision        int64           `json:"revision"`
	Credentials     json.RawMessage `json:"credentials"`
}
type runnerAccount struct {
	account ProviderAccount
	grantID string
	cleanup bool
}

// Reauthenticate under the same account lock as owner writes. A token observed
// before rotation/disconnect cannot authorize a later credential read or write.
func (s *Store) lockRunner(ctx context.Context, tx pgx.Tx, token string) (runnerAccount, error) {
	var r runnerAccount
	if len(token) != 49 || !strings.HasPrefix(token, "campr_") {
		return r, auth.ErrUnauthorized
	}
	var owner, id string
	err := tx.QueryRow(ctx, `SELECT a.owner_user_id,a.id FROM runner_grants g JOIN provider_installations i ON i.id=g.installation_id JOIN provider_accounts a ON a.id=i.account_id WHERE g.token_hash=$1`, auth.HashToken(token)).Scan(&owner, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, auth.ErrUnauthorized
	}
	if err != nil {
		return r, err
	}
	r.account, err = s.lockAccount(ctx, tx, owner, id)
	if err != nil {
		return r, err
	}
	err = tx.QueryRow(ctx, `SELECT id,cleanup_only FROM runner_grants WHERE token_hash=$1 AND installation_id=$2 AND epoch=$3 AND revoked_at IS NULL AND expires_at>clock_timestamp() FOR UPDATE`, auth.HashToken(token), r.account.InstallationID, r.account.Epoch).Scan(&r.grantID, &r.cleanup)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, auth.ErrUnauthorized
	}
	if err != nil {
		return r, err
	}
	if r.account.State == "disconnected" || (r.account.State == "disconnecting") != r.cleanup {
		return r, auth.ErrUnauthorized
	}
	return r, nil
}
func (s *Store) RunnerAccount(ctx context.Context, token string) (RunnerAccountState, error) {
	if err := s.cloudEnabled(); err != nil {
		return RunnerAccountState{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RunnerAccountState{}, err
	}
	defer tx.Rollback(ctx)
	r, err := s.lockRunner(ctx, tx, token)
	if err != nil {
		return RunnerAccountState{}, err
	}
	out := RunnerAccountState{InstallationID: r.account.InstallationID, Provider: r.account.Provider, Epoch: r.account.Epoch, Action: "idle"}
	if r.cleanup {
		out.Action = "erase"
	} else {
		if err = s.expireLogins(ctx, tx, r.account.InstallationID); err != nil {
			return out, err
		}
		var attempt string
		err = tx.QueryRow(ctx, `SELECT id FROM provider_login_attempts WHERE installation_id=$1 AND grant_id=$2 AND epoch=$3 AND state IN ('queued','running','waiting') ORDER BY created_at DESC LIMIT 1`, r.account.InstallationID, r.grantID, r.account.Epoch).Scan(&attempt)
		if err == nil {
			login, e := s.readLogin(ctx, tx, r.account, attempt)
			if e != nil {
				return out, e
			}
			out.Action = "login"
			out.Login = &login
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ClaimRunnerLogin(ctx context.Context, token, attempt, claim string) (ProviderLogin, error) {
	if err := s.cloudEnabled(); err != nil {
		return ProviderLogin{}, err
	}
	if !validID(attempt) || !validID(claim) {
		return ProviderLogin{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProviderLogin{}, err
	}
	defer tx.Rollback(ctx)
	r, err := s.lockRunner(ctx, tx, token)
	if err != nil {
		return ProviderLogin{}, err
	}
	if r.cleanup {
		return ProviderLogin{}, ErrDenied
	}
	if err = s.expireLogins(ctx, tx, r.account.InstallationID); err != nil {
		return ProviderLogin{}, err
	}
	var state string
	var oldClaim *string
	err = tx.QueryRow(ctx, `SELECT state,claim_id FROM provider_login_attempts WHERE id=$1 AND installation_id=$2 AND grant_id=$3 AND epoch=$4 FOR UPDATE`, attempt, r.account.InstallationID, r.grantID, r.account.Epoch).Scan(&state, &oldClaim)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderLogin{}, ErrDenied
	}
	if err != nil {
		return ProviderLogin{}, err
	}
	if state != "queued" && (oldClaim == nil || *oldClaim != claim || (state != "running" && state != "waiting")) {
		return ProviderLogin{}, ErrTransition
	}
	if _, err = tx.Exec(ctx, `UPDATE provider_login_attempts SET state='running',claim_id=$2,updated_at=now() WHERE id=$1 AND state='queued'`, attempt, claim); err != nil {
		return ProviderLogin{}, err
	}
	out, err := s.readLogin(ctx, tx, r.account, attempt)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func validateLoginChallenge(provider string, c LoginChallenge) bool {
	if len(c.URL) > 4096 || len(c.UserCode) > 32 || strings.ContainsAny(c.URL, "\r\n\t") {
		return false
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" || u.RawPath != "" {
		return false
	}
	if provider == "Codex" {
		if u.Host != "auth.openai.com" || u.Path != "/codex/device" || u.RawQuery != "" || len(c.UserCode) < 4 {
			return false
		}
		for _, r := range c.UserCode {
			if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
		return true
	}
	if provider != "Claude" || u.Host != "claude.ai" || u.Path != "/oauth/authorize" || c.UserCode != "" {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	allowed := map[string]bool{"client_id": true, "response_type": true, "redirect_uri": true, "scope": true, "state": true, "code_challenge": true, "code_challenge_method": true}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			return false
		}
	}
	if q.Get("response_type") != "code" || !validID(q.Get("client_id")) || len(q.Get("state")) < 16 || len(q.Get("state")) > 256 || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 {
		return false
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.User != nil || redirect.RawQuery != "" || redirect.Fragment != "" {
		return false
	}
	if redirect.String() == "https://console.anthropic.com/oauth/code/callback" || redirect.String() == "https://platform.claude.com/oauth/code/callback" {
		return true
	}
	port, e := strconv.Atoi(redirect.Port())
	return e == nil && port > 0 && port <= 65535 && redirect.Scheme == "http" && (redirect.Hostname() == "localhost" || redirect.Hostname() == "127.0.0.1") && redirect.Path == "/callback" && redirect.RawPath == ""
}
func credentialBinding(a ProviderAccount, revision int64) string {
	return fmt.Sprintf("credentials:v1:%s:%s:%s:%s:%d", a.OwnerUserID, a.ID, a.InstallationID, a.Provider, revision)
}
func validLoginReport(provider string, in RunnerLoginReport) bool {
	switch in.State {
	case "waiting":
		return in.Challenge != nil && validateLoginChallenge(provider, *in.Challenge) && in.VerifiedSubject == "" && len(in.Credentials) == 0 && in.CredentialExpiresAt == nil
	case "failed":
		return in.Challenge == nil && in.VerifiedSubject == "" && len(in.Credentials) == 0 && in.CredentialExpiresAt == nil
	case "connected":
		if in.Challenge != nil || !safeAccountText(in.VerifiedSubject, 256) || len(in.Credentials) == 0 || len(in.Credentials) > 256<<10 {
			return false
		}
		// Only the provider adapter interprets this object. No archive paths or
		// arbitrary file writes are exposed by the control-plane transport.
		var value map[string]json.RawMessage
		return json.Unmarshal(in.Credentials, &value) == nil && len(value) > 0
	default:
		return false
	}
}
func (s *Store) ReportRunnerLogin(ctx context.Context, token, attempt, claim string, in RunnerLoginReport) (ProviderLogin, error) {
	if err := s.cloudEnabled(); err != nil {
		return ProviderLogin{}, err
	}
	if !validID(attempt) || !validID(claim) {
		return ProviderLogin{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProviderLogin{}, err
	}
	defer tx.Rollback(ctx)
	r, err := s.lockRunner(ctx, tx, token)
	if err != nil {
		return ProviderLogin{}, err
	}
	if r.cleanup {
		return ProviderLogin{}, ErrDenied
	}
	if !validLoginReport(r.account.Provider, in) {
		return ProviderLogin{}, ErrInput
	}
	if in.CredentialExpiresAt != nil && (!in.CredentialExpiresAt.After(time.Now()) || in.CredentialExpiresAt.After(time.Now().AddDate(2, 0, 0))) {
		return ProviderLogin{}, ErrInput
	}
	if err = s.expireLogins(ctx, tx, r.account.InstallationID); err != nil {
		return ProviderLogin{}, err
	}
	var state string
	var oldHash []byte
	err = tx.QueryRow(ctx, `SELECT state,result_hash FROM provider_login_attempts WHERE id=$1 AND claim_id=$2 AND installation_id=$3 AND grant_id=$4 AND epoch=$5 FOR UPDATE`, attempt, claim, r.account.InstallationID, r.grantID, r.account.Epoch).Scan(&state, &oldHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderLogin{}, ErrDenied
	}
	if err != nil {
		return ProviderLogin{}, err
	}
	payload, _ := json.Marshal(in)
	hash := sha256.Sum256(payload)
	if state == "connected" || state == "failed" {
		if subtle.ConstantTimeCompare(oldHash, hash[:]) != 1 {
			return ProviderLogin{}, ErrConflict
		}
		return s.readLogin(ctx, tx, r.account, attempt)
	}
	if state != "running" && state != "waiting" {
		return ProviderLogin{}, ErrTransition
	}
	if in.State == "waiting" {
		payload, _ = json.Marshal(in.Challenge)
		encrypted, e := s.sealProvider(payload, loginBinding(r.account, attempt))
		if e != nil {
			return ProviderLogin{}, e
		}
		if _, err = tx.Exec(ctx, `UPDATE provider_login_attempts SET state='waiting',encrypted_challenge=$2,updated_at=now() WHERE id=$1`, attempt, encrypted); err != nil {
			return ProviderLogin{}, err
		}
	} else {
		result, failure := in.State, ""
		if in.State == "failed" {
			failure = "provider_rejected"
		}
		if in.State == "connected" && r.account.VerifiedSubject != "" && r.account.VerifiedSubject != in.VerifiedSubject {
			result, failure = "failed", "identity_mismatch"
		}
		if result == "connected" {
			var revision int64
			if err = tx.QueryRow(ctx, `SELECT credential_revision+1 FROM provider_installations WHERE id=$1`, r.account.InstallationID).Scan(&revision); err != nil {
				return ProviderLogin{}, err
			}
			encrypted, e := s.sealProvider(in.Credentials, credentialBinding(r.account, revision))
			if e != nil {
				return ProviderLogin{}, e
			}
			if _, err = tx.Exec(ctx, `UPDATE provider_installations SET state='connected',credential_revision=$2,encrypted_credentials=$3,credential_expires_at=$4,updated_at=now() WHERE id=$1`, r.account.InstallationID, revision, encrypted, in.CredentialExpiresAt); err != nil {
				return ProviderLogin{}, err
			}
			if _, err = tx.Exec(ctx, `UPDATE provider_accounts SET verified_subject=$2,revision=revision+1 WHERE id=$1`, r.account.ID, in.VerifiedSubject); err != nil {
				return ProviderLogin{}, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE provider_login_attempts SET state=$2,failure_code=$3,result_hash=$4,encrypted_challenge=NULL,updated_at=now() WHERE id=$1`, attempt, result, failure, hash[:]); err != nil {
			return ProviderLogin{}, err
		}
	}
	out, err := s.readLogin(ctx, tx, r.account, attempt)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) RunnerCredentialSnapshot(ctx context.Context, token string) (RunnerCredentials, error) {
	if err := s.cloudEnabled(); err != nil {
		return RunnerCredentials{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RunnerCredentials{}, err
	}
	defer tx.Rollback(ctx)
	r, err := s.lockRunner(ctx, tx, token)
	if err != nil {
		return RunnerCredentials{}, err
	}
	if r.cleanup || r.account.State != "connected" {
		return RunnerCredentials{}, ErrDenied
	}
	out := RunnerCredentials{InstallationID: r.account.InstallationID, Provider: r.account.Provider, VerifiedSubject: r.account.VerifiedSubject, Epoch: r.account.Epoch}
	var encrypted []byte
	if err = tx.QueryRow(ctx, `SELECT credential_revision,encrypted_credentials FROM provider_installations WHERE id=$1`, r.account.InstallationID).Scan(&out.Revision, &encrypted); err != nil {
		return out, err
	}
	plain, err := s.openProvider(encrypted, credentialBinding(r.account, out.Revision))
	if err != nil {
		return out, err
	}
	out.Credentials = json.RawMessage(plain)
	return out, tx.Commit(ctx)
}
func (s *Store) AcknowledgeRunnerErased(ctx context.Context, token string) error {
	if err := s.cloudEnabled(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	r, err := s.lockRunner(ctx, tx, token)
	if err != nil {
		return err
	}
	if !r.cleanup {
		return ErrTransition
	}
	if _, err = tx.Exec(ctx, `UPDATE provider_installations SET state='disconnected',updated_at=now() WHERE id=$1 AND state='disconnecting'`, r.account.InstallationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE runner_grants SET revoked_at=now() WHERE installation_id=$1 AND revoked_at IS NULL`, r.account.InstallationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE provider_accounts SET revision=revision+1 WHERE id=$1`, r.account.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
