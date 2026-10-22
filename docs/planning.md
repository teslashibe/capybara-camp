# Planning workflow

The V1 development app offers **Plan** for one model and **Plan with comparison** for two providers. Both keep their board ticket, version history, context and model settings. A single plan makes one bounded planning turn; comparison makes two independent drafts, two reviews and a combined plan.

The V1 development server has a durable planning queue for both modes. The Mac app
also has a private local planning coordinator and paired-chat workspace. Local
plans persist on the owning Mac and use isolated official CLI turns. The native
workspace also creates and controls shared planning tickets. The owner-Mac
worker executes their queued steps, while cloud runners remain unfinished.
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

Use the Plan menu's shared Camp actions to create a saved draft. Choose one already-shared source session for Plan, or two for comparison, then select model/effort/speed settings and pinned message ranges. Comparison also selects the combining provider. Only the selected
session owner's Mac runs its turns, using that owner's local profile. Plans
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

Planning tables were introduced in schema 5. The current required schema is
defined by `SchemaVersion` in [the database package](../server/internal/database/database.go).
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
