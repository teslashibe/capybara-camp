# Project memory

Memory remains in development. The Camp observation API, saved job metadata,
memory reads and embedded Cortex adapter are implemented; job dispatch and the
native memory interface are not. The CLI inference primitive and Cortex model adapter are
implemented, but no user-facing consolidation command queues them yet.
[Cortex PR #1](https://github.com/teslashibe/cortex/pull/1)
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

## Saved jobs and memory reads

Schema 31 stores a consolidation job before provider work. The current API can
save, inspect and cancel queued jobs. Execution is not connected yet, so creating
a job does not start a CLI call.

- `POST /v1/camps/{camp}/memory/jobs` accepts `project_id`, `visibility`,
  `target`, `model`, `effort`, `speed`, `observation_ids` and `memories`, with an
  `Idempotency-Key`
- `target` is `{location: "local", profile_id: "…"}` or
  `{location: "cloud", account_id: "…"}`; Camp derives the owning device or
  installation and epoch from that person's saved account
- Each selected memory is `{id: "…", digest: "…"}`, using the digest returned
  by the memory read API; a changed snapshot requires a new review
- `GET /v1/camps/{camp}/memory/jobs/{id}` reads one permitted job;
  `GET /v1/camps/{camp}/memory/jobs?project_id=…&visibility=…` lists jobs
- `POST /v1/camps/{camp}/memory/jobs/{id}/cancel` accepts
  `{expected_revision: N}` and an `Idempotency-Key`
- `GET /v1/camps/{camp}/memory/memories?project_id=…&visibility=…` lists current
  consolidated memories, and the same path with `/{id}` reads one

Selections contain 1–200 pending observations and up to 500 existing memories.
Selected memory snapshots, including their provenance, must fit within 8 MiB;
oversized reads require a smaller selection or page.
One active job is allowed per namespace, with at most 32 active jobs per owner.
Cancelling retains its history and leaves observations available for another
review. The creator can cancel after a source is revoked; that response omits
the selected input references. It does not restore access to them.

Every memory read and job creation checks all contributing observations,
including sources inherited through reinforcement. A missing, private or
revoked source hides the complete memory. Project-job metadata has the same
source checks; other members cannot read the creator's profile, account,
installation or device IDs. Event replay applies those checks too.

Lists accept `limit` up to 100 and an `after` cursor; responses return `next`.
Memory pages can contain fewer items when sources are inaccessible. Follow a
non-empty cursor even when the page is empty. Neither lists nor creation calls
grant access to a previously private source or invoke a model.

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

### Inference adapter

`internal/memory.CLIModel` preserves Cortex's system and user messages and
supplies their model-bound hash to its dispatcher. The dispatcher still needs
to bind that hash to a durable, authorized job before calling the selected
installation. It receives no credential or local profile path from Cortex.

`camp-local memory-inference` accepts the selected profile, model, effort,
speed, system message and prompt through JSON on stdin. It makes one call
through the existing CLI adapters, in a new temporary working directory and
an ephemeral conversation. It rejects project permissions, tools and steering.
Codex receives explicit base instructions with project-document loading
disabled. Claude keeps safe mode, disables session persistence and receives
the system prompt through a private file. Neither path resumes the supplied
session ID or runs in its original working directory.

The system/prompt JSON is limited to 1 MiB; output is limited to 4 MiB and the
call to two minutes. Missing model or usage reporting remains missing. A
failure returns no successful result and never triggers a retry. Cancellation
joins the child process and removes the temporary directory. This adapter
does not authenticate Camp users or persist jobs by itself.

### Memory screens

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
