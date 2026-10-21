# Camp server

V1 is under development. This first slice implements email-code sign-in,
revocable native-device credentials, Camp membership, invitation acceptance,
projects, atomic command receipts and an ordered event stream. It does not yet
implement shared session transcripts, execution requests, planning, memory or
skills. Do not deploy it as the complete V1 product.

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

Every Camp mutation requires a UUID `Idempotency-Key`. Reusing the same key for
the same operation and payload returns its original result; a changed operation
or payload returns `409 request_conflict`. Each receipt commits with the state
change and event. Replays still check current membership. Sensitive receipt
results, including invitation tokens, are encrypted using the protected auth key.

Event cursors are assigned under a Camp row lock, so they follow commit order.
Streams check device and membership validity while connected and close when
access is revoked. Clients reconnect with bounded backoff and the last applied
cursor. A `409 snapshot_required` response requires a fresh authorized snapshot.
SSE events currently contain membership and project metadata only; session-level
grants must be implemented before adding transcript payloads.

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
three-member permissions, ordered events, command retries and stream revocation.
Plain `go test ./...` skips PostgreSQL tests when that variable is absent.
