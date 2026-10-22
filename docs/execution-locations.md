# Execution location

Each provider card and main chat has a **Run on** control. It opens the same
Local/Cloud sheet. Local means the original provider session on its owner's
Mac; Cloud means an explicitly assigned workspace and provider installation.
Changing location does not silently choose another account or reroute queued
prompts.

## Defaults for new plans

The Defaults tab in Manage sources and the Defaults onboarding step use the
same controls. Save Local or Cloud, an optional single-plan provider and
separate Codex/Claude profiles or accounts. Cloud also requires a project.
These are private preferences for this Mac. Cloud choices are scoped by
server, signed-in user and Camp, with no provider credentials in the file.
Signing out retains the Cloud location preference so a new plan asks for
sign-in instead of unexpectedly starting on this Mac. Another Camp login
does not inherit the saved account IDs.

Local planning uses the selected profile. An explicitly selected profile that
is no longer available blocks creation until the user reviews the defaults.
Leaving a profile unspecified allows the first available imported profile for
that provider. Automatic provider selection uses the selected session's
provider if available, otherwise Codex if available, otherwise Claude.

Cloud planning requires an already prepared, assigned workspace in the
selected account and project. The default project is used unless the board
is filtered to another cloud project. For comparison, both selected accounts
must have eligible workspaces. The newest updated eligible source for each
provider is placed in a private draft, where its model, settings and execution
assignment can be reviewed before Start. This does not create a new checkout
or start a model automatically. Missing accounts or workspaces show an error;
they do not fall back to another account or Local. A **Plan on this Mac** menu
action explicitly overrides the location for that new draft.

Changing defaults never edits an existing plan or moves a ticket. The Run on
setup sheet prefers the configured account for new preparation, preserves a
selected account if it becomes unavailable, and requires a replacement to be
chosen explicitly.

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

`GET /v1/camps/{camp}/planning-sources?project_id={project}&account_id={account}`
returns at most 100 of the caller's eligible prepared cloud sources, ordered
by session update time. A source includes its ordinary session representation,
account ID and repository name. Eligibility requires a ready assigned
workspace, matching session/project revisions, an active project, connected
provider and Git installations at their saved epochs, and unexpired provider
credentials. Other members' sources are excluded, even when the sessions are
shared. The planning command still revalidates the chosen assignment when
Start is submitted and before each later phase.

The execution response includes `requires_transfer` after a published cloud
turn or a started implementation. Changing away from that workspace then
requires the reviewed transfer protocol. Until that protocol is implemented,
the app explains the requirement and the server blocks the move.

## Remaining acceptance

The complete history/base/ref/patch transfer flow and per-planner destination
selection remain unfinished parts of V1. Defaults currently select existing
prepared cloud sources; creating a new workspace directly from a planning
draft still needs implementation. Native [cloud steering](cloud-controls.md) now
uses the chat composer. A plain repository
checkout is not an accepted transfer of local edits. The signed-in native
happy path has been exercised against the real local API and disposable
PostgreSQL: private copy, project-linked repository selection, preparation,
review, cloud assignment and returning an unused workspace to its original
Mac. That fixture simulates Git and provider sign-in; it runs no model and
does not establish a real checkout. Remaining signed-in failure states and
the deployed two-Mac test are still open. No live cloud provider run has been
accepted yet.
