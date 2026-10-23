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

## Local destination staging helper

The bundled Go helper accepts `transfer-stage` for the destination side of a
Cloud-to-Local transfer. Its caller supplies the downloaded manifest as base64
`payload`, exact `payloadSHA256`, reviewed `reviewSHA256`, canonical `transferID`,
a 64-character lowercase SHA-256 `scope` identifying the origin/Camp/owner,
an absolute `destination` and its reviewed destination `branch`.

The destination must already be a private, isolated Git checkout at the
manifest's pinned commit, on the same filesystem as the app support directory.
The helper verifies both hashes before touching it
and applies files through the same stager used by the cloud runner. It retains
the staging journal under the app support directory, scoped to the transfer.
Repeated calls recheck the same checkout and return the same receipt. A changed
destination, branch, base, reviewed payload or conflicting local edit fails
without discarding the edit. Unknown fields, trailing JSON, symlink destinations
and non-private directories are refused. Input size includes the bounded base64
envelope; no provider process or Git network operation runs.

This helper does not create or fetch the checkout, download cloud history,
change a session assignment or start a local CLI. The cloud capture job, native
reverse-transfer coordinator, local history/attachment installation and final
ownership commit remain required before Cloud-to-Local moves are available in
the app. The staging receipt is file-verification evidence only.

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
and a catalog revision without local file paths. Child source revisions are
content revisions, so an unrelated conversation's database write does not
invalidate the child's contents. The catalog supports at most
512 children; an oversized or unreadable set fails explicitly rather than
returning a partial successful snapshot.

`child-export-page` accepts the parent, source ID, reviewed source revision,
`after` ordinal and `limit` from 1 to 200. It resolves the child through the
parent's current membership before reading any page. Another parent or profile
cannot substitute a source ID or transcript path. Each page reuses the existing
transcript index, preserving stable message IDs, amendments, tombstones and
recorded model settings. The page has the normal 1 MiB sharing budget and rejects
a changed child content revision. File fingerprints refresh the local index;
they are not attestations against an adversary who can alter local provider
files and their metadata.

Local export pages also return `contentRevision`, a SHA-256 revision of the
selected conversation's indexed messages, order, removals and recorded model
settings. It is distinct from the filesystem `fingerprint` used to refresh the
index and from the cloud's integer transcript revision. An unrelated Codex
conversation can change the shared database fingerprint without changing this
content revision. The revision and returned page come from one SQLite read
snapshot; an index refresh computes the revision before committing.

The native publisher requires the same main-conversation content revision
across all exported pages and checks it again after copying children. A changed
copy retries before being marked synchronized. Already accepted upload batches
remain on the server and are reconciled by that retry; this is not an atomic
multi-page upload. The final source handoff uses the history binding below.

### Reviewed history binding

A Local-source transfer manifest includes `history` with `version: 1`, a
`parent_id`, `main_revision`, `children` (`source_id` and `revision` per child),
and the imported cloud `transcript_revision`. Local IDs are opaque hashes;
no transcript text, credentials or local paths enter this binding. The manifest
and review hashes cover it along with the selected files.

The publisher saves this binding only after all main pages and children finish
copying. Capture requires its cloud revision to match the selected session,
then the Go helper compares it with the local contents before and after file
capture. A missing, changed or newly added child fails that comparison. Another
Codex conversation writing to the same profile database does not fail it when
the selected main/child contents remain unchanged.

The source guard repeats this comparison during its hold and immediately
before creating the persistent commit fence. The same session lock protects
Camp's local conversation execution. External processes are still checked;
the binding does not claim to defend against a malicious owner modifying their
own provider files or cache while preserving its metadata.

Schema 25 stores `history_sha256` after validating a completed upload. Local
handoffs require this digest, including previously ready uploads and pending
holds. The manifest's cloud revision must equal the transfer's pinned source
revision. Old file-only reviews must be cancelled and recreated. Upgrade the
API and runner builds, apply migration 25, and use the matching native app
before enabling this flow. Migration 25 preserves older records without
inventing verification; downgrade is refused while verified history records
remain. No cloud migration has been deployed for this development increment.

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

Before enabling child publication and reviewed transfers, upgrade every API
process to the schema-25 build and apply migrations through the normal backup
and release procedure.
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
For a cloud destination, the request supplies `workspace_id`, `expected_workspace_revision` and
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
Local destinations additionally require `context_continuation_approved: true`
from the selected destination Mac. This approves continuing the reviewed context
in a new provider conversation; it does not claim native provider-session resume.

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

The source hold verifies process ownership, selected working files and the
reviewed main/child history binding. Full migration acceptance still needs the
native walkthrough and live two-Mac/provider checks, in addition to the
remaining attachment, commit-graph and reverse-transfer work above.

## Cloud source exports

Schema 34 adds short-lived owner-only workspace exports. An owner creates an
`inspect` job to list changed paths, then a `capture` job with an explicit path
selection (including an intentional empty selection). Creation pins the source
execution epoch, workspace, session, transcript and permission revisions.
Only the assigned installation can claim the job. A claimed export reserves
that installation's execution slot while the worker reads its isolated checkout.
The export never launches a provider or moves session ownership.

The runner uses the same Git capture and credential-exclusion code as local
transfers. It saves the result in a private local file before uploading, so
response loss or restart reuses the same bytes. Results use SHA-256 checks and
encrypted 256 KiB chunks. Capture results must contain exactly the selected
paths or their exclusions; they cannot carry runner-invented chat history.
The owner can download completed results only while source, project, account
and Git authorization still match. A teammate cannot inspect or download them.
Revoking a runner grant stops further runner operations; it does not itself
erase a completed result owned by the user.

Jobs expire ten minutes after creation and cannot extend that deadline. Each
owner can retain two active or completed unexpired jobs. Cancellation and failed
captures delete uploaded chunks. Expired bytes are removed on the next owner
export operation; expiry prevents access even before that cleanup runs. A runner
can discard its own claimed upload after source changes, freeing its execution
slot without gaining access to new source content.

Owner routes under `/v1/camps/{camp}/sessions/{session}/exports`:

- `POST /` with `Idempotency-Key`: create an inspection or selected capture
- `GET /{export}`: read status and uploaded chunk ordinals
- `GET /{export}/chunks/{ordinal}`: download a completed result
- `POST /{export}/cancel`: cancel and discard file bytes

Installation routes under `/v1/runner/exports` provide `GET /next`,
`POST /{export}/claim`, `POST /{export}/result`,
`PUT /{export}/chunks/{ordinal}`, and `POST /{export}/complete` or `/fail`.
All writes after discovery carry the saved claim ID. Discovery exposes only
an opaque ID, and a second claim cannot adopt an interrupted upload.

The native `CampWorkspaceExport` coordinator persists a create intent before
posting it, then downloads one verified chunk per advance. Its journal and chunks
are scoped to server, owner and Camp; clearing that scope invalidates in-flight
work. Cancellation is durable, removes local chunks, and reconciles an uncertain
create before discarding the journal. Returning cached bytes requires a fresh
authorized chunk read as well as the complete payload hash. A status response
alone does not authorize access to file contents.

Downloaded captures are reviewed through the bundled Go `transfer-review`
command. It accepts the bounded manifest on stdin, checks the same secret/path/
integrity rules used by capture and staging, and returns the existing file-review
summary without duplicating file contents. It does not read provider profiles,
write files or change execution ownership.

The native selection/review surface still needs to connect to this coordinator.
Destination checkout preparation, attachments and the local ownership
handoff below need native integration and acceptance. Commits absent from the
remote repository remain a separate migration requirement.

## Native history download

`CampTransferHistory` downloads the parent's transcript and every complete,
non-removed child at one reviewed parent revision. The child catalog, each page
and the final archive must match that revision and its permission revision.
Incomplete children stop the download. Message IDs, ordinals, child provenance,
model, effort and speed are retained; child messages stay separate from the parent.

Each advance saves one page and its cursor atomically in the owner/Camp-scoped
SQLite cache. The journal contains page hashes and byte counts. Lost replies can
repeat a read; restarts reuse saved pages. A new sign-in for the same owner can
resume without adopting another owner's cache. Cancellation or clearing the Camp
removes pages and fences in-flight saves. Version checks reject stale writers.

Returning an archive rechecks every page hash and cursor, rejects duplicate IDs
across pages, verifies that the child catalog is complete, and fetches the current
authorized session before and after assembly.
Changed ownership, execution assignment, transcript or permissions requires a new
review. Offline or rejected authorization cannot release cached history to the
installer. The archive includes its server origin and source session identity.

Downloads use 120-message pages, at most 512 children, 2,000 messages and 8 MiB of
text per conversation, and 32 MiB of stored pages per transfer. Exceeding a limit
fails explicitly without truncation. The downloader owns no timer or provider
process. The installer below preserves this archive privately. Attachments and
the transfer review UI still need integration; a complete archive does not grant execution.

Run the native downloader against disposable PostgreSQL and the real local API:

```sh
CAMP_NATIVE_FIXTURE_HISTORY=1 tools/test-native-api.sh CampTransferHistoryTests NativeTransferHistoryAPITests
```

The fixture seeds 260 parent and 250 child messages for each provider, prepares
synthetic cloud assignments, then checks download recovery on a second device,
private-history denial for another user and changed permissions. Git and provider
accounts are synthetic; no model turn runs. Fixture credentials and caches are
removed on exit.

## Local checkout preparation

The bundled Go `transfer-checkout` action prepares a private checkout for one
owner/Camp scope and transfer UUID. Input selects a GitHub repository, exact base
commit and destination branch, with a matching short-lived read credential.
Schema 36 adds `POST /v1/camps/{camp}/sessions/{session}/exports/{export}/checkout-token`
with the reviewed capture's `sha256`. The authenticated device must belong to the
source owner. The API checks the completed capture, source/history/permission
revisions, current project, provider installation and Git connection before and
after GitHub scopes a read token to the exact repository. Root OAuth and runner
credentials are never returned to the Mac.

One durable grant belongs to each export/device pair. Concurrent or lost-response
retries return its encrypted saved result; an uncertain provider result closes
that pair without minting again. Start a new capture to recover from a closed
or expired grant. Responses are non-cacheable and excluded from shared events
and idempotency-command payloads. Effective validity is at most five minutes and
never exceeds the provider or export deadline. Cancellation queues immediate
revocation; periodic reaping also closes grants after device/membership/source,
project, provider or Git authority changes. GitHub revocation is an external
operation and can fail or be delayed; the helper also enforces the response's
expiry. No promise of instantaneous bearer-token invalidation is made.

The native export coordinator verifies the reviewed digest before requesting the
grant, validates its device/workspace/repository/base binding, and passes it to
the bundled helper without saving it to the export cache. It rechecks source
access and cancellation before returning a receipt. The native selection/review
screen still needs to compose this operation with history/file installation and
the committed local handoff; no token-entry UI is planned.

`internal/gitcheckout` contains the transport, credential helper and tree checks
shared with the cloud runner. Git ignores ambient user/system configuration,
hooks and credential helpers, refuses redirects, and bounds output and process
lifetime. The credential helper answers only for the exact authorized GitHub
HTTPS repository. Credentials are stored in a private temporary file for fetch,
removed afterwards, and cleared on restart if a helper was killed. They never
enter checkout configuration, intent records or receipts.

Preparation uses an unpublished scratch directory under the transfer's private
state. It fetches the exact commit, validates tree sizes, submodules, path
collisions and symlink chains, then checks out the selected branch. File and
directory synchronization precedes an atomic rename that cannot replace an
existing destination. A ready marker binds the same scope, transfer, repository,
commit, branch and destination. A lost response reuses the verified clean
checkout without another fetch. Changed Git configuration, newer edits and
unexpected destinations fail without resetting them. Interrupted unpublished
scratch work can be rebuilt; a published checkout is never removed by retries.

The checkout and installer share the same state lock. The installer then applies
reviewed changes and preserves the pinned parent/child archive. Once files are
staged, retry installation from its receipt instead of preparing the checkout
again. Preparation intentionally refuses that dirty checkout. Neither operation
changes execution ownership or starts a provider.

Local smart-HTTP Git tests cover both preparation and the history installer,
interrupted fetch/publication, independent transfers, unsafe trees, changed
configuration, cancellation and concurrent destination creation. These fixtures
use synthetic credentials and no external GitHub account. Commits absent from
the remote still require the separate commit-graph transfer work; a failed fetch
does not silently replace the selected base or discard source work.

## Local destination installation

The bundled Go `transfer-install` action composes the existing file stager with
an app-owned history archive. Its native caller supplies the authenticated export,
reviewed manifest, complete history archive, selected discovered local profile,
new conversation UUID and isolated destination checkout. The helper checks that
the export and archive refer to the same owner, Camp, source session, workspace,
execution epoch, transcript and permission revisions. An expired export cannot
prepare a new receipt. The profile and provider must match local discovery.

The installer holds the checkout and staging-directory locks while it writes a
private immutable intent, exact `history.json`, reviewed files and final
`installation.json`. Writes use private temporary files, file synchronization,
atomic rename and directory synchronization. The receipt binds the export ID,
history and file hashes, checkout, target profile and continuation UUID. A lost
response can replay the same request; changing that binding, corrupting history
or editing staged files refuses the retry. A completed installation with a
missing archive or intent fails instead of silently reconstructing it. Symlinks,
hardlinks and public archive files are rejected.

`CampWorkspaceExport.installLocally` obtains the authorized file bytes and pinned
history, checks the reviewed digest, invokes the bundled installer, then rechecks
source access and cancellation before returning the receipt. Preparing an
installation writes no provider-native transcript or credentials, publishes no
local session and starts no model. It is an approved context-continuation
candidate, not native provider resume. The remaining native handoff integration
must verify the receipt and committed assignment before executing it.

## Resuming local preparation

`CampLocalDestination` composes the native export, complete history downloader,
checkout preparer and installer. It begins only after the selected capture and
complete history have been reviewed. A private cache journal pins the source,
export, review hash, destination device/profile and new conversation UUID. One
advance prepares the checkout; the next installs files and history. The checkout
receipt is saved before installation starts, so restarting after installation
never reruns checkout preparation against the restored changes.

Each save compares the journal revision and preserves its original target and
receipts. Cancellation is durable while a helper is running, and a late result
cannot replace it. Account-cache erasure invalidates in-flight work. Resuming an
installed candidate re-verifies the installer receipt and current source access;
changing the selected device, profile, review or files requires a new reviewed
move. Cancellation preserves already written local files rather than deleting
potential user edits. After cancellation is acknowledged, resetting the flow
atomically removes only its matching review journal and cached export/history
pages. Stale operations cannot recreate the discarded journal.

This coordinator prepares a destination; it does not publish a local session,
start a provider or change execution ownership. Its integration with the native
selection/review screen and committed handoff remains required.

## Native return handoff coordinator

`CampReturnHandoff` starts from an installed destination and re-verifies its
receipt before creating a move. A private journal saves separate stable keys for
transfer creation, the source hold and owner confirmation. Uploads advance one
chunk at a time and reconcile the server's saved chunk inventory after restart.
The hold binds the selected profile, new conversation UUID, installation receipt
and transcript revision.

Run its HTTP/PostgreSQL integration test with:

```sh
CAMP_NATIVE_FIXTURE_HISTORY=1 CAMP_NATIVE_FIXTURE_RETURN=1 \
  bash tools/test-native-api.sh NativeReturnHandoffAPITests
```

The fixture uses a separate owner device and teammate login, both providers and
510 parent/child messages per ticket. It drops responses after the real server
persists transfer creation, the hold and confirmation. The production Go helper
reviews the manifest; checkout/install receipts and runner stop proofs are
synthetic. Provider processes, real GitHub access and the native review screen
need their separate acceptance tests. Fixture credentials and caches are removed
when the test exits.

An acknowledged source hold exposes confirmation. Context continuation requires
an explicit approval; implementation additionally requires explicit execution
permissions. The exact permissions and handoff revision are saved before the
confirmation request. The coordinator reconciles a lost confirmation response
before trying to read an old export that may have become inaccessible after the
source moved. Completion requires the committed handoff plus the same ticket
assigned to the selected Mac/profile/conversation at the next execution epoch.

Cancellation is persisted and reconciles any uncertain transfer creation before
cancelling its upload/hold. If the source already committed, the coordinator
observes that completed move instead of reporting cancellation. Cache erasure
invalidates late responses. These states do not themselves start a local model;
the native screen must adopt the completed binding before the worker can use it.

## Restored history in a local conversation

The native conversation transport accepts an optional `restoredHistory`
reference containing the installation scope, transfer ID and receipt hash. The
bundled helper opens only the corresponding private installation directory,
verifies its receipt, intent and complete archive, and checks that the selected
provider profile and checkout match the installation.

The archive uses the existing read-only `read_session` and `list_subagents`
context tools. Each page contains at most 20 messages and 48 KiB of text;
individual text chunks are at most 16 KiB and end at UTF-8 boundaries. Original
message IDs, sparse ordinals and parent/child links are retained. The helper
holds one bounded snapshot for the conversation and rechecks the receipt and
archive identity on each read. Erasure, replacement, file permission changes or a
new hard link refuse further reads. Cancellation closes the existing context
channel with the provider conversation.

Attaching history does not select new-versus-resumed execution, alter provider
conversation IDs or grant project tools. The native owner must first verify the
committed handoff and current execution assignment, then persist the new CLI
conversation ID before treating it as resumable. The archived history is the pinned context from
the move; subsequent provider messages belong to the continuation.

`LocalContinuationBinding` keeps the stable Camp ticket/destination identity
separate from the actual CLI conversation ID. After a fresh committed-handoff
and assignment read, adoption stores the binding and clears only the matching
transfer journals, downloaded chunks and history pages in one SQLite transaction.
Installed files and the shared transcript remain intact. Replayed adoption
preserves an existing first-request reservation and provider ID. Bindings are
scoped by server/user/Camp, ticket and execution epoch; clearing the Camp cache
fences late callbacks and removes its bindings.

Before process creation, `reserveContinuation` requires a persisted running
worker attempt with the assigned owner/device/epoch, matching permission revision,
unexpired lease, unrestricted context and appropriate execution permissions.
It atomically saves the request reservation. A duplicate reservation fails;
stale worker saves cannot clear it. If the first request has no known provider ID,
another request cannot silently create a replacement conversation. Normal
uncertain-attempt reconciliation remains required after restart. Binding the CLI
ID requires that first running reserved request; replaying the same ID is safe,
but replacing it is refused. Later authorized requests select the saved provider
ID. The native transport offers an awaited persistence callback before forwarding
the session event, rejects missing/conflicting IDs and saves a repeated identical
ID only once.

The native worker resolves an adopted binding without requiring an ordinary
imported session on the destination Mac. It checks the current assignment and
reserves the request after saving Start, before creating the provider process.
The first launch persists the physical conversation ID before forwarding its
event; follow-ups require agreement with the server and resume that ID.
Both use the pinned installed-history reference, never a truncated native
transcript. Planning-purpose continuations run read-only chat, while implementation
uses the committed permissions and a fresh CLI context fingerprint. The bundled
helper rechecks that fingerprint before project tools run.

Returned runs count against local execution capacity and use the existing lease,
stop, ordered stream and owner-control paths. Missing/stale bindings cannot
launch. An unknown first submission or reserved process-creation failure remains
fenced against replacement. Normal snapshot publication must not overwrite the
retained history. The native return screen adopts the binding and keeps the Camp
ticket visible while hiding its duplicate physical provider log. The original
source copy stays on disk and returns to the local list after disconnecting.

## Local continuation streams

Schema 37 adds a write-once provider conversation ID to the committed local
handoff. A request is identified as a local continuation only when a committed
local receipt exists for its exact session/execution epoch. Request reads return
the bound provider ID for follow-up; the stable ticket/local destination identity
does not change. A profile/conversation uniqueness index and the existing owner
lock prevent binding a conversation already published or bound elsewhere.

`POST /v1/camps/{camp}/requests/{request}/continuation-events` accepts the same
sequence/event/lease envelope as local implementation streaming. Only the assigned
owner device with its current lease can write. Each append rechecks session,
execution and source access, preserves the existing event limits, and rejects
sequence gaps or changed replays. A session event binds the provider ID once;
completion cannot report success without that ID. Ordinary local imports do not
gain this endpoint merely by being local.

The existing local-controls endpoint delivers steering and approvals to these
requests under the same device/lease checks. Control submission remains owner-only,
with existing approval sequence validation and provider acknowledgements. Stream
headers identify local continuations so the native chat can render their output,
offer the supported controls and show actual model settings. The existing activity
updates move the ticket to In progress, Needs you or its terminal state.

Terminal requests append through the existing transcript transaction. New IDs
are `camp:<request UUID>:<part index>` and ordinals start after the current last
message. Saved imported parent and child records remain unchanged. A persisted
publication flag prevents duplicate appends after lost finish responses. The
native reader hides streamed output after these IDs appear in saved history.
Ordinary transcript writes to the returned epoch are refused, and the bound
provider conversation cannot be separately published as a second ticket.

Downgrade from schema 37 refuses to discard retained provider IDs, continuation
streams or active/uncertain continuation requests. Empty rollback remains
supported. The native worker and return review consume this stream contract.
Attachments, missing remote commits and live-provider acceptance remain required
before the whole transfer flow is complete.

## Local destination receipts

Schema 35 extends the existing source-hold protocol to a local destination.
The selected Mac begins the handoff with `expected_transfer_revision` and a
`local_destination` object instead of cloud workspace fields:

```json
{
  "expected_transfer_revision": 3,
  "local_destination": {
    "profile_id": "<registered destination profile UUID>",
    "session_id": "<new local conversation UUID>",
    "purpose": "planning",
    "receipt_sha256": "<SHA-256 of the durable destination receipt>",
    "transcript_revision": 12
  }
}
```

The profile must belong to the same owner and selected device and match the
source provider. The device must remain active. The new local conversation ID
must not already be published in any Camp, and the history revision must match
the reviewed transfer. Publication and the final handoff collision check share
an owner transaction lock. A changed history, revoked destination or occupied
conversation invalidates the hold without changing the execution owner.

The receipt is the destination client's attestation. The API cannot inspect the
Mac's disk. The native installer must persist and verify the checkout, history
and attachments before beginning this protocol, recheck them before confirmation,
and verify the committed assignment before starting a provider. The backend
does not yet have that complete native installer connected to it.

Owner confirmation leaves execution on the source until its runner checks the
stopped provider and reviewed files again. Final proof updates the existing
ticket's profile, device, local conversation ID and execution epoch atomically;
its parent and child transcripts retain their IDs and revision. The old cloud
checkout is cancelled and its Git token retired. Implementation permissions
remain in the approved handoff receipt and apply to the new local epoch.
Replayed or concurrent identical proofs observe one committed move. A lost
response never authorizes starting a second provider process.

Real PostgreSQL tests cover parent/child history and recorded settings, concurrent
confirmation, explicit continuation/implementation consent, destination revocation,
cross-Camp collisions and migration rollback. Runner tests use the real API and
Git staging with synthetic Codex/Claude processes. They cover lost finish, source
acknowledgement and final commit responses, plus changed source files. Their local
destination installs files and attests a history revision; it does not test native
history restoration or a live local model turn.

## Validation

```sh
cd server
go test -race ./internal/transfer
# With CAMP_TEST_DATABASE_URL pointing to a disposable PostgreSQL database:
go test -race -run 'TestTransfer|TestWorkerStagesTransfer|TestRunnerHandoff|TestWorkspaceExport|TestRunnerWorkspaceExport|TestWorkspaceHTTP|TestMigration' \
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

## Native return review

**Run on → Local** reviews cloud project changes, previews selected captured
files, downloads the pinned main and child history, and offers discovered local
profiles for the same provider. Preparation creates an isolated checkout and
installs the reviewed files/history. It never starts a model turn.

The source hold must be acknowledged before the user can confirm imported-context
continuation. Implementation separately requires project-tool approval and the
shared permission selector. Closing the sheet preserves the journal; reopening
resumes preparation but never supplies consent. Cancellation remains available
during installation and after an earlier error. A completed handoff is adopted
once; a failed board refresh can retry without repeating the move.

The board resolves the adopted profile and actual provider conversation through
its private binding. It hides the old linked source and the new physical log
behind the existing Camp ticket, while preserving both local files. Unrelated
profiles with the same provider ID remain visible. Model catalogs and planning
setup use the destination profile, and stale source mappings cannot redirect
history or permissions. Local execution on a different device is labelled
**Owner’s Mac**.

Reproduce the screen recovery suite with `./test.sh CampReturnTransferTests`.
`tools/return-transfer-ui-audit.sh` builds an isolated native review with synthetic
accounts, files and handoff responses. It makes no provider calls. The real API
journey uses disposable PostgreSQL and synthetic provider/filesystem adapters:

```sh
CAMP_NATIVE_FIXTURE_HISTORY=1 CAMP_NATIVE_FIXTURE_RETURN=1 \
  tools/test-native-api.sh NativeReturnHandoffAPITests
```

## Unpublished commit payload

The transfer package can capture a bounded Git object archive above an explicitly
selected ancestor. The manifest's `base_commit` stays the source HEAD against
which selected working changes were captured. Optional `commits.base_commit`
identifies the ancestor a destination must independently authorize and fetch;
`commits.head_commit` must equal the manifest's `base_commit`.

Original object bytes preserve commit IDs, authorship, parent links, merge
history, binary content and executable bits. No refs, hooks, Git configuration,
credentials or unselected working files are copied. New object bytes are checked
against the transfer's known secret patterns, including earlier versions deleted
before HEAD. New or changed policy-excluded paths in any transferred commit
refuse the archive. They cannot be stripped without changing commit identity.
Source capture refuses grafts. A shallow source is accepted only when its sole
boundary is the authorized ancestor and every exported commit parent is either
included or that ancestor. Ordinary linked worktrees are supported. Capture
never fetches, pushes or modifies source history.

The archive allows at most 256 commits and 4,096 objects, with the existing 4 MiB
per-object bound. Commit objects and selected working-file bodies share the
32 MiB total payload budget. The review digest binds each object's SHA-256,
identity, type and length, plus the source and ancestor commits. Object order
does not change approval. Transfers without an archive retain their prior
review digest.

`RestoreCommits` requires a clean, private checkout at the authorized ancestor.
It validates connectivity and every transferred commit tree in a temporary
object database before installing objects. It rejects missing or extra objects,
unsafe paths and symlinks, grafts, shallow destinations and external object
stores. Failure removes the temporary database. Installation is retryable and
does not change HEAD, branches, the index or working files. A caller must exclude
other checkout writers and separately perform the reviewed checkout, file stage
and execution handoff. A receipt proves object installation only.

The bundled helper's `transfer-capture` accepts an optional `remoteBaseCommit`
and returns commit counts, byte counts and a separate archive review digest.
`transfer-review` derives the same summary from downloaded bytes. An ancestor
supplied here is not evidence of remote access or permission.

The cloud destination path now authorizes and fetches the reviewed ancestor,
restores the object archive, checks out the reviewed head and applies selected
working changes through the existing stager. It fetches complete ancestor history
for these transfers because a merge parent may fork before the ancestor. Ordinary
workspaces retain their depth-one fetch. The existing Git operation deadline,
private workspace storage and preparation lease still apply.

Schema 38 saves `remote_base_commit` separately on the transfer and destination
workspace. It is derived from the validated archive, not a separate client claim.
GitHub resolution must return that exact ancestor through the owner's repository
access. The destination's `base_commit` remains the reviewed unpublished head.
Claims, saved worker records and transfer validity bind both values. Replaying a
completed preparation request returns its existing workspace.

Before staging, interrupted read-only preparation may repeat in the unused
destination. Once the staging marker exists, the worker resumes its existing
file journal and refuses to recreate that checkout. Dropped chunk and ready-report
responses do not repeat completed preparation. Preparing a workspace still does
not move session execution or approve a provider turn.

Stop older writers before migrating to schema 38 and use the matching server and
runner binaries. An older worker must not prepare an archive-bearing transfer.
Rollback refuses to discard retained ancestor fields; it succeeds only when none
remain. Older ready payloads whose metadata lacks their archive ancestor require
recapture, rather than guessing an authorization binding.

Cloud-source capture includes commits above the workspace's authorized remote
ancestor, including commits previously imported from a Mac. The API verifies the
archive's workspace binding before accepting an export. Checkout-token issuance
derives the reviewed head and remote ancestor from the encrypted payload; a cached
grant with different bindings cannot be reused. Payload reconstruction uses the
existing bounded validation slots and is released before provider token issuance.

The native return review displays the unpublished commit count and ancestor/head
revisions. Its checkout request binds the downloaded payload, review and grant.
The Mac helper fetches the authorized ancestor into a private scratch checkout,
validates and restores the archive, and checks out the reviewed head. Its durable
receipt binds the archive digest. Retrying a published checkout never resets
newer files. Working-file and transcript installation and explicit execution
handoff follow through the existing return flow.

For Local-to-Cloud moves, the existing repository and branch/revision selection
also selects the ancestor lookup. The local helper matches the selected GitHub
repository against configured HTTPS/SSH remotes, reads that branch's cached
remote-tracking commit and computes its common ancestor with HEAD. A supplied
40-character commit must itself be an ancestor. Diverged branches are supported;
ambiguous common ancestors and conflicting cached remotes are refused. Discovery
is bounded to 30 seconds, 32 configured remotes and 16 matching remotes, and never
fetches, pushes or changes local history. Remote URLs and embedded credentials
are not returned in discovery results.

The file-selection screen labels the result as a cached remote ancestor. Capture
binds that selection to the inspected HEAD and requires another inspection when
HEAD changes. If ancestor and HEAD match, the transfer uses the existing ordinary
payload. Otherwise it captures the intervening commits and displays their summary
using the same component as Cloud-to-Mac review. The server independently resolves
the exact ancestor through the owner's GitHub connection during preparation;
cached local refs do not prove remote access. Native responses must preserve the
reviewed head and ancestor through transfer completion and workspace preparation.

Attachment completion and full live-provider handoff acceptance remain open.
Native transfer tests use synthetic fixtures; these do not prove a live provider
transfer or visual acceptance of the latest controls.
