# Session context

The V1 development app supports `@` session lookup and selected message snapshots.
The recent-session sidebar still shows ten conversations; the picker searches the
full accessible local and shared index by title, project, provider, profile and
shared member name.

Choose a source, review its messages and select a range. Each attachment is
limited to 120 messages, each message to 48 KiB and the whole turn to 256 KiB of
source text. Large sources require a smaller selection. Drafts and local queued
prompts retain their attachments. The local store is owner-only and capped at
4 MiB including source receipts; reaching that limit refuses a new write rather
than silently dropping provenance.

Local snapshots retain the exact selected text and its SHA-256 revision. Shared
snapshots retain the Camp scope, session ID, transcript and permission revisions,
and selected message IDs. Shared content is fetched through authenticated reads;
private local text cannot be attached to a shared request. Reading a source never
grants permission to prompt it.

Schema 4 adds the selected IDs to durable shared requests. The read-only
`POST /v1/camps/{camp}/sessions/{session}/context` endpoint checks current access
and the expected permission revision, then returns only the requested messages
from the pinned transcript revision. Revoking a source also cancels affected
requests through the existing request lifecycle. Apply migrations before running
this development server; its readiness check requires the exact schema.

For each CLI turn, Camp creates an owner-only temporary MCP manifest containing
only the authorized snapshots. The bundled `read_session` tool accepts a source
ID and a bounded offset/limit. It accepts no file paths, network destinations or
prompt actions. Source links return to the saved snapshot in the native app.
Temporary files are removed on normal completion and cancellation. A subsequent
context turn also removes recognized manifests left by a dead process. Project tools, hooks
and unrelated MCP servers remain disabled in the chat preview.

The Codex adapter configures only this read tool through app-server. The Claude
adapter uses an explicit MCP config in restricted mode with built-in tools and
hooks disabled. Readable attachment links appear with the user prompt; transport
metadata and tool instructions stay in the instruction channel. A live Codex check has
passed; live Claude verification is pending because the configured account
reported its weekly limit. Unit and protocol checks are not a substitute for
that remaining provider check.

Planning reuses these selected snapshots. Attaching a new source to an
already-running turn requires queuing the prompt for its next turn.

Cloud conversations also expose their own complete stored transcript through
`read_session`, including after a provider conversation has been created. This
history is pinned to the queued prompt's transcript revision and read in pages;
it is separate from the limits on selected `@` attachments above. Long messages
continue at the returned UTF-8 byte offset without splitting a character.
Every page checks the active request and current permissions. The parent runner
keeps its service credential; a temporary local reader gives the context helper
access to that one transcript. See [provider turns](runner-turns.md#conversation-history).

History citations use the same native snapshot inspector. For a citation not
stored as an `@` attachment, the app requests exactly that message and revision
through `POST /v1/camps/{camp}/sessions/{session}/message`. This read requires
current session access and the current permission revision. It accepts one
message, up to 1 MiB, so a long historical response can be inspected without
increasing the model-context selection limit. Missing or mismatched revisions
and message IDs are rejected. The fetched citation is not added to the local
attachment archive.
The inspector closes when the Camp changes, the source disappears or its
permission revision changes.

Child histories, file attachments, live-provider acceptance and the release
performance/soak gates remain in the V1 delivery scope.

Integration references: [Codex app-server](https://learn.chatgpt.com/docs/app-server)
and [Claude CLI reference](https://code.claude.com/docs/en/cli-reference).
