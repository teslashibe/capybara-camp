# Planning workflow

The V1 development server has a durable five-step planning queue. The Mac app
also has a private local planning coordinator and paired-chat workspace. Local
plans persist on the owning Mac and use isolated official CLI turns. The native
workspace is not yet connected to the shared planning queue or cloud runners;
implementation approval and execution remain release work.

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

Steps reuse the existing approved request and lease lifecycle. A separate
planning claim endpoint prevents older chat workers from resuming an anchor
session by mistake. Planning uses the anchor only to identify its provider
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

Schema 5 is additive and retains older request receipts and session history.
Run migrations before the new server. The public deployment templates use the
same image for the migration job and API.

Tests cover the five-step sequence, preserved output/settings, private events,
strict-budget refusal, cancellation, paused claims, version restart, late
completion, source revocation, unknown-outcome recovery and provider concurrency.
Native tests exercise isolated mode, five-step ordering, independent settings,
pause/resume, preserved restart history, unknown-outcome recovery, bounded
outputs and usage parsing. Computer-use checks use a clearly synthetic provider
fixture in a disposable development app; they do not prove actual provider
execution. Real two-provider validation, shared worker integration and
implementation approval remain release work.
