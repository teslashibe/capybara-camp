# Workspace transfer payload

`server/internal/transfer` captures selected files for review. The Go Camp API
stores resumable, encrypted uploads of that manifest. The internal staging
helper applies it to an isolated destination checkout. The cloud runner can
prepare that destination through an owner-authorized workspace job. The native
app includes a Local-to-Cloud review flow; full session migration and live
acceptance remain unfinished. Upload completion
means the payload passed validation and is ready for staging; it does not
change the session's assignment. Used cloud workspaces remain protected by
the existing transfer requirement.

## Captured state

`Capture(ctx, directory, selectedPaths)` reads an explicitly selected set of
repository-relative paths. The directory must be the Git working root. It
records the current commit, branch name, regular-file additions, changes,
deletions and executable-bit changes. A rename is represented by selecting its
old and new paths. Staged and unstaged changes are captured as the working
tree's final contents; the index split is not transferred.

Each file carries its base-content hash and desired-content hash. Added files
have no base hash, and deleted files have no desired contents. Files outside
the selection never enter the payload. The manifest contains no local root
path, account credentials or timestamps.

Version 1 limits a capture to 256 paths, 4 MiB per file and 32 MiB of desired
file contents. JSON decoding is bounded to 46 MiB. Git tree output is also
bounded. The current format requires a 40-character Git commit ID.

## Review and validation

`Manifest.Digest()` binds the base, branch, paths, file hashes, executable bits
and exclusion list. Reordering entries or formatting the JSON differently
does not change that digest. `Decode` validates content hashes, rejects
duplicate, overlapping or case-colliding paths, including different spellings
of a shared parent directory, unknown fields and versions, trailing JSON,
traversal and ambiguous path forms.

`VerifySource` rereads the working root, commit, branch and selected file
contents. A changed source requires a new capture and review. Capture also
performs that check before returning. The eventual handoff must still stop
source execution and revalidate this snapshot before accepting a destination
receipt.

Git reads use fixed commands with hooks, filesystem monitors, credentials and
external configuration disabled. File reads stay within an `os.Root`, refuse
links and non-regular files, and verify file metadata around the read.

## Exclusions

The manifest lists exclusions without their contents. Credential paths include
provider homes, `.env` files, common credential stores and private-key file
types. Content checks reject recognizable private-key blocks and common API
token formats. These checks do not identify every possible secret; they do not
replace the owner's review or the release security audit.

Symlinks, hardlinks, directories and submodules are currently reported as
excluded or unsupported. Unchanged selected files are identified separately.
The native review shows these outcomes before accepting a transfer;
it does not claim that an excluded file moved. Missing selected untracked
files are errors, while a missing tracked file is a deletion.

## Resumable upload API

All routes below require a signed-in device and the source session's owner.
Camp membership, editor access and permission to read a shared transcript do
not grant transfer access. Responses use `Cache-Control: no-store`. A server
must have `CAMP_PROVIDER_ENCRYPTION_KEY` configured to encrypt payloads.

Use `/v1/camps/{camp}/sessions/{session}/transfers` as the route prefix.

| Method and suffix | Contract |
| --- | --- |
| `POST /` | Create an upload with an `Idempotency-Key` and the reviewed source/destination snapshot |
| `GET /` | Return the latest 25 transfers for this session, including uploaded chunk ordinals |
| `GET /{id}` | Read durable state and resume an interrupted upload |
| `PUT /{id}/chunks/{ordinal}` | Upload one base64-encoded `data` value and its lowercase hexadecimal `sha256` |
| `POST /{id}/complete` | Send `{}` to validate the complete payload and change its state to `ready` |
| `GET /{id}/chunks/{ordinal}` | Read one verified chunk from a `ready` transfer |
| `POST /{id}/cancel` | Send `{}` to discard the stored chunks without touching the source |
| `POST /{id}/workspaces` | Authorize a separate Cloud destination using the existing workspace input |

Creation accepts these fields:

```json
{
  "target_location": "cloud",
  "target_account_id": "OWNER_ACCOUNT_UUID",
  "expected_epoch": 1,
  "expected_revision": 1,
  "expected_transcript_revision": 0,
  "expected_permission_revision": 1,
  "payload_sha256": "SHA256_OF_EXACT_MANIFEST_JSON",
  "review_sha256": "MANIFEST_DIGEST",
  "payload_bytes": 12345
}
```

The placeholders above must be replaced with real IDs and 64-character hashes.
For a Local destination, omit `target_account_id`; the API binds the transfer
to the authenticated device. A Cloud destination must be an owner's connected,
unexpired account for the source provider. Creation records that account's
epoch. Completion rechecks it, so changing the destination login requires a new
transfer and review.

The source must have no external owner or unfinished prompt request. Its
execution epoch, display revision, transcript revision and permission revision
must match at creation and completion. Cloud waiting states with unresolved
requests also fail this check. This is snapshot validation; it does not stop
the source or reserve the execution assignment.

Chunks are 256 KiB, with only the final chunk allowed to be shorter. The API
returns `chunk_bytes`, `uploaded_chunks` and `uploaded_bytes`, so a restarted
client can upload only the missing chunks. Out-of-order uploads are supported.
Repeating an identical chunk leaves the revision and byte count unchanged,
including after completion. Different bytes at an occupied ordinal return a
conflict. Creation-key replay returns the current transfer; changed creation
input with that key returns a conflict.

Chunk storage uses the operator's existing AES-GCM key with fresh nonces and
authenticated owner, Camp, transfer and ordinal bindings. This is encryption at
rest under the server's key, not end-to-end encryption. The completion path
verifies chunk integrity/order, the exact payload hash, strict manifest decoding
and the reviewed manifest digest before allowing chunk downloads. Invalid
uploads remain unverified until cancelled or expired. Uploaded bytes may contain
sensitive material before validation; the native review and source exclusions
are still required.

Each owner may hold two `uploading` or `ready` transfers across all Camps.
They expire 24 hours after creation; reads, lists and new uploads remove expired
chunks when they encounter them. This cleanup is opportunistic, so an inactive
server may retain encrypted expired chunks until the next operation. Cancel
immediately deletes chunks. Metadata remains for status and replay. A server
process admits at most two concurrent completion validations and returns 429
for excess work. Chunk HTTP bodies are capped at 384 KiB to accommodate base64;
decoded chunks and the complete payload retain their smaller format limits.

Creation, completion and cancellation emit owner-only `transfer.updated`
events. Intermediate chunk progress is available from status responses.
Replaying completion of a ready transfer only reports its stored validation
result. The future handoff must recheck source and destination eligibility even
when this earlier validation succeeded.

Schema 20 adds `session_transfers` and `transfer_chunks`; schema 21 adds reviewed
base metadata and the destination workspace binding. Schema 22 adds handoff
holds. Existing sessions,
assignments and queued work are retained. An uploaded payload from schema 20
keeps its encrypted chunks. Call completion again to validate it and populate
the new metadata before preparing a destination. Upgrades use the normal
migration command; destructive down-migration is refused.

## Isolated destination staging

`OpenStager(checkout, stateDirectory)` opens an existing private checkout and a
separate private journal directory on the same filesystem. Both directories
must belong to the current OS user, with no group or other permissions. Neither
may contain the other. The helper locks the checkout directory and journal so
two stagers cannot apply to the same destination, even with different journal
paths. These are cooperative locks; callers must keep provider processes and
other writers stopped until the destination handoff is accepted.
Each apply has a two-minute deadline, shortened by the caller's context when
applicable. Overlapping calls fail immediately. Directory scans and individual
Git outputs are bounded.

`Apply(ctx, manifest, reviewDigest, destinationBranch)` initially requires a
clean checkout, including no staged, untracked or ignored files. It verifies
the pinned Git commit and the caller's exact destination branch. The destination
branch can differ from the source branch because the runner creates a separate
ticket branch. Every selected base blob, executable bit and existing file must
match the manifest before any file is changed. Known secret markers in selected
base blobs are rejected. Whole-repository secret policy remains the checkout
and final release audit's responsibility.

The helper writes its journal before touching the checkout, applies only
selected files, then verifies every desired hash, executable bit and deletion.
Files are staged through private temporary files and renamed into place;
journal, file and affected directory writes are synced. New parent directories
are private. Link, hardlink and case-alias checks keep writes within the selected
paths. Git reads disable repository monitors, external diff and text-conversion
commands. The helper does not commit, fetch, push, change branches or run a
provider.

If the process exits during staging, successful file writes remain in that
isolated checkout. Reopening the same journal permits the same reviewed
manifest to resume: each file must still match either its base or desired state.
Unexpected edits, staged changes or additional files return a conflict without
discarding them. Abandoned helper temporary files are cleaned under the lock.
There is no rollback over newer work. The caller must retain the source and keep
the partial destination unavailable for execution until staging succeeds.

The returned `StageReceipt` identifies the review digest, base commit,
destination branch and file count. Replaying a completed stage rechecks the
files before returning the same receipt. A receipt is evidence of local file
verification, not permission to switch execution. Server-side source fencing,
main-history context and assignment commit use the handoff protocol below.

## Native Local-to-Cloud review

The original source Mac owns the review. In **Run on → Cloud**, the owner chooses
the destination account and GitHub repository before selecting working-tree
changes. `transfer-changes` lists at most 4,096 paths without reading their file
contents. The review starts with no selected files; an explicit empty selection
moves the main conversation and pinned base without working-tree changes.

`transfer-capture` returns the exact manifest and its file summaries. The native
review shows the pinned commit, exclusions and a captured-file preview capped at
64 KiB. Changing a source file after capture requires a fresh review; the source
worker rechecks it before acknowledging and before finishing the move. The
selected remote repository must contain the pinned commit. The app does not
silently substitute a branch tip or upload unpushed commits.

The scoped SQLite cache stores the payload once, separately from the small
transfer journal. Upload creation, workspace preparation, source hold and final
confirmation retain their command keys across restarts. Chunks resume from the
server's accepted ordinals. A terminal rejection pauses the flow for a deliberate
retry or cancellation. Cancellation intent is durable before any request is
cancelled. Clearing the Camp cache removes the journal and payload; persistent
source-lock recovery still follows the server's authoritative outcome.

One native transfer is active per signed-in Camp at a time. Closing its dialog
does not cancel it. Reopening the source ticket restores the current phase.
The final move shows the chosen destination and requires explicit tool
permissions for implementation. A completed transfer changes the execution
epoch only through the source-fenced server commit below.

This path preserves main and saved child history as context. Attachments,
unpushed commit transport, Cloud-to-Local moves and live two-Mac validation
remain open.

### Child history export

The local helper's `child-history-catalog` action enumerates a selected parent's
saved children. It returns opaque, profile-bound source IDs, provider metadata
and a catalog revision without local file paths. The catalog supports at most
512 children; an oversized or unreadable set fails explicitly rather than
returning a partial successful snapshot.

`child-export-page` accepts the parent, source ID, reviewed source revision,
`after` ordinal and `limit` from 1 to 200. It resolves the child through the
parent's current membership before reading any page. Another parent or profile
cannot substitute a source ID or transcript path. Each page reuses the existing
transcript index, preserving stable message IDs, amendments, tombstones and
recorded model settings. The page has the normal 1 MiB sharing budget and rejects
a changed source fingerprint. File fingerprints detect ordinary local changes;
they are not attestations against an adversary who can alter local provider
files and their metadata.

The native publisher sends these pages through the cloud storage contract
below. Child navigation uses the same sidebar and transcript component as local
children. The runner reads these histories through the context tool described
below. Previously opened catalogs and pages also use a bounded
[offline cache](offline-child-history.md).

### Cloud child histories

Schema 24 stores each child's transcript separately, using the existing message
and message-revision tables. Its relationship to the parent has an opaque source
key and versioned display metadata. Child writes advance the parent's transcript
revision, so a pinned parent revision also identifies the child history visible
at that point. Updates retain earlier message versions.

The source owner's original device can write through
`POST /v1/camps/{camp}/sessions/{parent}/children/{child}/transcript` with an
`Idempotency-Key`. `{child}` is a stable client-generated UUID for that source.
The request includes `source_key`, `source_revision`, `name`, `phase`, `workflow`,
`state`, `model`, `complete`, `removed`, `expected_parent_revision`,
`expected_transcript_revision` and `messages`. Messages use the same format and
200-message/1 MiB batch limits as the main transcript. An empty batch can publish
metadata or remove a child. The child count is bounded to 512 per parent.

The expected parent revision is its global transcript revision. The expected
child transcript revision is that child's own message revision. A successful
write returns both values. Retrying the same command returns the original
receipt; reusing its key with changed input is rejected. Concurrent stale writes
do not merge silently. The transcript stays unreadable until the sender marks
the reviewed snapshot `complete`; metadata can report an incomplete upload.

`GET /v1/camps/{camp}/sessions/{parent}/children?revision=N` lists child metadata.
`GET /v1/camps/{camp}/sessions/{parent}/children/{child}/transcript` reads messages
with `revision`, `before` and `limit` query parameters. Here `revision` selects
the parent's transcript snapshot; zero means current. The response includes the
resolved parent revision, current parent permission revision, child metadata and
the normal paged transcript. Current parent access is checked on every read,
including older snapshots. A removed child cannot be read through an old URL.

`POST /v1/camps/{camp}/sessions/{parent}/children/{child}/message` accepts
`revision` and `message_id` to inspect one cited message. The revision is the
parent snapshot revision, and the message must belong to that child's complete
snapshot. It shares the transcript endpoint's current access and removal
checks, with a 1 MiB message budget. The native inspector opens that exact
message and links back to the child conversation.

Children do not appear as standalone board tickets and cannot receive prompts,
change execution location or bypass the parent access check through ordinary
session URLs. Their update events use the parent session's visibility. No
provider credential is needed to read a shared child transcript.

The source device uses `GET /v1/camps/{camp}/sessions/{parent}/children/publication`
to reconcile current metadata, including removal records. This endpoint is
restricted to the parent's owner device. Ordinary readers never use it.
The native publisher saves child commands in the same scoped recovery journal
as main-session publication, with separate per-child message hashes. A dropped
reply reuses the saved command. Source disappearance marks a child removed;
reappearance keeps the same identity. The publisher rechecks the local child
catalog before reporting synchronization complete.

Before enabling child publication, upgrade every API process to the schema-24
build and apply the migration through the normal backup and release procedure.
Older API processes must not serve this database once child rows exist. The
migration preserves existing histories and refuses destructive downgrade.

## Cloud runner integration

`POST /{id}/workspaces` accepts the same `WorkspaceInput` as ordinary checkout
preparation: the owner's provider account, Git connection, installation,
repository, ref, purpose and expected session/project revisions. The selected
provider account must match the transfer. GitHub must resolve the selected ref
to the manifest's exact base commit. Commits that are only local are not yet
transported by this flow.

The source snapshot and account epoch are checked before and after the GitHub
reads. Creating a destination does not replace the source's existing workspace.
Each transfer may have one active destination. The returned workspace includes
`transfer_id`; its normal claim, lease, read-token and deadline rules apply.

Only the installation holding that destination's active preparation claim can
read its transfer. It posts `{"claim_id":"CLAIM_UUID"}` to these routes:

- `/v1/runner/workspaces/{workspace}/transfer` for validated metadata
- `/v1/runner/workspaces/{workspace}/transfer/chunks/{ordinal}` for a chunk

The worker verifies downloaded chunk hashes and the complete wire/review
hashes, prepares a separate Git checkout, and applies the manifest through
`Stager`. It saves a private manifest copy and staging journal beside the
checkout for recovery. These files contain the reviewed code; access is limited
to the owning OS user. Transient download failures retain the claim and retry
within its deadline. A retry with a completed local download verifies that copy.

The workspace's ready report must include `transfer_receipt` with the exact
review digest, base, destination branch and file count. The server rejects a
plain checkout report for a transfer workspace. Lost report responses replay
the same result. Source revision, transcript, permissions, account or Git
authority changes fence the job, and the reaper retires its Git read token.
Cancelling the transfer also cancels its active destination and retires that
token in the same transaction.

A ready transfer workspace remains unavailable through ordinary Run on
assignment. The final transfer commit uses the source lock and ownership
checks below. Local staged files are retained after
a rejected or cancelled job for review; normal retired-checkout cleanup does
not delete them. Account erasure remains a separate explicit lifecycle action.

## Source handoff hold

The owner can reserve a ready destination with
`POST /v1/camps/{camp}/sessions/{session}/transfers/{transfer}/handoffs`.
The request supplies `workspace_id`, `expected_workspace_revision` and
`expected_transfer_revision`, plus an `Idempotency-Key`. The server rechecks
the source snapshot, destination receipt state, project, Git connection and
provider account before creating one five-minute hold for that source session.
Existing pending, running or uncertain prompts prevent the hold from starting.

An effective hold blocks new Camp prompts, approvals, starts and execution
assignment changes with `409 transfer_in_progress`. It does not discard queued
work or move the session. The hold remains effective after source acknowledgement
until release, expiry, source/destination invalidation or a confirmed transfer commit.
The effective guard checks expiry on every request; it does not depend on a
cleanup timer running.

The original source device finds pending local work through
`GET /v1/camps/{camp}/handoffs/next`. A cloud source uses
`GET /v1/runner/handoffs/next`. Both return a handoff ID or 204 when none is
available. These source-only operation prefixes are:

- Local device: `/v1/handoffs/{handoff}`
- Cloud installation: `/v1/runner/handoffs/{handoff}`

Post `{"claim_id":"CLAIM_UUID"}` to `/claim`, `/source`,
`/chunks/{ordinal}` or `/reject`. The source response includes its session,
reviewed transfer metadata, and the source workspace for Cloud. Only the
original source device or its current source installation can claim or read
this data. A different owner device or provider account cannot acknowledge it.
The claim is retained for response-loss recovery.

The source client must stop its provider process and verify the selected files
against the manifest before posting to `/ack`:

```json
{
  "claim_id": "CLAIM_UUID",
  "stopped": true,
  "payload_sha256": "SHA256_OF_EXACT_MANIFEST_JSON",
  "review_sha256": "MANIFEST_DIGEST"
}
```

The server validates the source identity, claim, hashes and current snapshots.
Repeating the same acknowledgement returns its existing result. The `stopped`
field is a declaration from the authenticated source client; the API does not
itself stop a CLI. The cloud worker supplies that declaration only after
reconciling its saved turn, verifying that no active turn journal remains, and
checking the selected source files against the manifest.

Cloud handoff claims are saved in a private journal before submission. The
worker rechecks source files when retrying an acknowledgement, including after
a lost response. A changed file rejects the handoff. Completed turn receipts
remain readable and replayable during a hold, so a lost finish response cannot
prevent the worker from reaching an idle state. New turns remain blocked, and
the scheduler skips held sessions. After acknowledgement, the worker retains
its journal but can run other sessions on the account while the owner reviews.
The source stays assigned until the final confirmation described below.

The source Mac consumes handoff jobs through its existing local worker. It
persists the claim before submission, resolves the original imported session,
and sends the reviewed payload to the bundled Go helper. The helper acquires
the same process-held lock as local chat, checks external session ownership,
and verifies the selected Git files. It refuses a session still open in Codex
or Claude; it does not interrupt another application's provider process.

The helper remains alive after acknowledgement, retains the lock, and checks
for file or ownership changes during the hold. The Mac worker checks it again
before acknowledging. A restart reacquires and rechecks the source; a lost
acknowledgement is reconciled through the existing claim. Release, expiry,
sign-out or a closed app ends the local hold. A failed source check invalidates
the handoff without starting a model turn. Idle discovery is bounded to one
check every ten seconds and handoff events wake it immediately.

The owner reads status at
`GET /v1/camps/{camp}/sessions/{session}/handoffs/{handoff}` and releases it by
posting `{}` to the same path with `/release`. A rejected, cancelled, expired
or stale hold permits source work again. Transfer cancellation invalidates its
holds. Events are owner-only.

## Final source confirmation

After the source acknowledges, the owner posts to
`/v1/camps/{camp}/sessions/{session}/handoffs/{handoff}/commit` with an
`Idempotency-Key` and the acknowledged `expected_handoff_revision`. A planning
destination uses empty permissions and `execution_approved: false`. An
implementation destination requires `execution_approved: true` and explicit
provider permission settings, using the same validation as ordinary Run on.

The response is HTTP 202 with a fresh `commit_nonce`. The session still belongs
to the source. The original source client checks the files and stopped provider
again, then posts its earlier acknowledgement fields plus `commit_nonce` to
`/finish` under its source-only operation prefix. A changed file, expired hold,
changed permission or stale destination rejects completion.

The server commits these changes in one transaction:

- Retire the old cloud checkout's token and mark it cancelled, retaining its files
- Assign the prepared destination and approved permissions, incrementing the execution epoch once
- Store the committed source proof and new epoch on the handoff
- Mark the upload `completed` and remove its temporary encrypted chunks
- Emit the owner-scoped handoff, transfer and session events

The destination retains its transfer binding and verified file marker after
the upload expires. Moving away from that checkout requires another reviewed
transfer, even before its first provider turn. Replaying the exact completion
returns the saved result. A restarted source can read that result by claiming
with its original saved claim; it cannot reread the consumed payload. A new
claim or changed proof is rejected.

Within the five-minute hold, the Mac retains the helper's local conversation
lock until it has fetched the current session, saved it in the scoped cache
and synchronously published the updated routing. A failed read or cache write
keeps the completion journal for retry. The cloud worker keeps its journal until
it reads the committed receipt. Neither client starts a model turn to complete
a transfer.

Before posting final confirmation, the native coordinator saves its recovery
journal and asks the source helper to verify the files and persist a transfer
marker in the existing session lock. The helper flushes the marker and its
directory before returning. The marker contains only the handoff UUID; the
filename is a hash of the provider, canonical profile path and session ID.
Camp's normal conversation helper refuses a marked source after timeout,
app exit or restart. A partial or malformed marker also blocks execution.

Only the same handoff can reopen a marked source for verification. After a
restart, the coordinator checks its saved handoff through the authenticated
API before retrying. A committed move keeps the old local source marked. A
released, expired or invalidated move clears the marker only after the server
also confirms that the original execution epoch is still Local. Loss of
connectivity or access preserves the marker and recovery journal.

The local `transfer-resolve` helper clears a matching marker under the session
lock; the native coordinator owns the server authorization check. This controls
Camp's bundled CLI, not a provider CLI launched independently. The native transfer
review UI remains unfinished.

### Recovery without the local cache

Sign-out and cache erasure remove the scoped recovery journal. The separate
marker still blocks local execution. When the user signs back in, the Go
`transfer-fences` helper discovers orphaned markers and returns only their
handoff UUIDs and opaque lock keys. It skips active helpers, empty lock files,
malformed markers and symlinks without deleting them. Inspection is bounded
to 10,000 directory entries and 512 markers per inventory.

The native worker checks at most 32 handoffs at a time through
`POST /v1/camps/{camp}/handoffs/recover`, with a `handoff_ids` array. Unknown
IDs, another owner's handoffs and other Camps are omitted. The response pairs
each owner's local-source handoff with the current session assignment. It
normalizes expired holds under the same Camp lock used for other changes.
Reading recovery state grants no source claim or execution permission.

Committed transfers restore the current cloud assignment and retain the old
local blocker. Released, expired or invalidated transfers release a matching
opaque lock key only when the source epoch is still assigned to Local. This
works even if the old profile directory has moved. A different device can
read the result as the owner, but cannot confirm the original source's work.
An unresolved move stays blocked. A missing record, lost permission or another
Camp never releases it.

The worker scans once on connection, then at most every 30 seconds, retaining
completed results for that connection and processing one bounded batch per
poll. Changing the account or Camp resets that in-memory selection. No provider
prompt is started or replayed during recovery.

## Remaining integration

The cloud runtime can read the stored main transcript and complete child
histories through a paged context tool, without loading them into a startup
request. `list_subagents` returns up to 50 child descriptors per page;
`read_session` then reads the selected history. The catalog uses the queued
parent revision, and every read rechecks current request permissions and child
removal. Later child edits do not change the queued snapshot. These reads remove
the earlier 120-message startup boundary. File attachments remain separate work.

The agreed transfer flow still requires:

1. Finish complete migration acceptance and attachment provenance, including
   provider continuation and commits not
   available from the remote repository
2. Native conflict review and the complete base-repository credential policy,
   followed by cloud-to-local and cross-account destination acceptance
3. Connect native conflict review to the existing destination receipt and atomic
   assignment operation, with source and destination status visible throughout
4. Native review, standalone-plan history migration, and local-to-cloud and
   cloud-to-local acceptance through both provider clients

The capture and upload layers do not complete any destination handoff. Its
lifecycle and permissions remain under the existing Camp, runner and
local-client ownership boundaries.

## Validation

```sh
cd server
go test -race ./internal/transfer
# With CAMP_TEST_DATABASE_URL pointing to a disposable PostgreSQL database:
go test -race -run 'TestTransfer|TestWorkerStagesTransfer|TestRunnerHandoff|TestWorkspaceHTTP|TestMigration' \
  ./internal/camp ./internal/httpapi ./internal/database ./internal/runner
go vet ./internal/transfer ./internal/camp ./internal/httpapi
# From the repository root, including the real bundled Go source guard:
cd ..
./test.sh CampHandoffTests CampWorkerTests CampExecutionTests
```

The tests use temporary Git repositories. They cover additions, deletions,
mode changes, exact selection, excluded credentials and links, source drift,
detached commits, tampered payloads, input limits and disabled repository hooks.
PostgreSQL and HTTP tests cover restart/resume, encrypted chunk substitution,
owner-only access, source and account revisions, queued work, expiration,
cancellation, concurrent cross-Camp quotas, body limits and migration retention.
Staging tests use real Git checkouts and cover file/mode/deletion application,
replay, partial writes, abrupt process exit, source retention, changed files,
staged/untracked/ignored files, false preimages, links, path aliases, directory
replacement, journal binding and disabled repository commands.
The runner integration test uses the Go HTTP API, disposable PostgreSQL and a
real Git HTTP fixture. It drops chunk and completion responses, verifies one
destination checkout and receipt replay, and confirms the source stays intact.
Handoff tests cover original-device/source-installation access, exclusive holds,
idempotent acknowledgement, queue preservation, expiry, release and source drift.
The source-runner tests use both provider protocol fixtures, verify their actual
subprocesses have exited, recover a lost finish response during a hold, and retry
a lost handoff acknowledgement. Changed source files are rejected without
starting another provider turn. Native tests cover claim and acknowledgement
response loss, relaunch, retained locks, changed files, scope erasure and the
real helper against a temporary Git repository. Live provider, native transfer
review and two-Mac acceptance remain separate gates.

Child-history tests read 150 messages through each provider's actual context
tool connection and preserve a 300,000-byte Unicode response across pages. They
check pinned revisions, incomplete/removed children, foreign parents and
bounded catalog lookup. Native tests verify exact-message citation projection
and reject cross-parent snapshots. These use synthetic provider processes;
they do not replace live-provider acceptance.

Final-commit tests cover explicit implementation permissions, concurrent
confirmation replay, expiry and revision changes after approval, owner/source
authorization, receipt recovery and continued work in the destination after
the upload expires. Both provider fixtures resume from the transferred files
after a dropped completion response. The native source tests retain the lock
through lost completion and session-read responses and a failed cache write;
they reject files changed after the first acknowledgement.
Persistent-marker tests cover helper exit, hold expiry, same-handoff retries,
wrong-handoff resolution, malformed markers and symlinks. Native tests exercise
the real helper's final-verification command and a cancelled confirmation across
coordinator restart.
