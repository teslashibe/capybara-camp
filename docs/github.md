# GitHub connections

The GitHub integration connects each Camp user to their own GitHub App user
authorization. The native GitHub screen is shared between Manage sources and
onboarding. It signs in, reconnects, disconnects and browses authorized
installations and repositories. Repository links on a client project still
describe its work; those links do not grant access.

The Go service queues owner-bound workspace preparation and pins its repository,
commit, branch and identities. The Go runner prepares an isolated checkout.
Execution grants, pushes and pull-request publication remain pending. A queued workspace does
not authorize a teammate to use someone else's Git identity or start execution.

The Go provider adapter can now resolve a ref to an exact commit and request a
read-only token through GitHub's [scoped user-token endpoint](https://docs.github.com/en/rest/apps/apps#create-a-scoped-access-token).
It checks the returned user, permissions and single-repository access, and
revokes a rejected candidate. The broad user authorization stays in the API.
Workspace jobs and the isolated runner use those adapters. There is no
execution-grant endpoint yet.

## Workspace preparation

`POST /v1/camps/{camp}/sessions/{session}/workspaces` creates a preparation job.
The request includes `provider_account_id`, `git_connection_id`,
`github_installation_id`, `repository_id`, `installation_page`,
`repository_page`, `purpose`, `expected_session_revision` and
`expected_project_revision`. Both page numbers start at one. `ref` is optional;
omitting it uses the repository's default branch. Purpose is `planning` or
`implementation`.

The caller must own the session, provider account and GitHub connection, and
have a writable Camp role. The provider must match the session. GitHub must
return the selected installation and repository through that person's user
authorization. Planning requires read access; implementation also requires
write access. The repository must appear in the session's client project links.
The link is checked after GitHub authorizes access; it never grants access.

The service pins the resolved commit and generates a distinct `camp/` branch
for each new workspace. It records the original ref, project/session revisions,
GitHub actor and the current Git/provider credential epochs. Authorization and
revisions are checked again after the external reads. No database lock is held
while calling GitHub.

The `Idempotency-Key` must be a UUID. Repeating a committed request returns the
same current workspace without resolving a moving branch again. Safe GitHub
reads can repeat before the first commit; that does not create duplicate rows,
branches or events. Reusing a key with different input fails. At most one queued
workspace exists per session, with at most 25 per provider installation.

`GET /v1/camps/{camp}/workspaces/{workspace}` returns the owner's record.
`POST /v1/camps/{camp}/workspaces/{workspace}/cancel`, with an idempotency key
and `expected_revision`, cancels preparation. Cancellation preserves history;
a new preparation gets a new workspace and branch. Replaying the original
creation after cancellation returns the cancelled workspace. Private events
contain only the workspace/session IDs, state and revision, and are delivered
only to its owner.

Schema 13 adds these records without changing existing sessions or credentials.
A queued response means preparation was saved; it does not mean a checkout
exists, a CLI is running, or execution moved from the Mac. The control plane
supports worker claims and scoped checkout credentials as described below. The
runner reports ready only after checking out and validating the pinned commit.

## Worker claims and checkout credentials

Only an active installation grant can use `/v1/runner/workspaces/next` or the
`claim`, `heartbeat`, `token`, `status` and `report` POST routes beneath
`/v1/runner/workspaces/{workspace}`. Human Camp tokens cannot use these routes.
These preparation POST requests carry the same worker-generated `claim_id`.
Each installation has at most one preparing workspace or active provider turn.
It may retain several ready ticket checkouts. [The scheduler](runner-turns.md#multiple-tickets-on-one-account)
selects queued work across those tickets without mixing their provider histories.

A claim leases preparation for 90 seconds with a fixed ten-minute deadline.
Heartbeats extend the lease within that deadline. A different claim or a
replacement runner grant cannot adopt it. The worker saves its claim ID before
submitting a claim and reconciles a lost reply using that same ID. The status
route returns metadata only to the grant and claim that own the preparation.
The next-work route also returns ready workspaces so a restarted worker can
verify retained files without another checkout. Cleanup checks small batches
of local IDs through the separate `/v1/runner/workspaces/retired` route; a ready
workspace is not deleted when another ticket is selected.

The token route revalidates Camp membership, source/project revisions, account
epochs, and the selected GitHub installation/repository. It then requests a
repository-scoped read-only token using the owner's user authorization. The
broad GitHub token stays in the service. The scoped token is encrypted with the
workspace, owner, claim, grant and Git epoch bound to it; it appears only in the
private token response. Public workspace records and events omit it.

The issuance fence is committed before the external token call. Repeated token
requests return the saved token to the same active claim. An uncertain issuance
fails that preparation rather than minting a second token. Cancellation or an
authorization change during issuance refuses the candidate and queues its
revocation. Expired claims cannot renew or obtain a token.

A ready report must repeat the exact pinned commit and branch. Identical reports
are retryable; conflicting terminal reports fail. Completion and cancellation
erase the cached token and make its revocation due. A revocation is also queued
for its permitted expiry when the token is first saved. The existing bounded
Git cleanup worker performs those calls. Reaping closes stale leases and
workspaces whose membership, source, project, account or Git authority changed.
Cloud inference uses the separate [provider-turn protocol](runner-turns.md).
Git publication and live execution acceptance remain unfinished.

Schema 14 adds these claims and retains older preparation records. New records
also retain their installation/repository page selections. Older records use
page one when revalidating; an old paginated selection that is no longer found
must be cancelled and prepared again, with no alternate repository substituted.

Local validation uses disposable PostgreSQL databases. It covers concurrent
creation, response-loss replay, cancellation and restart, private events,
cross-owner account/connection access, repository permissions, invalid commits,
and Git/provider/source/membership changes during the external read. HTTP tests
exercise authentication, strict input decoding, owner-only reads and cancellation.
Claim tests also cover competing workers, two tickets on one installation,
heartbeat deadlines, one-time token issuance, cached redelivery, cancellation
during issuance, terminal-report replay, stale grants and expiry cleanup. The
HTTP tests follow a complete claim/token/report sequence and verify that the
token is absent from the owner's ordinary workspace response.
The upgrade check retains the prior Git and provider credential bytes and
epochs. Runner tests use real Git over a local smart-HTTP server with disposable
PostgreSQL. They cover dropped claim, token and report replies, a restart during
fetch, and cancellation while Git is blocked. These checks do not prove a
cloud provider turn or two-user GitHub operation.

## Isolated checkout

`CAMP_RUNNER_WORKSPACE_DIR` points to a private directory separate from the
provider state and grant file. The worker holds an exclusive directory lock,
checks ownership and permissions, and rejects overlapping paths, including
aliases through symlinks. Each ticket gets its own `workspace-<UUID>` directory.

The worker runs a shallow fetch of the exact commit, validates the tree, and
creates the server-assigned branch. It disables Git hooks, global/system
configuration, credential stores, submodule recursion and HTTP redirects.
Only HTTPS GitHub transport is enabled in production. The temporary credential
helper returns the read token only for the authorized repository path and
expiry. Tokens stay out of command arguments, remotes, journals and logs. The
temporary token file is removed before reporting ready; restart removes any
abandoned token files.

Preparation permits at most 20,000 files, 32 MiB per file and 512 MiB of tree
content. Unsafe paths, escaping symlinks and submodules are rejected before
checkout. Subprocess output and execution time are bounded; the deployment's
1 GiB workspace volume also bounds downloaded pack storage. Cancellation,
lease loss and shutdown kill and join the Git process group. All limits apply
to preparation; larger repositories need an explicit product decision.

An owner-only, atomically written journal stores the claim and checkout
metadata, without credentials. A restart can repeat a partial safe fetch with
the same claim and cached token. Once a ready report is saved, it verifies the
existing commit, branch and clean worktree and retries that report without
fetching again. A missing or modified prepared checkout fails visibly. A
confirmed cancellation removes the managed preparation directory. This loop
does not invoke a model, run repository code, push, or transfer Mac execution.

## Self-hosted registration

Register a GitHub App for your deployment. Configure these URLs against the
same public HTTPS origin as `CAMP_PUBLIC_URL`:

- User authorization callback: `https://YOUR_CAMP/v1/git/github/callback`
- Webhook: `https://YOUR_CAMP/v1/git/github/events`, with JSON payloads and a
  random webhook secret of at least 32 characters

Keep expiring user tokens enabled. Configure repository Metadata read, Contents
read/write and Pull requests read/write for the intended implementation flow.
A read-only installation remains suitable for reading/planning; it cannot
publish. Avoid unrelated permissions such as administration, secrets or actions.
Each repository owner chooses which repositories receive the installation.
Each person separately authorizes the app for their GitHub identity.

Set all five optional API settings together:

```text
CAMP_GITHUB_CLIENT_ID
CAMP_GITHUB_CLIENT_SECRET
CAMP_GITHUB_APP_SLUG
CAMP_GITHUB_ENCRYPTION_KEY
CAMP_GITHUB_WEBHOOK_SECRET
```

The encryption key is a base64-encoded random 32-byte key, separate from the
provider-account and Camp authentication keys. Keep it in your encrypted
operator secret store and disaster-recovery backup. These settings go only to
the API; the controller and provider workloads do not receive them. The public
Kubernetes API template includes optional Secret references for each setting.
No GitHub App private key or installation access token is used by this flow.

Apply schema 12 before starting the matching API and controller builds. Existing
schema-11 binaries are not a rollback target after migration. The feature is
disabled when no GitHub settings are supplied; partially configured settings
fail startup.

## Authorization and recovery

OAuth uses a random, expiring state and PKCE. The exact configured callback is
bound into the authorization and token exchange. An owner can recover the
pending login from another app launch. Browser URLs are not stored in the
native setup journal, which retains only operation and connection IDs. The
server stores the pending verifier encrypted and accepts one callback claim.
Cancel, disconnect, expiry and connection revisions fence late completions.

The browser callback does not exchange the code. It hands the state and code
to the Camp app on the same Mac through a `capybara://github-login` link, and
the app completes the login over its signed-in Camp session. The server
exchanges the code only when that signed-in person started the login, so a
teammate who approves someone else's authorize link cannot bind their GitHub
account to that person's connection.

Access and refresh tokens are encrypted with owner, connection and epoch
binding. Human endpoints return only metadata and permitted repository views.
Shared Camp snapshots and events contain no Git credentials or connection
details. The callback page carries the code only in its link back to the
local app and sends no referrer. Disable query-string logging on the callback route in your ingress,
proxy and observability configuration; OAuth codes and state appear in its URL.

Repository browsing uses GitHub App user tokens. The API checks the chosen
installation against the user's current installation list, then intersects the
installation's Contents/Pull requests permissions with the user's repository
permissions. A link, shared Camp membership or public repository visibility is
not a substitute for that authorization. An expired SAML session or missing
organization approval requires action on GitHub.

Refresh is claimed before the external call. Concurrent callers wait; they
cannot spend the same one-use refresh token. If the refresh outcome is unknown
or the process dies during it, Camp requires a new sign-in instead of repeating
the refresh. Signed `github_app_authorization` revocation webhooks immediately
fence matching connections. Delivery IDs and payload hashes make repeated
deliveries idempotent without persisting webhook bodies.

Disconnect immediately prevents further use in Camp and queues deletion of the
app token at GitHub. That external cleanup is retried independently and may
still be pending while GitHub is unavailable. The encrypted cleanup record is
removed after success or after both tokens expire. A failed reconnect preserves
the existing identity and credentials; an identity mismatch does not replace it.

## Local validation and remaining gates

PostgreSQL and HTTPS fixtures cover owner isolation, state and callback replay,
encrypted persistence, cancellation races, identity mismatch, refresh recovery,
repository permission intersection and signed revocation. Native tests cover
the recoverable setup journal and late replies after sign-out. Real GitHub App
registration, organization/SSO approval and two-user repository operations must
be exercised before the V1 release.

Provider contracts: [GitHub App user authorization](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app),
[token refresh](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens),
and [installation repository access](https://docs.github.com/en/rest/apps/installations).
