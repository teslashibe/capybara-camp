# Cloud provider turns

This is an unreleased local implementation. No provider account has run a live
cloud turn and no production deployment has been performed.

The installation runner consumes requests from its assigned, prepared Git
workspace. It uses the same Go Codex app-server and Claude stream-JSON adapters
as the Mac. The assignment fixes the installation, workspace, execution epoch,
permission mode and request context revision. The original Mac cannot claim a
cloud request.

Before starting a provider, the runner saves its claim and start receipt keys.
It saves each outbound event before sending it, then advances its cursor after
the server acknowledges the event. A restart replays an unacknowledged event
with the same sequence and content. A process that may have started is never
started again automatically. Expired running work becomes `outcome_unknown`
and requires review. Terminal receipts can be retried independently of provider
work and credential persistence.

The server stores a bounded event log and publishes activity through the Camp
stream. The Mac subscribes while its chat is open, fetches new event pages and
renders response text before completion. Closing the chat drops that projection.
Reopening replays the server log. A committed transcript replaces the projection
without rendering the answer twice. The stream head contains display metadata;
it does not expose another person's queued prompt, account or execution lease.
Shared-session access is checked on every read.

The existing native approval component renders cloud approvals. Only the
session owner can answer. Answers and steering are bound to the running request,
provider request ID and, for approvals, the original event sequence. Pending
control content is encrypted and erased when resolved. The CLI validates the
actual provider decision and questions. Rejected approval answers can be
corrected. The native chat now exposes cloud steering through the same controls
as local chat, with a durable outbox for steering and approval answers. See
[cloud chat controls](cloud-controls.md) for delivery and recovery behavior.

Terminal publication preserves response boundaries and accepted steering in
conversation order. Output pages, events, transcript chunks and per-turn work
are bounded. The worker cancels and joins provider processes on cancellation,
authority loss and deadline. Modified workspaces survive normal worker recovery;
preparation does not delete a used working tree.

## Multiple tickets on one account

Each ticket retains its own checkout, branch and provider conversation ID. One
installation worker processes them serially. A ready checkout does not reserve
the worker; queued preparation and eligible prompts compete by their creation
time. An active request or preparation is resumed before selecting another
ticket. Login, preparation and provider execution cannot overlap on that account.

The database account lock fences competing claims, including requests for
different tickets. A started request with an unknown outcome blocks more work
on the installation until it is reconciled. The worker recovers its saved turn
and terminal receipt before consulting the scheduler, so switching tickets
cannot strand a completion or repeat a provider turn.

Checkout cleanup asks `POST /v1/runner/workspaces/retired` about batches of at
most 64 local workspace IDs. Only an authenticated successful reply can retire
an unused checkout. Queued, preparing and ready workspaces are retained;
unavailable or malformed replies do not delete anything. A used checkout stays
on disk for reviewed transfer or account erasure even after its server record
is retired. The endpoint returns no other account's workspace metadata.

Schema 17 permits multiple ready workspaces per installation while retaining a
unique preparing workspace. It adds request/workspace scheduling indexes and
preserves existing prepared checkouts. Upgrade the API and every runner with
the matching schema; older runners assume only one retained checkout and must
be stopped before enabling multiple tickets. Review [self-hosting](self-hosting.md)
before deployment. Live multi-ticket cloud acceptance remains a release gate.

## Provider credentials

The official CLI renews its own OAuth credentials. The runner restores only the
fixed credential files into the installation's private home. Updates must match
the previously verified provider identity and use the credential revision that
was read. An identical retry is accepted; an older snapshot cannot overwrite a
newer login. Credentials are encrypted in PostgreSQL and never included in
stream events, request metadata or logs. This does not guarantee that a provider
login will remain valid indefinitely.

## Validation and remaining gates

Real subprocess fixtures exercise both provider protocols with PostgreSQL and a
real local Git HTTP checkout. They cover streaming and terminal persistence,
approvals, cancellation, lost start/finish replies, and process restart without
repeating uncertain work. Additional checks cover shared read versus control
permissions, event replay, response boundaries, credential revision/identity
checks and symlink-safe fixed-file restoration.

Cloud subprocesses now enter a mandatory Linux filesystem and process-control
domain before executing provider or Git code. It protects the worker's control
files and grant while preserving the provider's private home and the assigned
workspace. The local adversarial check has passed; enforcement by the chosen
Kubernetes runtime and live provider behavior remain release gates. See
[process isolation](process-isolation.md) for the exact boundary and required checks.

The native [Run on controls](execution-locations.md) now prepare and assign a
private cloud copy. Also pending: execution defaults,
reviewed transfers, cloud usage
presentation, Git publication and actor attribution,
real provider logins/turns with the Mac offline, and two-user cloud acceptance.

Local verification for this slice: `tools/test-server.sh` passed the full Go
race suite and vet against disposable PostgreSQL 17. Follow-up subprocess tests
also passed two sequential requests for each provider, with Codex resuming the
saved provider thread. Native regression results and UI evidence are recorded
in `docs/ui-audit.md`. These results do not satisfy the live isolation gate.

The multi-ticket regression runs three synthetic turns across two checkouts
for each provider. It checks distinct provider session IDs, continuation on the
right ticket and a dropped completion response without an extra provider turn.
Additional tests cover retained unused checkouts across restart, explicit
retirement, malformed cleanup replies, competing claims and unknown-outcome
fencing. PostgreSQL migration tests preserve existing ready workspace metadata.
