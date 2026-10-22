# Cloud provider account service

The Go service stores owner-bound accounts, installation grants and login
attempts. Go sign-in adapters now drive the official CLIs inside disposable
private profiles. An auth-only Go worker now claims those attempts, relays codes
and persists recovery state. Native account setup uses these endpoints in Manage
sources and onboarding. Workload provisioning and cloud execution are still
required. These endpoints alone do
not start a cloud chat session.

## Ownership and storage

An account belongs to one Camp user, independently of any Camp membership. Adding
four Codex accounts and two Claude accounts creates six separate cloud
installations. Labels never establish provider identity. The first successful
login pins the provider subject; reconnecting with a different subject fails and
preserves the working credentials. No account is merged by email or label.

Schema 9 adds `provider_accounts`, `provider_installations`, `runner_grants` and
`provider_login_attempts`. Schema 10 adds encrypted Claude authorization-code
delivery. Existing local `profiles` and shared `sessions` keep
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
| `GET /v1/provider-accounts/{account}` | Read the account and its latest sign-in attempt after opening or relaunching the app |
| `POST /v1/provider-accounts` | Add `{provider, label}` where provider is Codex or Claude |
| `POST /v1/provider-accounts/{account}/runner-grants` | Create one installation token, replacing its previous grant |
| `POST /v1/provider-accounts/{account}/logins` | Queue one login attempt for its current runner |
| `GET /v1/provider-accounts/{account}/logins/{attempt}` | Read state and the validated browser challenge |
| `POST /v1/provider-accounts/{account}/logins/{attempt}/code` | Submit `{code}` once for this waiting Claude login |
| `POST /v1/provider-accounts/{account}/logins/{attempt}/cancel` | Cancel pending login and prevent late credential activation |
| `POST /v1/provider-accounts/{account}/disconnect` | Fence login/credential access and request runner erasure |
| `POST /v1/provider-accounts/{account}/reactivate` | Return an erased account to pending with a fresh installation epoch |

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
| `POST /v1/runner/logins/{attempt}/code` | Read a Claude code with the owning `claim_id` and a persisted `delivery_id` |
| `POST /v1/runner/logins/{attempt}/code/ack` | Erase that delivered code using its `claim_id`, `delivery_id` and `code_id` |
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

## Claude authorization-code delivery

Only the account owner can submit a browser return code. The login must already
be waiting on its validated Claude challenge. Submission accepts at most 4 KiB,
rejects whitespace and control characters, and stores the code encrypted with
the owner, account, installation, attempt and code ID as associated data. Codes
never enter shared Camp state, owner read responses or command receipts. The
receipt stores a hash of the submitted request and encrypted status metadata.
One attempt accepts one submission; a retry with the same idempotency key and
payload returns current status. Correcting a code requires a new login attempt.

Only that login's claimed runner can fetch its code. The runner persists a
delivery UUID before the request. Repeating that UUID returns the same code until
acknowledgment; a different UUID conflicts. A `204` means no code is waiting.
Acknowledgment removes the ciphertext and is safe to retry with the same IDs.
The runner must persist that it is about to deliver the code and acknowledge it
before writing it to the CLI. After a crash with uncertain provider delivery, it
must fail the attempt and ask for a new sign-in instead of replaying the code.
The `code_consumed` flag means the server erased the code, not that the provider
accepted it. Only a verified connected result establishes sign-in.

The challenge is fixed once published. Changed challenges conflict, preventing
an already-submitted code from being rebound to another browser flow. Completion,
failure, cancellation, expiry, grant replacement and account disconnect all erase
any pending code. Every take and acknowledgment rechecks ownership, grant, claim,
epoch and attempt state under the account lock. The auth-only worker implements
this delivery protocol. The native form keeps code entry in memory and does not
save it in its setup journal.

Waiting reports contain only an allowlisted HTTPS provider URL and optional
device code. Codex uses the official device verification route. Claude URLs must
carry a PKCE authorization request for the verified Claude Code subscription
client with an allowed callback. Both the current `claude.com/cai/oauth/authorize`
route and the older `claude.ai/oauth/authorize` route are supported. Arbitrary CLI output,
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

Reconnect after a completed disconnect reactivates the same account and
installation, preserving its pinned provider identity and history. It requires
`disconnected` state and no unrevoked grants. The operation increments the
installation epoch once and requires a new runner grant; it cannot restore old
credentials or revive a previous worker. Replays of an old reactivation request
return current state without undoing a later disconnect. A different provider
identity still fails verification. The native form persists the reactivation
request before sending it and then waits for provisioning before sign-in.

## Validation

Real PostgreSQL integration tests cover six distinct installations, shared-Camp
member isolation, simultaneous create/claim requests, token replacement and
expiry, cancellation/completion races, identity mismatch, encrypted state binding,
cleanup-only access and HTTP token separation. These tests use synthetic provider
proofs. They do not establish successful Codex or Claude cloud sign-in or workload
isolation.

The sign-in adapters use an empty private HOME and provider directory, discard
ambient credentials and set a fixed non-browser command for headless login.
Codex uses its app-server device-code flow and verifies `account/read` after the
matching completion event. Claude uses `auth login --claudeai`, accepts one
authorization code through stdin and verifies `auth status --json`. Identity is
extracted from the fresh CLI-owned credential state after these checks. This is
not an independent JWT verification API for caller-supplied credentials.

Snapshots include only fixed provider credential files, with a 256 KiB total
limit. Reads reject symlinks, pipes and foreign-owned files. The candidate profile
is removed on success, cancellation or failure; a cleanup failure is reported.
The auth-only runner encrypts and persists a successful snapshot before reporting
completion and handles uncertain delivery without repeating provider sign-in.

Tests exercise each adapter with fake CLIs, credential identity checks, inherited
credential rejection, one-code input, cancellation and profile cleanup. An opt-in
preflight also exercised the installed Claude CLI's actual authorization URL and
cancelled before signing in. It proves URL compatibility, not a completed login.

## Auth-only worker

`server/cmd/camp-runner` polls only its installation routes and invokes the fixed
Go sign-in adapters. It cannot read human Camp history, execute prompts or accept
shell commands. Configure it through these environment variables:

| Variable | Purpose |
| --- | --- |
| `CAMP_RUNNER_ORIGIN` | HTTPS Camp service origin |
| `CAMP_RUNNER_GRANT_FILE` | Absolute path to an owner-only regular file containing its installation grant |
| `CAMP_RUNNER_STATE_DIR` | Existing owner-only directory dedicated to this installation |

Keep the grant file outside the state directory. The worker rejects symlink
grant files, shared state permissions, concurrent workers using the same state
directory, redirects and mismatched installation responses. Its API calls have
bounded deadlines and response sizes. Provider processes receive the adapter's
restricted environment, not the Camp grant or arbitrary host credentials.

The encrypted journal stores a claim UUID before claiming work and a start fence
before invoking a provider. A prepared claim can be resumed with the same UUID.
After restart, a started attempt without a saved result fails and requires a new
sign-in; it never launches another provider process for that attempt. A saved
final result is retried with identical content. If the server already committed
it and the response was lost, the next idle state permits local cleanup.

Claude code delivery persists its delivery UUID and a delivery-intent fence,
acknowledges server erasure, then writes the code once to the CLI. A restart with
uncertain code delivery fails the attempt. Cancellation and disconnect cancel
and join the provider before cleanup or erasure acknowledgment. A lost network
connection cancels an active login after a bounded interval.

The journal is encrypted with a key derived from the random installation grant
and bound to the service origin. Grant rotation requires the provisioning
controller to stop the old workload and remove its journal before replacing the
grant. Changing a grant in place cannot decrypt the previous journal and fails
closed. No grant-renewal controller has shipped yet.

Run one worker per isolated installation container. The container supervisor must
terminate all processes before a replacement worker starts; a filesystem lock
alone cannot prove that a killed worker's children have stopped. Startup removes
only this adapter's abandoned scratch profiles, under that container lifecycle
contract. Host execution after an ungraceful kill is not a supported recovery
path. The future Kubernetes controller must enforce this lifecycle and handle
offline workers and expired grants before cloud execution is enabled.

PostgreSQL/HTTPS fixture tests exercise both providers' full worker paths,
one-time-code delivery, lost acknowledgments, durable result retries, interrupted
login recovery, cancellation and cleanup ordering. The provider calls in these
tests are fakes. Real cloud sign-in and runtime/container isolation acceptance
remain release gates.

## Native account setup

Manage sources separates Local profiles and Cloud accounts. The account form is
also the optional Accounts step in onboarding. Camp sign-in identifies the owner;
each provider account still requires its own provider consent. The native client
checks account ownership, installation identity, provider URLs and bounded
response shapes before displaying them. It never opens an arbitrary URL returned
by the server.

Creating an account records a stable request ID in the owner's private cache
before sending it. A lost response or relaunch can recover that same account.
The form waits for a runner before beginning sign-in, and recovers the current
attempt through the owner-only status endpoint. Pending starts and the selected
account survive relaunch. The journal contains no Camp token, provider credential
or authorization code. Signing out clears the visible account state, and late
responses from an older owner or connection cannot restore it.

Only the visible account form polls for status. It shows queued, waiting,
cancelled, expired, failed and connected states. Disconnect remains pending until
the runner acknowledges erasure; unavailable usage is labeled explicitly. A
server without cloud account storage shows that configuration is unavailable.
The provisioning controller is still required to prepare runners automatically;
the native form does not manufacture a connected state while waiting for one.
