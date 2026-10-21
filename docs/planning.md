# Planning workflow

The V1 development server has a durable five-step planning queue. The Mac app
also has a private local planning coordinator and paired-chat workspace. Local
plans persist on the owning Mac and use isolated official CLI turns. The native
workspace does not yet create or edit shared planning tickets. The owner-Mac
worker can execute their queued steps, while cloud runners remain unfinished.
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
source message ranges, two provider profiles, model/effort/speed settings and
merge provider. Each provider drafts independently, reviews the other draft
once, and one produces the combined plan. The combined-plan prompt explicitly
retains unresolved disagreements.

## Local implementation

The combined plan opens an implementation setup step in the same workspace.
Choose an executor profile, model, effort and supported speed independently of
the two planners. Selecting a different profile retains the plan's original
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

At most one planning step per provider can be leased to a device, including
across different tickets and profiles. The two providers may run concurrently.
The five-step limit bounds durable planning-turn requests, not a provider's
internal network retries or tool-related model calls. Reported token usage is
preserved when available; missing usage is not reported as zero.

The initial API defaults to strict token ceilings and refuses to start because
these CLI adapters cannot guarantee those ceilings. Explicit `budget_policy:
"calls"` enables the five-step workflow with 40,000 input and 8,000 output token
targets for reporting only. The release default is awaiting the owner's budget
choice; these targets must not be described as enforced caps. The native
workspace requires an explicit five-turn selection and leaves it off by default.

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

- `POST /v1/camps/{camp}/planning` creates a draft with an idempotency key
- `GET /v1/camps/{camp}/planning` returns bounded board summaries
- `GET /v1/camps/{camp}/planning/{plan}?version=N` reads one saved version
- `POST /v1/camps/{camp}/planning/{plan}/actions` accepts start, pause, resume,
  cancel or restart with an expected ticket revision
- `POST /v1/camps/{camp}/planning-requests/claim` claims work only for the device's
  offered, available sessions
- Existing request actions acknowledge start, heartbeat, finish and reconciliation

Planning tables were introduced in schema 5; the current server requires schema
6, including recipient notifications. Older request receipts and session
history are retained. Run migrations before the new server. The public deployment templates use the
same image for the migration job and API.

Tests cover the five-step sequence, preserved output/settings, private events,
strict-budget refusal, cancellation, paused claims, version restart, late
completion, source revocation, unknown-outcome recovery and provider concurrency.
Native tests exercise isolated mode, five-step ordering, independent settings,
pause/resume, preserved restart history, unknown-outcome recovery, bounded
outputs and usage parsing. Computer-use checks use a clearly synthetic provider
fixture in a disposable development app; they do not prove actual provider
execution. Local implementation tests cover independent executor selection,
project binding, approval invalidation, queued continuation, terminal transcript
updates, duplicate-card suppression and restart recovery. Real two-provider
execution, shared planning creation/editing in the native workspace and cloud
runners remain release work. Shared worker results persist before reporting
completion and retain their output and usage through a lost acknowledgement;
recovery never repeats the provider turn automatically.

The native worker walkthrough shares two synthetic local sessions, starts a
plan through the real Go API, and verifies five isolated subprocess turns,
saved outputs and usage, the completion notification, and both result panes.
An active cancellation stops the current fixture process and prevents the next
step. Creating and controlling shared plans from the native planning workspace
remains unfinished. Snapshot reads retry a concurrent database update at most
three times; those retries never repeat provider work or mutation commands.
