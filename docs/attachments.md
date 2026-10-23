# Transcript attachments

The local importer preserves attachment metadata alongside message text, including
messages that contain only an attachment. Codex SQLite history and JSONL rollouts
and Claude JSONL messages use the same Go parser in `internal/attachments`.
Nested Claude tool-result content is inspected as well.

Supported embedded sources include base64 images, PDF and plain-text documents,
and audio data URLs. The parser recognizes local paths, remote URLs and provider
file references, but it does not resolve them. Unsupported, malformed and oversized
attachments remain visible with an availability state. Metadata contains no
original path, URL, provider file ID or base64 body.

Each reference binds a block position within its message, the SHA-256 of the
original block, and, for readable embedded data, the SHA-256 and byte count of
the decoded body. Changing a reference or body changes its digest. Descriptors
are not globally readable blob IDs.

The bundled helper's `transcript-attachment` action takes the selected session,
message ID, attachment ID and original-block digest. It rereads the source,
applies the provider's message-amendment ordering, and returns exact embedded
bytes only if the selection still matches. An incomplete source write, removed
message, changed attachment or changed source fingerprint refuses the read.
It writes no attachment cache and never fetches a URL or opens an attachment path.
The native reader also verifies the returned selection, size and content hash.
The `child-transcript-attachment` action resolves an opaque source within the
selected parent/profile and checks the selected child revision before and after
reading. It accepts no caller-supplied child path.

Inspection is bounded to 64 attachments, 4,096 content blocks and eight nested
levels per message. Embedded bodies are limited to 16 MiB; provider records are
limited to 32 MiB. Inline MIME types are allowlisted, but this metadata does not
attest that a file is safe to execute or render. Native chat shows type and availability for every attachment. Embedded images,
audio and documents can be opened in the shared Quick Look preview.

## Publication and transfer status

The native publisher uploads verified embedded bodies before recording a pending
transcript command. Publication retains all attachment descriptors, including
unavailable references. Duplicate bytes within a page share one upload per
conversation. The publisher checks the receipt's conversation, hash and size;
a lost response retries the content-addressed PUT. Once a transcript command is
journaled, restarting replays its references without rereading the body. Raw
attachment bytes are never saved in the publication journal.

Uploads stop when the active Camp changes, the task is cancelled or its local
cache scope is cleared. The server independently checks current source ownership.
Child uploads follow creation of the incomplete child history and precede its
message publication. Completed parent/child histories retain their source
revision bindings. A failed upload leaves synchronization incomplete. A full storage quota shows
`Attachment storage full` and stops automatic retry until another sync is requested.

The native byte reader, chat preview/save controls, transfer downloader and local
byte installer are implemented. Transfer history version 2 carries a separate
manifest binding each embedded body to its conversation and message. The
downloader retains at most 128 MiB across 1,024 conversation-scoped bodies and
checks the pinned source before and after each read. The local installer copies
one verified private file at a time and commits its receipt only after all
files and history are present. Interrupted installation resumes from verified
files; a changed or missing body cannot produce a completed receipt.
Saved implementation history retains the original provider conversation as its
private byte source. Publication uploads verified bodies before journaling their
descriptors and reconciles attachments alongside text after a lost response or
restart. A missing or changed source stops publication. A complete saved copy
can be recovered without rereading the original provider files.
Restored and cloud history readers expose attachment descriptors through
`read_session` and exact embedded bytes through `read_attachment`. Each tool
selection includes the message, attachment ID and source digest. Reads verify
the selected revision, current authorization and body hash. The two large-body
HTTP slots are shared with ordinary attachment upload and download requests.

The local development handoff now presents the number and total size of stored
files, plus unavailable references grouped by type and reason. Confirmation
includes a SHA-256 review identity bound to every descriptor, its original
conversation/message and the pinned transcript revision. Missing or changed
confirmation identities refuse the move. Missing retained bodies also refuse
handoff, while cancellation remains available. Source ownership and fresh stop
proof checks still apply. This replaces the development blanket attachment block;
it does not complete the release gate. Live provider acceptance remains unfinished.
The PostgreSQL attachment/handoff regression suite and native transfer, export
and return-screen suites pass, including exact review confirmation, missing-body
cancellation and preserved contents. Evidence: `build/attachment-review-regression.log`
and `build/attachment-review-native.log`. These checks use synthetic providers.
Live provider, full migration and complete native visual acceptance remain
open. No remote-reference fetching or arbitrary local-path reading is authorized
by an imported transcript alone.

Focused validation covers 16 MiB bodies in native parent/subagent downloads,
private staging files, interrupted installation, hash corruption, link refusal,
source revocation and cache erasure. Real local API/PostgreSQL downloads retain
260 parent and 250 child messages for each provider. A synthetic provider
process also reads a 16 MiB image through the actual context-tool executable,
runner proxy and authenticated API, then completes exactly one turn for Codex
and Claude. These are fixture tests, not live-provider acceptance. Evidence:
`build/transfer-attachment-native-current.log`,
`build/transfer-attachment-context-tests.log`,
`build/transfer-attachment-cloud-postgres.log` and
`build/transfer-attachment-provider-verified.log`.

Provider formats were checked against the official
[Codex input types](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/user_input.rs)
and [Anthropic message types](https://github.com/anthropics/anthropic-sdk-go/blob/main/message.go).

## Camp storage and API

Schema 39 stores attachment descriptors on immutable message revisions and keeps
bodies in `transcript_attachment_blobs`, separate from transcript pagination.
Bodies use the existing operator-supplied provider encryption key, with AES-GCM
binding to Camp, conversation and SHA-256. The same bytes in another conversation
require a separate authorized upload. Database and key backups are both required
for recovery.

| Endpoint beneath `/v1/camps/{camp}/sessions/{session}` | Contract |
| --- | --- |
| `PUT /attachments/{sha256}` | Upload `{data, child_id?}` with base64 bytes; the active owner device may upload for a local session, or another device belonging to the same owner for saved history |
| `POST /attachment` | Read `{revision, message_id, attachment_id, source_digest, child_id?}` through an authorized committed message |

A PUT is idempotent by conversation and content hash. A retry rechecks current
ownership and verifies the existing encrypted body before returning its receipt.
There is no readable blob-by-hash endpoint. Parent-session permissions authorize
child reads; the current child must remain present and the selected historical
child revision must be complete. Each read checks current Camp membership,
session sharing and device validity before decrypting. A pinned revision can
retain older message attachments, but it never retains access after session
sharing or membership is revoked. Responses use `Cache-Control: no-store`.

Transcript publication verifies that every embedded descriptor has a stored body
with the same hash and size in that conversation. All descriptors participate in
message revision storage and page budgets. Missing bodies refuse the publication
transaction. Unavailable external references can be represented as metadata, but
the byte endpoint cannot resolve them.

The server accepts at most 16 MiB per body, 1,024 blobs and 128 MiB per parent
session including its children, and 512 MiB per owner across Camps. Two attachment
requests can decode or return bodies concurrently per API handler; other requests
receive `attachment_busy`. The HTTP upload envelope is bounded to 23 MiB. The native uploader leaves
base64 slashes unescaped so every valid 16 MiB body fits that bound. There
is no compression, executable-file installation or automatic reference fetch.
Unreferenced uploads count toward storage limits. An upload with no reference
in any retained message revision can be reclaimed seven days after its last
verified PUT. Retrying the PUT renews that window. Cleanup runs in bounded batches
in the existing maintenance loop, skips busy Camps, and rechecks references under
the same Camp lock as publication. Superseded messages and removed child histories
retain their referenced bodies. A queued publication whose upload has expired
must upload the original bytes again before its descriptors can be committed.

Stop schema-39 writers, back up the database and encryption key, run the existing
migration command, and start the schema-41 server. Readiness requires schema 41.
Existing uploads receive a fresh seven-day window on upgrade. Downgrading to 39
stops expiry cleanup and retains the bodies; stop schema-40 processes before the
downgrade. Downgrade below 39 refuses while any attachment blob or message
descriptor is retained; restore a compatible deployment rather than discard it.

Schema 41 marks a blob as published in the same transaction that commits its
first referencing revision, and the expiry index holds unpublished uploads
only. Cleanup cost therefore follows the number of unreferenced uploads, not
transcript history. At 10,000 aged published blobs over 400,000 revisions the
schema-40 sweep took 12.7 s against a 5 s budget; schema 41 takes under 1 ms.
The upgrade backfills the flag from message revisions once, which took about
13 s at that size, so stop schema-40 writers for the migration as above.
Downgrading to 40 restores the full reference scan.

The native publisher and permission-checked byte reader use this storage API.
Cloud message decoding retains descriptors, and the native destination
history downloader refuses an archive that would omit their bodies. Server
transfer checks require the pinned embedded bodies to remain available and the
owner to confirm the exact attachment review before ownership moves.

## Native byte reads

`CampAttachmentReader` binds a read to the Camp origin/user, parent or child,
message, transcript revision, permission revision and original descriptor. It
verifies the returned descriptor, exact byte count and SHA-256 before returning
bytes. References without embedded bytes cannot enter this path. The reader has
no attachment cache and does not fetch original URLs or local file references.

`CampConnection.attachmentBytes` also checks the active connection generation,
credential, session permissions and local cache epoch before and after the read.
Child reads check the child-cache epoch as well. Cancelled or superseded reads
discard their result. Every new read goes through current server authorization.

Only the exact attachment-read POST route accepts a response up to 23 MiB;
ordinary JSON responses retain their 4 MiB cap. Attachment bodies arrive through
a reusable ephemeral URLSession with chunk bounds, declared-length rejection,
redirect refusal and two active downloads at most. Cancelling one request stops
its network task without closing the session; closing the client cancels all
active reads. Data remains in memory and is not written to a file or URL cache.

## Native previews and saved copies

Main and subagent messages use the same preview control. Opening an embedded
attachment reads and verifies its bytes, then creates a private temporary folder
(mode 0700) and preview file (0600). Quick Look displays the file without automatic
audio playback. Closing, switching the selected source or replacing its descriptor
removes the temporary copy; cancelled or superseded reads cannot reopen it.

Save a copy opens the native destination picker. It verifies the preview bytes
again and saves only to the chosen destination. That explicit copy survives preview
closure. A closed or replaced preview cannot complete a pending save. Unavailable
references stay visible without a preview button; the app never follows their
original URLs or file paths.

Preview and transfer staging folders include the creating process ID. At startup,
a background cleanup removes recognized, owner-only folders only after the
process has stopped. It retains active or ambiguous process IDs, symlinks,
folders with unexpected permissions and all unrelated files. A reused process ID
is retained until that process exits. Explicitly saved copies are outside this
cleanup. Earlier development folders without process ownership are left alone.

The saved-history, plan-import, preview cleanup, transfer-staging and bundled
helper checks pass in `build/saved-attachment-native-tests.log`. The same run
verifies 16 MiB saved attachments through the local API for both providers,
including lost responses and private owner access. Providers are synthetic;
this does not complete live-provider acceptance.

## Verification

Run the native and bundled-helper checks with:

```sh
./test.sh AttachmentPreviewTests CampAttachmentReadTests CampSharingTests BridgeIntegrationTests CampChildHistoryTests CampCloudTests LocalPlanImportTests
```

Run the native publisher against a disposable loopback Go API and PostgreSQL:

```sh
./tools/test-native-api.sh NativeAttachmentAPITests
```

The API test uses independent owner and teammate logins, parent and child bodies
at the 16 MiB maximum, lost upload/transcript responses, cache erasure/reconnect
and revoked sharing. It uses synthetic
provider histories and does not establish live-provider or native visual acceptance.
