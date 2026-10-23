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
attest that a file is safe to execute or render. Native chat currently shows
attachment type and availability, not a media preview.

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
revision bindings. A failed upload leaves synchronization incomplete. A full storage quota shows\n`Attachment storage full` and stops automatic retry until another sync is requested.

Destination installation and native media download/preview are unfinished.
Native saved-history publication still refuses attachment-bearing messages.
Transfer guards remain until both destinations can preserve the bytes, including
main/subagent provenance, permissions, retries, offline recovery and revocation.
The transfer review must identify unavailable bytes before ownership moves.
Live provider, media preview, full migration and native visual acceptance remain
open. No remote-reference fetching or arbitrary local-path reading is authorized
by an imported transcript alone.

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
receive `attachment_busy`. The HTTP upload envelope is bounded to 23 MiB. The native uploader leaves\nbase64 slashes unescaped so every valid 16 MiB body fits that bound. There
is no compression, executable-file installation or automatic reference fetch.
Unreferenced uploads count toward storage limits; automatic orphan reclamation
is not implemented yet.

Stop schema-38 writers, back up the database and encryption key, run the existing
migration command, and start the schema-39 server. Readiness requires schema 39.
Downgrade refuses while any attachment blob or message descriptor is retained;
restore a compatible deployment rather than discard that history.

The native publisher uses this storage API; media preview is unfinished. Cloud message decoding retains descriptors, and the native destination
history downloader refuses an archive that would omit their bodies. Server
transfer checks also refuse sessions with retained attachment history. Those
guards remain until destination byte installation is complete.

## Verification

Run the native and bundled-helper checks with:

```sh
./test.sh CampSharingTests BridgeIntegrationTests CampChildHistoryTests CampCloudTests LocalPlanImportTests
```

Run the native publisher against a disposable loopback Go API and PostgreSQL:

```sh
./tools/test-native-api.sh NativeAttachmentAPITests
```

The API test uses independent owner and teammate logins, parent and child bodies
above 2 MiB, lost upload/transcript responses and revoked sharing. It uses synthetic
provider histories and does not establish live-provider or native visual acceptance.
