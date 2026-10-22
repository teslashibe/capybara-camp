# Cloud provider account service

This is the first cloud account slice for V1. The Go service stores owner-bound
accounts, installation grants and login attempts. The official CLI login worker,
native account setup screen and cloud execution are still required. These
endpoints alone do not start a cloud provider session.

## Ownership and storage

An account belongs to one Camp user, independently of any Camp membership. Adding
four Codex accounts and two Claude accounts creates six separate cloud
installations. Labels never establish provider identity. The first successful
login pins the provider subject; reconnecting with a different subject fails and
preserves the working credentials. No account is merged by email or label.

Schema 9 adds `provider_accounts`, `provider_installations`, `runner_grants` and
`provider_login_attempts`. Existing local `profiles` and shared `sessions` keep
their existing ownership. Cloud account metadata does not enter Camp snapshots,
shared events or exported transcripts. There is a limit of 32 account records per
person in this initial service.

`CAMP_PROVIDER_ENCRYPTION_KEY` must contain a base64-encoded 32-byte key, supplied
outside source control. It is separate from `CAMP_AUTH_SECRET`. Missing keys leave
shared-history features available and cloud account operations disabled. Invalid
keys stop server startup. Back up this key separately and securely: restoring
PostgreSQL without it cannot restore provider credentials. Key rotation needs an
explicit re-encryption operation; replacing the key in place is not supported.

Credentials use AES-256-GCM with the owner, provider, account, installation and
credential revision as associated data. One-time browser challenges are also
encrypted and bound to their attempt. The server receives a bounded JSON
credential object from the trusted runner, never a filesystem archive. The
provider adapter will own its exact schema and allowed files. An account list
returns metadata only; there is no human credential-read API.

## Native owner API

All routes require the person's Camp device token. Writes require an
`Idempotency-Key` UUID. Requests use JSON with unknown fields rejected.

| Method and path | Behavior |
| --- | --- |
| `GET /v1/provider-accounts` | List only this person's accounts |
| `POST /v1/provider-accounts` | Add `{provider, label}` where provider is Codex or Claude |
| `POST /v1/provider-accounts/{account}/runner-grants` | Create one installation token, replacing its previous grant |
| `POST /v1/provider-accounts/{account}/logins` | Queue one login attempt for its current runner |
| `GET /v1/provider-accounts/{account}/logins/{attempt}` | Read state and the validated browser challenge |
| `POST /v1/provider-accounts/{account}/logins/{attempt}/cancel` | Cancel pending login and prevent late credential activation |
| `POST /v1/provider-accounts/{account}/disconnect` | Fence login/credential access and request runner erasure |

A runner grant lasts 24 hours, is installation-scoped and is stored only as a
hash, except its encrypted idempotency receipt. Replaying grant creation can
recover the same token while it is valid; it cannot revive an expired, replaced
or cleanup-only grant. Grant renewal and automatic provisioning still need their
controller integration before unattended execution ships.

The provisioning integration must deliver grants privately to isolated workloads.
A human Camp token must never be installed in a runner. Manual grant creation is
currently an internal API capability, not the final user onboarding flow.

## Runner API

These routes accept only the separate installation token. A runner token cannot
access `/v1/me`, any human account endpoint, or Camp history. Every read and write
checks the grant again under the same account lock used by owner changes.

| Method and path | Behavior |
| --- | --- |
| `GET /v1/runner/account` | Return only this installation's idle, login or erase action |
| `POST /v1/runner/logins/{attempt}/claim` | Claim a queued attempt with a persisted `claim_id` UUID |
| `POST /v1/runner/logins/{attempt}/report` | Report waiting, connected or failed with the same claim ID |
| `GET /v1/runner/credentials` | Restore only this installation's credential object and pinned subject |
| `POST /v1/runner/erased` | Acknowledge stopped processes and erased local provider state |

Only one claim ID can own an attempt. A replay with that ID reads its state; it
does not authorize starting the CLI twice. The worker must persist its claim and
reconcile process ownership after restart. A changed completion payload conflicts.
This is idempotent persistence, not an exactly-once provider login guarantee.

Attempts expire after ten minutes. Reads and claims enforce expiry; a bounded
maintenance pass clears expired challenges even when no client returns. Replacing
a runner invalidates its unfinished attempts. Cancelled or expired attempts cannot
activate credentials. A failed or cancelled reconnect retains the existing login.

Waiting reports contain only an allowlisted HTTPS provider URL and optional
device code. Codex uses the official device verification route. Claude URLs must
carry a PKCE authorization request with an allowed callback. Arbitrary CLI output,
URLs with embedded credentials and external redirects are rejected. Live CLI
compatibility tests are still required before enabling the native login UI.

Disconnect removes the server's credential copy and changes the current runner
grant to cleanup-only. It can then poll for erasure and acknowledge completion,
but cannot read credentials or complete a login. The account stays
`disconnecting` until erasure is acknowledged; the service does not claim that a
workload stopped merely because its database credentials were removed. After the
acknowledgment the grant is revoked and the account is `disconnected`. Workload
termination, handling an offline or expired runner grant, and credential-volume
removal must be implemented by the provisioning controller before release.
An account that has never received a runner grant can disconnect immediately.

## Validation

Real PostgreSQL integration tests cover six distinct installations, shared-Camp
member isolation, simultaneous create/claim requests, token replacement and
expiry, cancellation/completion races, identity mismatch, encrypted state binding,
cleanup-only access and HTTP token separation. These tests use synthetic provider
proofs. They do not establish successful Codex or Claude cloud sign-in or workload
isolation.
