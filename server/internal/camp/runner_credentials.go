package camp

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/teslashibe/capybara-camp/server/internal/localcli"
)

type RunnerCredentialUpdate struct {
	ExpectedRevision int64           `json:"expected_revision"`
	Credentials      json.RawMessage `json:"credentials"`
}

// The official CLI owns renewal. Persist its finished snapshot only for the
// same verified installation and credential revision. Repeated delivery of an
// identical snapshot is safe; it cannot overwrite a newer login.
func (s *Store) UpdateRunnerCredentials(ctx context.Context, token string, in RunnerCredentialUpdate) (RunnerCredentials, error) {
	if in.ExpectedRevision < 1 {
		return RunnerCredentials{}, ErrInput
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
	if localcli.ValidateCredentialSnapshot(r.account.Provider, r.account.VerifiedSubject, in.Credentials) != nil {
		return RunnerCredentials{}, ErrInput
	}
	out := RunnerCredentials{InstallationID: r.account.InstallationID, Provider: r.account.Provider, VerifiedSubject: r.account.VerifiedSubject, Epoch: r.account.Epoch}
	var encrypted []byte
	if err = tx.QueryRow(ctx, `SELECT credential_revision,encrypted_credentials FROM provider_installations WHERE id=$1`, r.account.InstallationID).Scan(&out.Revision, &encrypted); err != nil {
		return out, err
	}
	current, err := s.openProvider(encrypted, credentialBinding(r.account, out.Revision))
	if err != nil {
		return out, err
	}
	var compact bytes.Buffer
	if json.Compact(&compact, in.Credentials) != nil {
		return out, ErrInput
	}
	out.Credentials = compact.Bytes()
	var old bytes.Buffer
	_ = json.Compact(&old, current)
	if out.Revision != in.ExpectedRevision {
		if out.Revision == in.ExpectedRevision+1 && bytes.Equal(old.Bytes(), out.Credentials) {
			return out, nil
		}
		return out, ErrRevision
	}
	if bytes.Equal(old.Bytes(), out.Credentials) {
		return out, nil
	}
	out.Revision++
	encrypted, err = s.sealProvider(out.Credentials, credentialBinding(r.account, out.Revision))
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE provider_installations SET credential_revision=$2,encrypted_credentials=$3,updated_at=now() WHERE id=$1`, r.account.InstallationID, out.Revision, encrypted); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
