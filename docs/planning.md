# Planning workflow

The V1 development app offers **Plan** for one model and **Plan with comparison** for two providers. Both keep their board ticket, version history, context and model settings. A single plan makes one bounded planning turn; comparison makes two independent drafts, two reviews and a combined plan.

The V1 development server has a durable planning queue for both modes. The Mac app
also has a private local planning coordinator and paired-chat workspace. Local
plans persist on the owning Mac and use isolated official CLI turns. The native
workspace also creates and controls shared planning tickets. Their queued steps
use the session's assigned owner-Mac worker or cloud runner. The cloud path has
local synthetic evidence; live cloud acceptance is still pending.
Local plans can continue into an approved implementation on the same ticket.

The local workspace includes a saved board card, independently selected
profiles and model settings, pinned session context, separate draft/review
transcripts, combined output and version history. It shares the ordinary
session card dimensions, provider marks, model menu and recent-session list.
Leaving the workspace keeps planning active. Pause stops the next step from
starting; cancellation stops active turns. Start over preserves the prior
version, and an unconfirmed result requires explicit review before restarting.

Private state is bounded to 200 tickets, 100 versions per ticket and a 16 MiB
file. It is stored with owner-only permissions. Every step is persisted before
launch, and a saved starting/running step becomes outcome unknown after an app
restart. A failed save prevents new provider work. Streaming text updates only
the open planning workspace; board updates occur at state transitions.

One ticket retains every plan version. A version freezes its brief, selected
source message ranges, its planning mode, provider profiles and model/effort/speed settings. Comparison also freezes its combining provider. In comparison mode, each provider drafts independently, reviews the other draft once, and one produces the combined plan. The combined-plan prompt explicitly
retains unresolved disagreements.

## Local implementation

The completed plan opens an implementation setup step in the same workspace.
Choose an executor profile, model, effort and supported speed independently of
the planner or planners. Selecting a different profile retains the plan's original
project directory. Approve and implement freezes the plan revision, executor and
local CLI configuration snapshot, then creates a persistent provider session.

The board card keeps its logical ticket ID and becomes a provider session card.
Its chat streams the implementation and accepts steering or queued follow-ups.
View plan and history returns to the drafts, reviews and combined plan. Previous
implementations remain in history when a new planning revision starts.
Imported copies of the implementation's native session do not create another
visible board card. Terminal activity and saved transcript updates refer back to
the same ticket.

An implementation attempt is saved before launch. On interruption, its outcome
requires review; Camp does not automatically repeat it. Continuing the same
provider session or starting another implementation is an explicit choice.
The installed CLI enforces project permissions, and requests for additional
access appear in chat. See [execution approvals](execution-approvals.md).

Steps reuse the existing approved request and lease lifecycle. A separate
planning claim endpoint and explicit `include_planning` opt-in on mixed claims
prevent older chat workers from resuming an anchor session by mistake. New
native workers claim chat and planning through the same durable queue. Planning
uses the anchor only to identify its provider
profile and project. The CLI bridge starts an ephemeral Codex thread or a
non-persistent Claude session; it does not append planning turns to the anchor
conversation. The request mode also refuses mid-turn steering of the frozen
brief and bounds visible output to 8 KiB per step.

## Shared planning in the native app

Use the Plan menu's shared Camp actions to create a saved draft. Choose one accessible source session for Plan, or two for comparison, then select model/effort/speed settings and pinned message ranges. Comparison also selects the combining provider. Each planner shows its saved execution location. An owner-Mac assignment uses that owner's local profile; a cloud assignment uses its prepared workspace and account. Plans
are private by default; publishing a shared plan requires accessible shared
sources. Camp does not upload private source sessions implicitly.

Start atomically saves the version and queues its single turn or first two comparison turns. The Mac
persists commands before sending them and reuses their idempotency keys after
a lost reply. A cancellation made while creation is pending binds to that
created version. It cannot cancel a later version of the same ticket.

The draft becomes one board card and opens one chat for Plan or both provider chats for comparison in the main workspace. Pause and resume control subsequent claims; an in-flight turn can
finish after a pause. Planning events wake idle workers when a plan resumes.
Start over retains the ticket, prior versions, pinned context and settings.
History and source links remain accessible subject to current permissions.
Revoked sources are excluded from board reads and receipt replays as well as
detail reads. Drafts and pending commands use the existing scoped local cache.

A draft records each session's execution location, assignment epoch and cloud
workspace ID. If a session moves while the draft is open, Start is disabled
until the creator reviews the current locations. The queued command retains
that exact selection through offline delivery and retries. The server rejects
a command whose reviewed assignment is stale. Existing clients that omit this
field bind to the current assignment when the server saves the version.

Every subsequent phase checks the saved assignment again. A changed location,
expired cloud account or unavailable checkout stops the plan and retains its
completed drafts; it never falls back to a Mac or another account. The whole
next phase is checked before its requests are created. Restart creates a new
revision with newly reviewed destinations. [Execution defaults](execution-locations.md#defaults-for-new-plans)
apply only to newly created drafts and never edit these saved revisions.

Shared results refresh when steps complete. Partial remote output and shared
plan-to-implementation conversion remain unfinished; local plans already
support both live output and approved implementation on the same ticket.

At most one planning step per provider can be leased to a device, including
across different tickets and profiles. The two providers may run concurrently.
The one- or five-step limit bounds durable planning-turn requests, not a provider's
internal network retries or tool-related model calls. Reported token usage is
preserved when available; missing usage is not reported as zero.

The initial API defaults to strict token ceilings and refuses to start because
these CLI adapters cannot guarantee those ceilings. Explicit `budget_policy:
"calls"` enables the bounded workflow with 40,000 input and 8,000 output token
targets for reporting only. The release default is awaiting the owner's budget
choice; these targets must not be described as enforced caps. The native
workspace requires an explicit one- or five-turn selection and leaves it off by default.

Pause prevents another claim or start. Cancellation requests stop active work
and fences queued steps. Restart creates a new immutable version under the same
ticket ID. Late results remain attached to their original version and cannot
advance the new one. An expired started lease becomes outcome unknown and stops
new planning claims until reconciliation. The coordinator never re-dispatches a started turn automatically.

Plans are private by default. Shared plans require shared source sessions;
sharing a plan does not grant access to a private source. The creator controls
start, pause, resume, cancellation and restart. Selected planner owners may read
private plan details so they can approve their work. Camp membership and source
permissions remain authoritative, and revocation cancels affected work. Events
respect the same plan visibility as reads.

## API

- `POST /v1/camps/{camp}/planning` creates a draft with an idempotency key;
  `mode: "single"` selects one planner/turn; `mode: "comparison"` selects two planners/five turns; omission retains comparison for existing clients
  `start: true` also queues the first turns in that transaction
- `GET /v1/camps/{camp}/planning` returns bounded board summaries
- `GET /v1/camps/{camp}/planning/{plan}?version=N` reads one saved version
- `POST /v1/camps/{camp}/planning/{plan}/actions` accepts start, pause, resume,
  cancel or restart with an expected ticket revision; cancellation can include
  `expected_version` to tolerate progress within that version, and restart can
  set `input.start: true` to start the new version atomically
- `POST /v1/camps/{camp}/planning-requests/claim` claims work only for the device's
  offered, available sessions
- Existing request actions acknowledge start, heartbeat, finish and reconciliation

## Local-plan import API

The development Go API accepts an existing local plan at
`POST /v1/camps/{camp}/planning/import`. This is the storage portion of local-plan
promotion. The native review/upload flow is not connected yet. The endpoint
can also attach implementation histories already copied to private Camp
sessions. Keep the original local plan until the complete native flow ships.

The request uses the normal native bearer token and an `Idempotency-Key`. It
contains a `source_id`, `title`, destination `project_id` and `versions`. The
source ID is a stable, lowercase SHA-256 identity for the local ticket, scoped
to its owner and destination Camp. Versions are numbered consecutively from
one, with at most 100 versions and a 16 MiB encoded request limit. Ordinary
planning requests retain their existing body limit.

Each version supplies its saved `mode`, `state`, `brief`, `budget_policy`,
`merge_slot`, `planners`, `sources`, `local_sources` and `steps`. Planner session
IDs must resolve to the importing person's own accessible sessions in that
Camp. Model, effort and speed remain the saved values; historical execution
locations are left unrecorded. The destination project must exist and be active.
Historical effort and speed values are bounded strings, including settings from
older or newer provider versions. Import does not validate them as executable
choices. A restarted plan still uses the normal supported-settings checks.

`sources` are the existing pinned Camp message references, with current access
checks. `local_sources` carry quoted local messages: an opaque hashed ID and
revision, a title and ordered messages with their IDs, roles, text and optional
model settings. A version allows 20 total sources, at most 120 messages per
quoted source and 256 KiB of quoted message text. Individual messages are
limited to 48 KiB. Cloud references must remain references so revocation keeps
working; they must not be converted to quoted copies to avoid access checks.

Steps retain their ordinal, saved state, output, error and reported token usage.
There are either no steps or all one/five steps for the selected mode. A ready
version requires every step to have completed with output. Running versions and
running steps are rejected: stop local work and resolve its outcome first.
The stored import snapshot preserves paused, draft and uncertain outcomes;
those versions appear as needing review. Empty drafts remain empty.

An import is private, creates no provider requests and cannot be started or
resumed. Restart requires a reviewed set of current planner assignments and
creates a new version through the ordinary workflow. Cancel is also available.
Import metadata remains attached to the historical version, and its steps have
no provider request IDs. The API does not assert that a provider produced the
owner-supplied historical text.

Repeating the same source ID and payload returns the same ticket, including
after command receipts expire. A different payload for that source ID returns
`409 request_conflict`; importing never overwrites the first copy. Reads and
replayed responses recheck current Camp and source permissions. Revoked source
access hides the affected version and board entry.

Schema 26 stores these snapshots and import identities. Migrate with the
normal [self-hosting procedure](self-hosting.md). Downgrading an empty import
table is supported; once imports exist, retain an import-aware server. The
migration refuses to discard them. This backend increment is not a released
native transfer feature.

### Native import preparation and recovery

`LocalPlanImport.prepare` maps a local ticket into the import contract. It
requires an owned, published planner binding for each participant and a pinned,
complete cloud transcript for each implementation. Message IDs, order, text and
model metadata must match the local saved transcript. Missing, changed or
unfinished history fails preparation instead of being omitted. Local source
quotes are checked against their saved digest; Camp sources retain their pinned
references and permissions.

The mapped request contains opaque hashes, Camp IDs and historical content.
It excludes local profile roots, working directories, transcript file paths and
account configuration. It retains every version and implementation attempt,
including uncertain outcomes. The original local ticket is not changed.

`CampPlanImport` stores a single reviewed request in the owner-only, scoped
SQLite cache. Submitting requires the same local ticket fingerprint that was
reviewed. Approval persists before network work; an interrupted approved upload
reuses its exact body and idempotency key on reconnect. Changing Camp or signing
out clears visible state, and deleting the scoped cache also clears the saved
request. Cache epochs fence late writes. Source permission changes stop result
acceptance, including responses already in flight. A paused approved import
retains its journal for explicit retry.

The HTTP client allows up to 16 MiB only for the exact planning-import POST
route. Other requests retain the 2 MiB limit. The final response must match the
reviewed owner, Camp, project, version and saved planning snapshot before the
controller installs it.

These components are tested independently of the UI. The saved-history
publisher is implemented; the coordinator and native review screen remain
unfinished. The current app does not yet expose a complete local-plan import
action.

### Read-only history sources

A copied planning participant or implementation transcript uses a session with
`history_only: true`. Set this when creating the source through the existing
session publication API. It is immutable: a normal session cannot be changed
into a history source, and a history source cannot become an executor. Upload
transcript pages through the normal versioned transcript API and pin the copied
revision in the planning import. Profile and source IDs identify the copy;
provider credentials are not part of the publication payload.

History sources support authorized transcript reads, attached context and
explicit sharing. They cannot accept prompts, become planning executors,
change execution ownership or location, create cloud workspaces or start
session transfers. `allow_requests` and `external_owner` must be false, and
their state cannot claim running or waiting work. The server enforces these
rules even if an older client offers an execution action.

The native app retains history-source access records for plan and transcript
readers, while excluding them from ordinary board cards, recent sessions and
executor selectors. The saved implementations remain inside their parent plan.

Schema 29 adds the marker and database constraints. Existing sessions default
to normal behavior. Downgrading requires there to be no history-only records;
the migration refuses to remove their execution protection.

`CampSharing.configureHistory` publishes a frozen `HistorySourceSnapshot`,
using the same durable profile, session and transcript operations as ordinary
sharing. It does not read the provider's live files or launch a CLI. Snapshot
identities and cloud IDs are deterministic per person and Camp. The publisher checks
the existing server transcript before uploading and verifies the complete
copy before reporting it saved. A changed or non-prefix server history needs
review; it is never overwritten as part of recovery.

Publication allows at most 2,000 messages and 8 MiB of text per snapshot, with
the transcript API's 1 MiB text and 2 MiB encoded limits per page. One message
must fit a page. Stored snapshot publication is limited to 32 MiB of text in
the local Camp cache. The snapshot and pending request survive restart, and
clearing that cache removes them. A completed copy can be recovered by its
stable identity without writing the transcript again. The same human owner
can finish an incomplete history copy from another authenticated Mac; live
transcript writes keep their existing device binding and execution ownership
does not change.

The native reader retains import metadata in its scoped cache and shows the
original saved state, step errors and unconfirmed outcomes. Quoted sources open
in the existing snapshot inspector after a fresh authorized plan read. Changing
Camp, losing plan access or observing a source permission change closes the
snapshot; a late response cannot reopen it. Ordinary shared-plan source chips
use the same access checks.

Start over carries quoted local sources into the new draft. Inspect a source
or remove it with its close control before starting. Removing it affects only
the new revision; the saved version keeps its context. Native import review and
implementation-history transfer remain release work.

On restart, `input.quoted_source_version` names the saved version and
`input.quoted_source_ids` selects its quoted sources. The server copies only
those stored snapshots into the new revision. An explicit empty array removes
them; omitting the selection when quotes exist is rejected. A different plan's
source IDs cannot supply content. Ordinary Camp references and quoted messages
share the 20-source and 256 KiB context budget.

Schema 27 persists carried quotes independently from imported results. Both
owner-Mac workers and cloud runners receive them through the existing context
tool, keeping the brief separate from source text. Starting the request requires
the worker's acknowledgement of `quoted_context_digest`; an older worker that
ignores the new field cannot start without the context. This acknowledgement
records protocol support, not proof of what the model read. Sign-out clears
quotes from retained local recovery records.
Quoted-context citations contain the plan, version and source identity. Opening
one fetches that authorized version and selects the referenced message; it does
not substitute the latest plan or a newer source revision.

## Imported implementation history

An import can include up to 100 `implementations`. Each names its saved plan
version, source identity, provider and copied Camp session, with the exact
transcript revision and visible message count. The session must belong to the
importing person in that Camp and match the provider. Import checks the count
against the stored revision and limits it to 2,000 messages and 8 MiB of text.
This prevents a partial transcript copy from being accepted as the full history.

Saved model settings, initial prompt, plan/context fingerprints and historical
permission settings remain attached to that implementation. Up to 200 attempts
retain their prompt, outcome, error, settings, permission fingerprint and
reported usage. Running attempts are rejected; an unconfirmed outcome is stored
as `uncertain`. This is historical evidence supplied by the owner and never
permission to run another turn. Profile paths and provider credentials are not
fields in the import contract.

The history uses the existing versioned transcript store. Later changes to the
source session do not change the imported revision. Every read checks Camp and
plan access plus access to the source session, including reads of attempts.
Unsharing that source removes another member's access to its implementation
history. The import and its attempt rows commit in the same transaction as the
plan; repeating an import returns the same records without creating work.

- `GET /v1/camps/{camp}/planning/{plan}/implementations?version=N&after=0&limit=20`
  lists a version's implementations, with a maximum page size of 50
- `GET /v1/camps/{camp}/planning/{plan}/implementations/{id}` returns its saved
  metadata and initial prompt
- `GET /v1/camps/{camp}/planning/{plan}/implementations/{id}/attempts?after=0&limit=10`
  pages through attempt records, at most ten per response
- `GET /v1/camps/{camp}/planning/{plan}/implementations/{id}/transcript?before=0&limit=200`
  reads the pinned transcript using the existing message-count and byte limits

Implementation and attempt pages return a `next` cursor of zero at the end.
Transcript pages retain the ordinary `before`/`has_more` contract. Schema 28
stores the links and attempt records and refuses a downgrade that would erase
them. The native planning workspace lists saved implementations under History.
Selecting one opens its chat and attempt records in the same workspace, with
the existing provider marks, model controls and transcript component. Earlier
messages and further attempts load in bounded pages. Switching records cancels
the previous read; sign-out or observed permission changes clear its contents.
Native publication and continuation of these imported implementations remain
release work.

Planning tables were introduced in schema 5. The current required schema is
defined by `SchemaVersion` in [the database package](../server/internal/database/database.go).
Schema 19 stores each planner's execution binding. Older history remains
readable and shows “Location not recorded”; the migration does not invent a
past assignment from today's session. Already queued requests keep their own
binding, but a legacy revision needs a restart before it can queue another
phase. See [upgrade guidance](self-hosting.md) before migrating.
Migration 7 retains existing versions as comparison plans with five turns; migration 8 preserves existing project assignments and adds each Camp’s Inbox. Deploy the migrated server and updated native client before creating single plans; older clients cannot display those plans. Older request receipts and session
history are retained. Run migrations before the new server. The public deployment templates use the
same image for the migration job and API.

Tests cover single-turn completion, conversion to a comparison version without changing earlier history, the five-step sequence, preserved output/settings, private events,
strict-budget refusal, cancellation, paused claims, version restart, late
completion, source revocation, unknown-outcome recovery and provider concurrency.
Native tests exercise either provider as the sole planner, independent executor selection from a single plan, old history without a mode field, isolated mode, five-step ordering, independent settings,
pause/resume, preserved restart history, unknown-outcome recovery, bounded
outputs and usage parsing. Computer-use checks use a clearly synthetic provider
fixture in a disposable development app; they do not prove actual provider
execution. Shared workspace tests cover durable creation, lost acknowledgements,
pending cancellation, model settings, same-ticket restart, cache isolation and
idle-worker wake-up after resume. Local implementation tests cover independent executor selection,
project binding, approval invalidation, queued continuation, terminal transcript
updates, duplicate-card suppression and restart recovery. Real two-provider
execution and cloud runners remain release work. Shared worker results persist before reporting
completion and retain their output and usage through a lost acknowledgement;
recovery never repeats the provider turn automatically.

The native worker walkthrough shares two synthetic local sessions, starts a
plan through the real Go API, and verifies five isolated subprocess turns,
saved outputs and usage, the completion notification, and both result panes.
An active cancellation stops the current fixture process and prevents the next
step. The shared workspace walkthrough also creates a plan, pins a message,
selects model settings, restarts on the same ticket and pauses/resumes it.
Snapshot reads retry a concurrent database update at most
three times; those retries never repeat provider work or mutation commands.

The October 21 single-planning check also ran a real ephemeral Codex turn, received streamed output and completion, and confirmed that the temporary project was unchanged. A live Claude attempt returned the provider's weekly-limit error; successful real Claude execution remains a release gate. The bridge uses the current `on-request` approval policy with its existing read-only planning sandbox. Session permission controls remain separate work.

The native single-plan walkthrough used a synthetic Claude turn, pinned Codex context, and a separately selected Codex implementation model. It completed one planning turn, approved one fixture file change, then restarted the same ticket in comparison mode. The saved ticket retained its one-turn first version, five-turn second version, matching context and completed implementation history. Shared single-planning creation, persistence and one-turn completion pass the native unit and real-PostgreSQL suites; a live multi-device walkthrough is still required.
