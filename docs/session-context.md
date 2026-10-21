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

This is a foundation for Plan together, not a completed planning workflow.
Attaching new sources to an already-running turn currently requires queuing the
prompt for its next turn. Cross-device validation, cloud-hosted execution and
the release performance/soak gates remain separate work.

Integration references: [Codex app-server](https://learn.chatgpt.com/docs/app-server)
and [Claude CLI reference](https://code.claude.com/docs/en/cli-reference).
