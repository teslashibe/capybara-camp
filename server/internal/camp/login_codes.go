package camp

import (
	"context"
	"errors"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

type ProviderCodeInput struct {
	Code string `json:"code"`
}

// The code is returned only to its claimed installation. Owner reads expose
// submission/consumption flags, never the code or its ciphertext.
type RunnerLoginCode struct {
	CodeID string `json:"code_id"`
	Code   string `json:"code"`
}

func validProviderCode(code string) bool {
	return len(code) >= 8 && len(code) <= 4096 && utf8.ValidString(code) &&
		!containsCodeSpace(code)
}

func containsCodeSpace(code string) bool {
	for _, r := range code {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func codeBinding(a ProviderAccount, attempt, codeID string) string {
	return loginBinding(a, attempt) + ":code:" + codeID
}

func (s *Store) SubmitProviderLoginCode(ctx context.Context, actor auth.Actor, id, attempt, key string, input ProviderCodeInput) (ProviderLogin, error) {
	if err := s.cloudEnabled(); err != nil {
		return ProviderLogin{}, err
	}
	if !validProviderCode(input.Code) {
		return ProviderLogin{}, ErrInput
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
	if a.Provider != "Claude" || a.State == "disconnecting" || a.State == "disconnected" {
		return ProviderLogin{}, ErrTransition
	}
	out, err := s.readLogin(ctx, tx, a, attempt)
	if err != nil {
		return out, err
	}
	var receipt ProviderLogin
	if hit, err := s.beginCommand(ctx, tx, actor, key, "provider.code:"+id+":"+attempt, input, &receipt); err != nil {
		return out, err
	} else if hit {
		return out, tx.Commit(ctx)
	}
	if out.State != "waiting" || out.CodeSubmitted || out.URL == "" {
		return out, ErrTransition
	}
	// The account lock serializes this with grant replacement. Require the same
	// still-live grant even if the owner submits near the attempt deadline.
	var live bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provider_login_attempts p JOIN runner_grants g ON g.id=p.grant_id
 WHERE p.id=$1 AND g.revoked_at IS NULL AND NOT g.cleanup_only AND g.epoch=$2 AND g.expires_at>clock_timestamp())`, attempt, a.Epoch).Scan(&live); err != nil {
		return out, err
	}
	if !live {
		return out, ErrTransition
	}
	codeID := uuid.NewString()
	encrypted, err := s.sealProvider([]byte(input.Code), codeBinding(a, attempt, codeID))
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE provider_login_attempts SET code_id=$2,encrypted_code=$3,updated_at=now() WHERE id=$1`, attempt, codeID, encrypted); err != nil {
		return out, err
	}
	out.CodeSubmitted = true
	if err = s.finishCommand(ctx, tx, actor, key, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) TakeRunnerLoginCode(ctx context.Context, token, attempt, claim, delivery string) (*RunnerLoginCode, error) {
	if err := s.cloudEnabled(); err != nil {
		return nil, err
	}
	if !validID(attempt) || !validID(claim) || !validID(delivery) {
		return nil, ErrInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	r, err := s.lockRunner(ctx, tx, token)
	if err != nil {
		return nil, err
	}
	if r.cleanup || r.account.Provider != "Claude" {
		return nil, ErrDenied
	}
	if err = s.expireLogins(ctx, tx, r.account.InstallationID); err != nil {
		return nil, err
	}
	var codeID, oldDelivery *string
	var encrypted []byte
	err = tx.QueryRow(ctx, `SELECT code_id,code_delivery_id,encrypted_code FROM provider_login_attempts
 WHERE id=$1 AND claim_id=$2 AND installation_id=$3 AND grant_id=$4 AND epoch=$5 AND state='waiting' FOR UPDATE`, attempt, claim, r.account.InstallationID, r.grantID, r.account.Epoch).Scan(&codeID, &oldDelivery, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	if oldDelivery != nil && *oldDelivery != delivery {
		return nil, ErrConflict
	}
	if codeID == nil || len(encrypted) == 0 {
		return nil, tx.Commit(ctx)
	}
	plain, err := s.openProvider(encrypted, codeBinding(r.account, attempt, *codeID))
	if err != nil {
		return nil, err
	}
	if !validProviderCode(string(plain)) {
		return nil, ErrInput
	}
	if _, err = tx.Exec(ctx, `UPDATE provider_login_attempts SET code_delivery_id=$2,updated_at=now() WHERE id=$1`, attempt, delivery); err != nil {
		return nil, err
	}
	return &RunnerLoginCode{CodeID: *codeID, Code: string(plain)}, tx.Commit(ctx)
}

// Acknowledge before handing the code to the CLI. A lost HTTP response can be
// retried with the same IDs; a worker crash must fail the attempt instead of
// guessing whether the provider consumed a code. This is not provider exactly-once.
func (s *Store) AcknowledgeRunnerLoginCode(ctx context.Context, token, attempt, claim, delivery, codeID string) error {
	if err := s.cloudEnabled(); err != nil {
		return err
	}
	if !validID(attempt) || !validID(claim) || !validID(delivery) || !validID(codeID) {
		return ErrInput
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
	if r.cleanup || r.account.Provider != "Claude" {
		return ErrDenied
	}
	if err = s.expireLogins(ctx, tx, r.account.InstallationID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE provider_login_attempts SET encrypted_code=NULL,code_consumed_at=COALESCE(code_consumed_at,now()),updated_at=now()
 WHERE id=$1 AND claim_id=$2 AND installation_id=$3 AND grant_id=$4 AND epoch=$5 AND state='waiting' AND code_id=$6 AND code_delivery_id=$7`, attempt, claim, r.account.InstallationID, r.grantID, r.account.Epoch, codeID, delivery)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrDenied
	}
	return tx.Commit(ctx)
}
