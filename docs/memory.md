# Project memory

Memory remains in development. The Camp observation API and embedded Cortex
adapter are implemented; consolidation job routing and the native memory
interface are not. [Cortex PR #1](https://github.com/teslashibe/cortex/pull/1)
is merged and supplies the exact-selection and permission-guard API needed by
[the memory issue](https://github.com/teslashibe/capybara-camp/issues/5).

## Current observation API

- `POST /v1/camps/{camp}/memory/observations` saves `{project_id, visibility,
  title, source}` with an `Idempotency-Key`. Visibility is `private` or `project`;
  `source` is the existing session/revision/permission/message-ID reference
- `GET /v1/camps/{camp}/memory/observations?project_id=…&visibility=private&limit=50`
  returns metadata and a `next` cursor, with at most 100 entries per page
- `GET /v1/camps/{camp}/memory/observations/{id}` returns the selected content and
  full source reference after checking current permissions

Members with write permission can save observations. Viewers can read permitted
project observations. Private observations belong to their creator. A project
save requires a shared source; saving into a project does not share a private
session. Each observation contains up to 120 messages, at most 48 KiB per
message and 256 KiB total serialized content. A namespace holds at most 10,000
observations. Lists contain metadata; full text loads only when opened.

Identical source snapshots converge to one observation within their scope.
Changed content with a reused request key conflicts. Camp persists the selected
snapshot before copying it into Cortex. `stored: false` retains a pending copy;
retrying the same save reconciles it without another observation. This operation
does not call a model. Completion emits a permission-filtered update event.

Revoking a source hides project observations from reads, lists and event replay.
Sharing that source again does not revive observations made under the older
grant; explicitly save a new snapshot. Owners retain their own private copies
when they stop sharing a source.

## Storage and access

Use the embedded Cortex Go library for observations, consolidation, recall and
provenance. Camp owns authentication, permissions, job routing and the native
interface. Keep Cortex tables in a separate schema in the same PostgreSQL
database as Camp, so permission checks and memory commits share a transaction.
Do not expose Cortex's caller-trusting HTTP service.

Derive each namespace from the authenticated Camp, project and visibility.
Private namespaces also include the owner's identity. Clients choose Private
or Project; they never supply Cortex's `user_id`. Project memory requires
explicit sharing. A Camp member's provider credentials remain separate from
memory content and are never stored in Cortex.

Start with explicit Save to memory and manual consolidation. Save pinned
message ranges with their source session, revision, message IDs and content
digest. Reuse the private history-copy flow for local sessions that have not
been published. Do not automatically harvest conversations. A memory's access
depends on every source contributing to it, including sources inherited from
an older reinforced memory. Recheck those permissions on search, inspection,
recall, event replay and consolidation; remove inaccessible cached content when
revocation is observed.

## Consolidation

Use `Engine.ConsolidateSelected`, not the automatic pending-queue selection of
the standalone service. The caller supplies an exact batch of at most 200
observations and at most 500 accessible existing memories. The complete model
request must fit within 1 MiB. Oversized selections require review; they are not
silently truncated. This entry point makes one model call and does not invoke
an automatic format-repair model or another consolidation chunk.

Persist a job before execution. Freeze its namespace, selected IDs and source
grants, provider account, Local/Cloud destination, model, effort, speed and
permissions. Use a dedicated conversation with project tools disabled. The
existing working conversation must not receive consolidation instructions.
The provider runs through its official CLI on the chosen Mac or isolated cloud
installation. An unavailable destination stays unavailable; it does not cause
account or provider fallback.

The Cortex guard runs before inference and again inside the commit
transaction. It must lock Camp's policy rows and validate source access in the
same transaction that applies memories and acknowledges observations. No
database transaction stays open during the provider call. The guard also
provides the Cortex run ID and the hash of the exact model/messages; retain
these in the Camp job for recovery and provenance.

Commit revocation in Camp's policy transaction, then perform any Cortex
cleanup separately. Do not wait for a Cortex mutation while holding Camp
policy locks; an active consolidation may need those same locks to finish its
permission check. Access checks hide revoked material immediately, even when
cleanup is deferred.

Cancellation or malformed output leaves observations pending. A dropped
response does not prove the provider did no work. Use the stored job and
`Store.Consolidation` outcome to reconcile; never redispatch an uncertain
attempt automatically. Provider usage may be unavailable and must remain
labelled as such.

## Native interface and verification

Follow Shared memory in the approved interactive design. Reuse the existing
model catalogue and Model/Effort/Speed component, source inspector, execution
destination control and notification history. Show the selected provider
account, pending observations, active jobs and consolidated memories with
their revisions and source links. Keep private and project scopes explicit.

The Cortex dependency has Go tests for exact selection, private namespaces,
foreign inputs, changed sources, malformed output, cancellation and a
revocation racing the commit. Camp's observation tests cover cross-user and
cross-Camp access, duplicate saves, pending-copy recovery, bounded pagination
and revocation during copying. The copy releases Camp's policy lock before
entering Cortex and checks permissions again before announcing completion.
Routing tests, the native interface, real CLI consolidation, job recovery and
the two-Mac source-revocation walkthrough remain V1 release gates.
