# GitHub connections

The GitHub integration connects each Camp user to their own GitHub App user
authorization. The native GitHub screen is shared between Manage sources and
onboarding. It signs in, reconnects, disconnects and browses authorized
installations and repositories. Repository links on a client project still
describe its work; those links do not grant access.

Isolated checkouts, execution grants, pushes and pull-request publication are
still being implemented. This connection flow does not enable those actions
or authorize a teammate to use someone else's Git identity.

The workspace integration will use GitHub's [scoped user-token endpoint](https://docs.github.com/en/rest/apps/apps#create-a-scoped-access-token)
to narrow each execution grant to its approved repository and permissions.
The broad user authorization stays in the API. This preserves user attribution
without giving a worker access to every repository or substituting an
installation identity. Scoped execution grants are not implemented by this
connection slice.

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

Access and refresh tokens are encrypted with owner, connection and epoch
binding. Human endpoints return only metadata and permitted repository views.
Shared Camp snapshots and events contain no Git credentials or connection
details. Browser callbacks return a fixed completion page without codes or
tokens. Disable query-string logging on the callback route in your ingress,
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
