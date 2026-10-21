# Camp server

V1 is under development. This first slice implements email-code sign-in,
revocable native-device credentials, Camp membership, invitation acceptance,
projects, selected session sharing, versioned transcripts, durable prompt
requests, atomic command receipts, recipient notification history with shared
read receipts and an ordered event stream. Native execution,
planning, memory and skills are still being built. Do not deploy it as the
complete V1 product.

## Run locally

Use a dedicated PostgreSQL 17 database and environment configuration:

| Variable | Purpose |
| --- | --- |
| `CAMP_DATABASE_URL` | Database credentials for this process |
| `CAMP_ADDRESS` | Listen address, default `127.0.0.1:8096` |
| `CAMP_PUBLIC_URL` | HTTPS service origin for native clients |
| `CAMP_AUTH_SECRET` | At least 32 random characters; protect and back up |
| `CAMP_RESEND_API_KEY` | Operator's Resend key for email-code delivery |
| `CAMP_EMAIL_FROM` | Verified email sender |

Run `go run ./cmd/migrate` explicitly, then `go run ./cmd/camp-server`.
The API never runs migrations during startup. `/health` is process liveness;
`/ready` requires the exact schema expected by the binary. Missing or incompatible
schemas stop startup. For Kubernetes, see [self-hosting](../docs/self-hosting.md).

## Native API contract

JSON bodies reject unknown fields and trailing documents, with a 64 KiB limit.
Responses contain no token except the successful sign-in response and newly
created invitation. Do not put credentials in URLs or logging middleware.
Use `Authorization: Bearer <device-token>` after sign-in.

| Method and path | Input or result |
| --- | --- |
| `POST /v1/auth/code` | `{email}`; generic accepted response |
| `POST /v1/auth/verify` | `{email, code, device_name}`; actor, device token and expiry |
| `POST /v1/auth/renew` | Extend this active device, subject to its absolute lifetime |
| `POST /v1/auth/logout` | Revoke this device |
| `GET /v1/me` | Current authenticated user and device |
| `GET /v1/camps` | Accessible Camps |
| `POST /v1/camps` | `{name}` |
| `GET /v1/camps/{camp}/snapshot` | Current Camp, members, projects and cursor |
| `POST /v1/camps/{camp}/projects` | `{name}` |
| `POST /v1/camps/{camp}/invitations` | Owner-only `{email, role}`; role is editor or viewer |
| `POST /v1/invitations/accept` | `{token}`; verifies the signed-in email |
| `DELETE /v1/camps/{camp}/members/{user}` | Owner-only removal of a non-owner member |
| `GET /v1/camps/{camp}/events` | SSE, with `Last-Event-ID` or `after` cursor |
| `GET /v1/camps/{camp}/notifications` | Recipient inbox, `limit` up to 100 and optional `before` cursor |
| `GET /v1/camps/{camp}/notifications/{id}` | Authorized saved completion target |
| `POST /v1/camps/{camp}/notifications/actions` | `{ids, action}`; `read` or `archive`, up to 1,000 IDs, with `Idempotency-Key` |
| `POST /v1/profiles` | `{local_id, provider, label}`; `local_id` is a SHA-256 hex identifier, never a filesystem path |
| `GET /v1/camps/{camp}/sessions` | Authorized search with `q`, `limit` and opaque `before` cursor; returns `sessions` and `next` |
| `PUT /v1/camps/{camp}/sessions/{id}` | Owner-device metadata, explicit sharing and expected revision |
| `PATCH /v1/camps/{camp}/sessions/{id}/board` | Owner/editor stage change with expected revision; preserves execution ownership |
| `POST /v1/camps/{camp}/sessions/{id}/ownership` | Same-user, explicit device adoption after active and uncertain work is resolved |
| `GET /v1/camps/{camp}/sessions/{id}` | Current authorized metadata |
| `POST /v1/camps/{camp}/sessions/{id}/transcript` | Expected transcript revision and up to 200 changed messages |
| `GET /v1/camps/{camp}/sessions/{id}/transcript` | Pinned `revision`, `before` ordinal and `limit`; returns a bounded message page |
| `POST /v1/camps/{camp}/sessions/{id}/requests` | Prompt, model, effort, speed and pinned source references |
| `GET /v1/camps/{camp}/requests` | Latest 100 requests created by or addressed to this user |
| `GET /v1/camps/{camp}/requests/{id}` | Current request state; excludes its worker lease token |
| `POST /v1/camps/{camp}/requests/claim` | Claim one eligible request on its owning device |
| `POST /v1/camps/{camp}/requests/{id}/actions` | Approve, start, heartbeat, cancel, finish or reconcile |

Every Camp mutation requires a UUID `Idempotency-Key`. Reusing the same key for
the same operation and payload returns its original result; a changed operation
or payload returns `409 request_conflict`. Each receipt commits with the state
change and event. Replays still check current membership. Sensitive receipt
results, including invitation tokens, are encrypted using the protected auth key.

Event cursors are assigned under a Camp row lock, so they follow commit order.
Streams check device and membership validity while connected and close when
access is revoked. Clients reconnect with bounded backoff and the last applied
cursor. A `409 snapshot_required` response requires a fresh authorized snapshot.
Session events contain an ID and revision, not transcript text. Fetching the
content checks current access again. Unsharing filters earlier session events
from replay and sends existing members an ID-only removal notice for their
cache. New members do not receive that old removal notice. Initially fetch the
Camp snapshot, save its cursor, list authorized sessions, then replay from that
cursor. Apply each entity revision once, including removal notices.

Only an owning device can publish a session or update its transcript. New
uploads require explicit sharing; later unsharing preserves the owner's cloud
history and denies other members. Provider profiles are metadata, not verified
provider account identities. Private local imports are not uploaded by default.

Transcript writes have immutable revisions and preserve each message's original
model/tuning metadata. Reads accept a pinned revision and are bounded to 200
messages and roughly 1 MiB of text per page; larger history remains available
through pagination. A write can contain at most 1 MiB of message text. Conflicting
metadata or transcript revisions return `409 revision_conflict` for explicit
reload/rebase. Reads always recheck current access, including old revisions.

A new device login does not inherit execution ownership. The same user may
explicitly adopt a session using a profile on the new device. Adoption preserves
its transcript, invalidates old queued requests and increments the permission
revision. Active, uncertain, or externally controlled sessions cannot move.

The email-code store hashes codes, limits attempts, consumes atomically, and
throttles resends. The public auth boundary also limits requests by the direct
remote address. Forwarded-IP headers are not trusted. A production proxy needs a
corresponding edge rate limit so one proxy IP does not become a shared limit.
Device tokens are hashed in PostgreSQL, last 30 days and can renew within an
absolute 90-day lifetime; expired/revoked devices sign in again.

## Validation

From the repository root, run `./tools/test-server.sh`. It creates a disposable
loopback PostgreSQL container, runs tests with Go's race detector and vets the
code, then removes that container. An explicit `CAMP_TEST_DATABASE_URL` instead
uses isolated schemas in the supplied test database. Never set it to production.

The tests exercise code replay/concurrency, device revocation, invitations,
three-member permissions, ordered events, command retries, bounded streams,
lease expiry, cancellation races and source revocation. HTTP tests cover the
request lifecycle; these are not evidence of a real CLI execution yet.
Plain `go test ./...` skips PostgreSQL tests when that variable is absent.


## Prompt request lifecycle

A request freezes the prompt, model settings, target device, permission revision
and pinned source transcript revisions. Another person's session requires both
permission to request work and owner approval. Both participants need source
access, and a shared destination cannot silently receive private context.
Sharing or project-permission changes cancel queued work and request cancellation
of active work. Titles and new transcript messages do not invalidate approval.

Requests move through `pending_approval`, `queued`, `claimed` and `started`.
`cancel_requested` asks the owner to interrupt the CLI. `completed`, `failed` and
`cancelled` are terminal. Each mutation has a monotonic revision; clients discard
older responses and events. A late Cancel is a no-op and cannot emit a second
completion event.

The 60-second claim lease should be renewed every 15 seconds with a new
idempotency key. Expiry before Start can return a request to the queue. Expiry
after Start becomes `outcome_unknown`, which blocks another execution of that
session. Cancellation cannot remove that fence. Reconciliation requires the
original lease and evidence of the observed result.

Only the assigned device receives the claim token. Before invoking a provider,
the native worker must persist its local attempt and acknowledge Start. It must
not launch another CLI because a network response was lost. Repeated commands
preserve their committed effect and return current state; a replayed expired
claim cannot authorize work. Sources are checked again before starting.

The server does not run CLIs or guarantee exactly-once provider work. Native
outbox persistence, streaming and provider reconciliation remain required before
this queue can execute real prompts. Externally owned terminal sessions remain
queued until safely released. One bounded recovery loop checks expired leases.
