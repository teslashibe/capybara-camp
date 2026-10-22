package camp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

var ErrRunnerCapacity = errors.New("runner capacity unavailable")
var ErrManualRunner = errors.New("installation uses an unmanaged grant")

// ControllerGrant is never returned by a human HTTP endpoint or logged.
type ControllerGrant struct {
	ID        string
	Account   ProviderAccount
	Token     string `json:"-"`
	ExpiresAt time.Time
}

func (s *Store) ControllerAccounts(ctx context.Context, after string, limit int) ([]ProviderAccount, error) {
	if err := s.cloudEnabled(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 64 || after != "" && !validID(after) {
		return nil, ErrInput
	}
	rows, err := s.pool.Query(ctx, `SELECT `+accountColumns+` FROM provider_accounts a JOIN provider_installations i ON i.account_id=a.id
 WHERE ($1::text='' OR a.id>NULLIF($1,'')::uuid) ORDER BY a.id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderAccount
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func grantBinding(a ProviderAccount, id string) string {
	return fmt.Sprintf("managed-grant:v1:%s:%s:%s:%d", a.OwnerUserID, a.InstallationID, id, a.Epoch)
}

// EnsureControllerGrant serializes mint/reuse without holding a transaction
// during any Kubernetes request. A stale controller cannot change a new epoch.
func (s *Store) EnsureControllerGrant(ctx context.Context, expected ProviderAccount, capacity int) (ControllerGrant, error) {
	if err := s.cloudEnabled(); err != nil {
		return ControllerGrant{}, err
	}
	if capacity < 1 || capacity > 1024 {
		return ControllerGrant{}, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ControllerGrant{}, err
	}
	defer tx.Rollback(ctx)
	a, err := s.lockAccount(ctx, tx, expected.OwnerUserID, expected.ID)
	if err != nil {
		return ControllerGrant{}, err
	}
	if a.Epoch != expected.Epoch || a.InstallationID != expected.InstallationID || a.State == "disconnecting" || a.State == "disconnected" {
		return ControllerGrant{}, ErrTransition
	}
	out := ControllerGrant{Account: a}
	var encrypted []byte
	err = tx.QueryRow(ctx, `SELECT id,encrypted_token,expires_at FROM runner_grants WHERE installation_id=$1 AND epoch=$2 AND revoked_at IS NULL AND NOT cleanup_only ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, a.InstallationID, a.Epoch).Scan(&out.ID, &encrypted, &out.ExpiresAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	now := time.Now()
	if err == nil {
		if len(encrypted) == 0 && out.ExpiresAt.After(now) {
			return out, ErrManualRunner
		}
		var busy bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provider_login_attempts WHERE installation_id=$1 AND state IN ('queued','running','waiting') AND expires_at>clock_timestamp()) OR EXISTS(SELECT 1 FROM ticket_workspaces WHERE provider_installation_id=$1 AND state='preparing' AND lease_until>clock_timestamp())`, a.InstallationID).Scan(&busy); err != nil {
			return out, err
		}
		if len(encrypted) > 0 && (out.ExpiresAt.After(now.Add(45*time.Minute)) || busy && out.ExpiresAt.After(now)) {
			plain, err := s.openProvider(encrypted, grantBinding(a, out.ID))
			if err != nil {
				return out, err
			}
			out.Token = string(plain)
			return out, tx.Commit(ctx)
		}
		if busy {
			return out, ErrTransition
		}
	}
	// This is an authorization-slot cap. Namespace ResourceQuota also bounds
	// physical Pods and storage while old workloads are being removed.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('capybara-runner-capacity'))`); err != nil {
		return out, err
	}
	var occupied int
	if err = tx.QueryRow(ctx, `SELECT count(DISTINCT installation_id) FROM runner_grants WHERE encrypted_token IS NOT NULL AND revoked_at IS NULL AND expires_at>clock_timestamp() AND installation_id<>$1`, a.InstallationID).Scan(&occupied); err != nil {
		return out, err
	}
	if occupied >= capacity {
		return out, ErrRunnerCapacity
	}
	if _, err = tx.Exec(ctx, `UPDATE runner_grants SET revoked_at=now(),encrypted_token=NULL WHERE installation_id=$1 AND revoked_at IS NULL`, a.InstallationID); err != nil {
		return out, err
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return out, err
	}
	out.ID = uuid.NewString()
	out.Token = "campr_" + base64.RawURLEncoding.EncodeToString(random[:])
	encrypted, err = s.sealProvider([]byte(out.Token), grantBinding(a, out.ID))
	if err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO runner_grants(id,installation_id,token_hash,epoch,expires_at,encrypted_token) VALUES($1,$2,$3,$4,now()+interval '24 hours',$5) RETURNING expires_at`, out.ID, a.InstallationID, auth.HashToken(out.Token), a.Epoch, encrypted).Scan(&out.ExpiresAt); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ControllerGrantCurrent(ctx context.Context, grant ControllerGrant) (bool, error) {
	var current bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runner_grants g JOIN provider_installations i ON i.id=g.installation_id
 WHERE g.id=$1 AND i.id=$2 AND i.epoch=$3 AND g.epoch=i.epoch AND g.revoked_at IS NULL AND NOT g.cleanup_only AND g.expires_at>clock_timestamp() AND i.state IN ('pending','connected','needs_login'))`, grant.ID, grant.Account.InstallationID, grant.Account.Epoch).Scan(&current)
	return current, err
}

func (s *Store) ControllerCleanupCurrent(ctx context.Context, expected ProviderAccount) (bool, error) {
	var current bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provider_installations WHERE id=$1 AND epoch=$2 AND state IN ('disconnecting','disconnected'))`, expected.InstallationID, expected.Epoch).Scan(&current)
	return current, err
}

func (s *Store) ControllerWorkloadStatus(ctx context.Context, expected ProviderAccount, state, code string) error {
	states := map[string]bool{"pending": true, "starting": true, "ready": true, "erasing": true, "erased": true, "failed": true}
	codes := map[string]bool{"": true, "capacity": true, "manual_grant": true, "cluster_unavailable": true, "resource_conflict": true, "workload_pending": true, "workload_failed": true}
	if !states[state] || !codes[code] {
		return ErrInput
	}
	_, err := s.pool.Exec(ctx, `UPDATE provider_installations SET workload_state=$3,workload_error=$4,updated_at=now()
 WHERE id=$1 AND epoch=$2 AND (workload_state<>$3 OR workload_error<>$4)`, expected.InstallationID, expected.Epoch, state, code)
	return err
}

// Called only after the controller has observed all managed Pods, volumes and
// Secrets for this installation gone. Old observations cannot clear a new epoch.
func (s *Store) ControllerErased(ctx context.Context, expected ProviderAccount) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	a, err := s.lockAccount(ctx, tx, expected.OwnerUserID, expected.ID)
	if err != nil {
		return err
	}
	if a.Epoch != expected.Epoch || a.State != "disconnecting" && a.State != "disconnected" {
		return ErrTransition
	}
	if _, err = tx.Exec(ctx, `UPDATE runner_grants SET revoked_at=now(),encrypted_token=NULL WHERE installation_id=$1 AND revoked_at IS NULL`, a.InstallationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE provider_installations SET state='disconnected',workload_state='erased',workload_error='',updated_at=now() WHERE id=$1 AND (state<>'disconnected' OR workload_state<>'erased' OR workload_error<>'')`, a.InstallationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
