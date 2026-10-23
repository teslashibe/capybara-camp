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

Inspection is bounded to 64 attachments, 4,096 content blocks and eight nested
levels per message. Embedded bodies are limited to 16 MiB; provider records are
limited to 32 MiB. Inline MIME types are allowlisted, but this metadata does not
attest that a file is safe to execute or render. Native chat currently shows
attachment type and availability, not a media preview.

## Publication and transfer status

Attachment byte synchronization is unfinished. Exporting a transcript containing
attachments currently returns an explicit error and leaves the prior committed
index unchanged. Native saved-history publication also refuses attachment-bearing
messages. The importer fingerprint changed so an old text-only index cannot
bypass this check. Local viewing and text-only sessions remain supported.

This guard is temporary implementation state, not the V1 transfer contract.
The next work must carry owner-authorized bytes and immutable message references
through Camp storage, sharing, both transfer directions and destination history
installation. It must enforce current Camp/source permissions on every read,
retain main/subagent provenance, recover uploads without duplicate blobs, remove
access after revocation, and represent references whose bytes cannot be recovered.
The transfer review must identify unavailable bytes before ownership moves.
Live provider, media preview, full migration and native visual acceptance remain
open. No remote-reference fetching or arbitrary local-path reading is authorized
by an imported transcript alone.

Provider formats were checked against the official
[Codex input types](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/user_input.rs)
and [Anthropic message types](https://github.com/anthropics/anthropic-sdk-go/blob/main/message.go).
