# Execution assignments

The Go API stores a Local or Cloud assignment for each session. This is a
control-plane implementation, tested with simulated workers. The runner still
needs provider-turn consumption, streaming and control relay before cloud chat
is usable. The native location control is not enabled by these endpoints.

## Assignment and consent

`GET /v1/camps/{camp}/sessions/{session}/execution` returns the owner's location,
assignment epoch, workspace, mode and permission settings. `available: false`
preserves the selected Cloud destination when its account or workspace becomes
unavailable. Availability checks the stored authorization, not process liveness.
It does not select a replacement account or fall back to a Mac.

`PUT` to the same path requires an idempotency key, `location`,
`expected_epoch`, `expected_session_revision` and `expected_transcript_revision`.
Cloud additionally requires `workspace_id` and `expected_workspace_revision`.
The caller must own the session and have a writable Camp role. A cloud workspace
must be ready, belong to that session and owner, and retain valid project,
provider-account and Git authority. An implementation workspace also requires
`execution_approved: true` and explicit supported CLI permission settings.
The runtime must verify that the installed CLI accepts those settings before
submitting a prompt.

The assignment epoch changes independently of the display revision. The
approved repository and history snapshot are preserved. A workspace is
prepared for one session revision, and an unassigned preparation fails if the
session changes. Once it is the session's assigned execution workspace, board
moves and sharing changes no longer fail it; a sharing or permission change
cancels running turns through the permission revision instead. Every new prompt
freezes its destination, epoch, workspace, mode and permissions. Pending,
queued, claimed, running or uncertain requests prevent reassignment; they must
first be cancelled or reconciled. Replaying an old assignment command observes
the current destination and never moves work back.

Returning to Local selects the original owning Mac. Moving to a different Mac,
copying dirty files, and bidirectional transfer review remain separate work.
This endpoint does not copy files, migrate a provider's process or start a turn.
Once an implementation has started, changing to another workspace or Local is
held until the reviewed transfer flow is implemented, so edits cannot be stranded.

## Worker authority and recovery

The existing `prompt_requests` table remains the single queue. Local workers
can claim only Local requests. A cloud runner claims through
`POST /v1/runner/workspaces/{workspace}/requests/claim`, using a persisted
`claim_id`. The service verifies its installation grant, active Cloud assignment
and request permissions before returning a prompt. The first claim binds the
request to that grant. The same claim can recover a lost response; another
installation or a replacement grant cannot adopt it.

`POST /v1/runner/workspaces/{workspace}/requests/{request}/actions` supports
start, heartbeat, release before start, finish and reconciliation of a saved
result. Actions use idempotency keys and the claimed lease token. They share
the Local request state machine. A claim does not invoke a provider. The worker
must persist its start fence and receive Start acknowledgment before submission.
An expired started request becomes `outcome_unknown`, blocks another start and
requires reconciliation. A newly issued runner grant cannot retry that provider
operation. The owner can explicitly reconcile it through the existing request
action endpoint without receiving the private worker lease token.

Cloud start moves the session to In progress; completion, failure and uncertain
outcomes update its activity and notifications. Stale Mac activity or transcript
writes cannot overwrite Cloud execution. Sharing can still be revoked from an
older Mac runtime snapshot, which fences further worker actions.
An independent activity revision orders these updates without changing the
approved repository snapshot. The native live state and SQLite cache reject a
delayed response with older activity, including a delayed idle state.

The provisioning controller sees only installation IDs and lease times through
`runner_execution_leases`. It defers normal credential rotation during a live
cloud turn. It has no access to prompt bodies through that view.

Before enabling the provider consumer, extend checkout recovery to preserve
implementation edits and commits across process restarts, revocation and
workspace retirement. The current preparation cleanup rules are for unused
checkouts. The consumer also needs durable streamed output, narrowly scoped
context reads, approval/control delivery and verified provider-session recovery.

## Validation and deployment

Schema 15 is additive and preserves existing sessions as Local with epoch one.
Stop schema-14 writers before migration; all writers must then run a
schema-15-compatible build. Existing request, transcript,
Git and provider histories remain in place. The controller needs the GRANT block
from `00015_execution_assignments.sql` if its database role is created after
migration. An older binary is not a rollback target after this migration.

Disposable PostgreSQL and HTTP tests cover assignment/claim serialization,
owner and installation isolation, permission consent, response replay, stale
Mac writes, revoked sharing, unavailable destinations, expired starts and owner
reconciliation. These are API tests, not proof of a real cloud model turn.
Native cache and worker tests also cover delayed activity responses and the
existing claim/start/finish recovery contract.
