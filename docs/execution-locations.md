# Execution location

Each provider card and main chat has a **Run on** control. It opens the same
Local/Cloud sheet. Local means the original provider session on its owner's
Mac; Cloud means an explicitly assigned workspace and provider installation.
Changing location does not silently choose another account or reroute queued
prompts.

## Moving into the cloud

1. Copy a local transcript into a private Camp project, if it is not already
   there. This does not share it with other members or allow their requests.
2. Choose a connected provider account, the owner's GitHub connection,
   installation and a repository linked to that project.
3. Prepare a checkout from a branch, tag or commit. The server pins the commit
   and creates the ticket's branch. Local uncommitted files are not included.
4. Review the account, Git identity, pinned commit, transcript revision and
   permissions. Implementation requires explicit tool approval.
5. Assign the ready workspace to the session. A running or queued turn blocks
   the move. Future prompts use that assignment.

The native client saves each setup operation before sending it. Lost replies
retain the same idempotency key through retries and app restarts. A definitive
rejection clears the rejected operation and reads the current state so the
owner can review again. Signing out clears the Camp's setup journal and fences
late writes with its cache epoch. The journal contains identifiers and setup
choices, never provider or Camp credentials.

The local publisher stops exporting local runtime state and transcript changes
after cloud assignment. The server also rejects local transcript writes while
the cloud owns execution. Cloud activity and streaming update the existing
session card and chat.

Returning an unused cloud workspace to the Mac retires that preparation slot
in the same transaction. The account can then prepare another ticket. The
ticket retains its Camp project while connected, and its unsent draft remains
in the composer. A used workspace still requires the transfer protocol below.
An account can also retain multiple ready cloud tickets. Its worker schedules
their prompts one at a time and keeps each checkout and provider history
separate; see [provider turns](runner-turns.md#multiple-tickets-on-one-account).

## API additions

`GET /v1/camps/{camp}/sessions/{session}/workspaces` returns the newest 25
workspace records to the session owner only. It reuses the existing workspace
authorization and representation. Other Camp members cannot read the owner's
account or Git identities through this endpoint.

The execution response includes `requires_transfer` after a published cloud
turn or a started implementation. Changing away from that workspace then
requires the reviewed transfer protocol. Until that protocol is implemented,
the app explains the requirement and the server blocks the move.

## Remaining acceptance

The complete history/base/ref/patch transfer flow and location defaults remain
separate unfinished parts of V1. Native [cloud steering](cloud-controls.md) now
uses the chat composer. A plain repository
checkout is not an accepted transfer of local edits. The signed-in native
happy path has been exercised against the real local API and disposable
PostgreSQL: private copy, project-linked repository selection, preparation,
review, cloud assignment and returning an unused workspace to its original
Mac. That fixture simulates Git and provider sign-in; it runs no model and
does not establish a real checkout. Remaining signed-in failure states and
the deployed two-Mac test are still open. No live cloud provider run has been
accepted yet.
