# V1 delivery status

October 22, 2026 · [Release epic](https://github.com/teslashibe/capybara-camp/issues/1)

V1 is not released. The public app and website still describe v0.0.1. Local
development builds contain more functionality, but implementation, deployment
and acceptance are separate milestones.

| Area | Implemented locally | Still required for V1 |
| --- | --- | --- |
| Local sessions | Automatic multi-profile import, board/chat, model/effort/speed controls, sub-agents, streaming, dictation, steering and queues | Finish live provider/recovery and performance validation after the Go migration; complete the final design comparison below |
| Planning and projects | Single-model plan, provider comparison, saved versions, live shared planning streams, per-Camp execution defaults, shared-draft per-planner destination selection, pinned execution assignments, same-ticket local implementation, project assignment across repositories, native private history-import review and resumable source publication, imported-implementation reader | Native streaming/import visual acceptance, shared implementation and owner-routed approval completion; cross-device validation |
| Shared Camps | Go/PostgreSQL auth, membership, sessions, events, requests, native cache/outbox and owner-Mac worker | Real independent users on two Macs, remaining permission/recovery cases, production deployment |
| Onboarding | Automatic profile discovery before full import, optional Camp sign-in, owner-only cloud account setup, GitHub connections, projects, execution defaults, notifications, skip/resume | Final onboarding validation after remaining services land |
| Go migration | All native runtime actions use bundled Go; Python source, native fixtures and build/test/CI requirements removed | Final live-provider and release acceptance gates below |
| Cloud execution | Owner-only accounts/installations, native account forms, scoped grants, sign-in recovery, credential encryption, Kubernetes provisioning, explicit Local/Cloud assignments and new-plan defaults, native Run on controls, reviewed Local-to-Cloud file capture and private transcript copy, multi-ticket per-account scheduling, native steering/queue/stop controls, durable approval/control delivery, a shared request queue, provider subprocess consumption, event streaming and credential revision fencing, mandatory Linux child-process isolation | Native plan-import acceptance, remaining signed-in recovery states, target-runtime isolation validation, complete history/attachment and reverse transfers, complete reported runtime settings and usage states, live sandbox/network/login/control validation and disconnected-Mac execution |
| Git | Project repository links, local CLI project execution, owner-bound GitHub App authorization, native repository browsing, encrypted tokens, signed revocation, exact-commit and scoped read-token adapters, workspace records, exclusive claims and isolated runner checkouts with restart/cancellation recovery | Execution grants, Git actor attribution in execution, branch/push/PR recovery and live two-user GitHub validation |
| Memory and skills | Merged Cortex exact-selection and transactional guard API; embedded store, private/project observation API, saved job metadata, derived-memory source checks, replay and migration protection; isolated CLI inference and Cortex model adapter | Job dispatch, native provider/model selection and memory inspection, skill editing/version rollout and live permission tests |
| Hosting and release | Public Kubernetes/K3s base, optional runner overlay, pinned provider image, local development builds and resumable notarization/packaging tooling | Cortex deployment templates, Hetzner provisioning with the owner, backup restore, two clean reviews, soak tests, actual Developer ID/notarization acceptance, universal V1 release and recorded website demo |

Next: finish the remaining transfer cases and full-history migration, then memory/skills
and shared execution. Continue the native design audit as those surfaces are
implemented. Validate the full workflow locally before the owner provisions
the Hetzner box with us. Cloud sign-in and
the two-Mac acceptance run follow that deployment. Hardware has not been bought
or provisioned for this goal.

October 22 native Save to Camp review now passes its focused local API
walkthrough. The actual Go/PostgreSQL API received three synthetic planning
revisions, quoted context and six implementation messages with two saved
attempts, all private. The rebuilt reader opens a single implementation directly,
uses shared controls and formatted settings, marks completed implementation and
returns to provider plans when changing revisions. Read-only imported history
no longer offers execution actions. Native regressions and strict bundle
signature verification pass. Pointer edge checks remain incomplete because
computer use intermittently reports no windows. Republishing newer local work
after a saved copy, live migration, multi-device acceptance and the complete
design audit remain open; no cloud deployment or release was made.

The following native planning pass corrected misleading provider-tile status
and keyboard navigation. Local and shared results use one outcome presentation;
unconfirmed work no longer says Ready, and a failed review is not hidden by a
completed draft. Native fixtures verify unconfirmed, failed, paused, cancelled
and completed states. The planning navigation and stages now support Tab,
Shift-Tab, Space and Return, with disabled Implementation skipped. Focused native
tests and strict signature verification pass. These checks close those specific
findings; pointer edge checks and the rest of the keyboard inventory remain open.

The October 22 transfer review now keeps checking the source hold while the
user reviews permissions. Computer use reproduced the stale Source verified
label, then verified the correction with the real native handoff coordinator
and bundled Go guard: changed project files and changed child history prevent
the move; an unchanged main chat with two subagents completes the review.
Server responses were synthetic, with no provider execution or cloud upload.
The invalidated/expired/released hold regressions and packaged-helper checks
pass. This closes those focused component walkthroughs, not the signed-in
full-app or two-Mac transfer acceptance gates.

The memory dependency is merged in
[Cortex PR #1](https://github.com/teslashibe/cortex/pull/1): exact input selection,
one provider call, a required permission guard inside the memory commit and a
saved-outcome lookup. Its Go race suite, vet, build and upstream CI pass,
including concurrent revocation and cancellation recovery. Camp now embeds
that store in a private PostgreSQL schema and provides authenticated observation
saves, metadata pages and source inspection. Schema 30 retains the original
source grant and refuses a downgrade that would discard observations. API
startup and readiness require both schema sets.

The complete Go race suite and vet pass with disposable PostgreSQL 17. The
memory tests pass again after correcting a policy-lock hold during Cortex
copies. They cover private/project separation, concurrent duplicate saves,
response-loss recovery, revocation during a delayed copy and filtered event
replay. The Linux commands build; native worker, cloud and state regressions
pass with the updated Go helper. These are local and synthetic checks.
The actual migration command and API also pass a fresh-database smoke test with
the restricted `camp_runtime` role: both stores are readable, schema-ledger
writes are denied and an incomplete Cortex schema returns HTTP 503 readiness.
[Project memory](memory.md) records the API and remaining integration. Dedicated
CLI consolidation jobs, native memory screens and live two-Mac acceptance are
still required; this does not complete memory or skills.

The memory inference primitive now uses the existing Go CLI transports with a
new ephemeral conversation and private temporary working directory. It preserves
the selected profile/model/settings and sends Cortex's system and user messages
without starting project tools. The model adapter passes the exact prompt hash
to its dispatcher and does not retry. Focused tests use both provider protocols
with large Unicode replies and verify cleanup, cancellation, profile exclusion,
tool rejection and missing usage metadata. The complete Go race suite and vet
pass, followed by focused tests of the final validation changes. Linux builds
and native worker, cloud and state regressions pass. A synthetic prompt also
completed through the real Codex CLI 0.163.0, with reported model and token
counts. Successful real Claude inference remains an open acceptance check.
Durable job dispatch and the native memory UI remain open; no real provider
consolidation through the full Cortex workflow has been run.

October 22 CI caught cancellation racing with a startup acknowledgement: pipe
closure could replace `context.Canceled` with a CLI connection error. Inference
now preserves the cancelled context after the provider has been reaped. The
regression covers cancellation before initialization as well as during startup,
profile-lock release and temporary-file cleanup. Thirty race-test repetitions
and the complete local helper suite pass.

Schema 31 now saves the reviewed namespace, observations, existing-memory
digests and local profile or cloud installation in a job. Creation, reads,
cancellation and events check the full source set. This includes inherited
memory sources and prevents a later re-share from reviving an older grant.
The creator can cancel a revoked job without reading its old input references.
The new memory API returns current source-linked memories and their review
digests. Jobs remain queued until the dispatcher is connected; no provider work
starts through these metadata endpoints yet.
The full Go race suite and vet pass at schema 31 against disposable PostgreSQL
17. Tests cover concurrent replay, private and project access, source lineage,
revocation/re-share, cancellation after revocation, local/cloud target binding,
bounded provenance reads and retained-data downgrade refusal. HTTP tests cover
creation, listing, inspection and cancellation. Linux commands build; native
worker, cloud and state regressions pass. Execution and native memory acceptance
remain open.

Shared planning now streams each active step into its existing provider pane.
The owner-Mac worker sends bounded snapshots through the same event store as
cloud runners; the native reader replays by sequence and follows new text.
Reported model, effort and speed are preserved in stream metadata. Readers
check the plan and all referenced sessions, and private planning output is
excluded from ordinary session streams and anchor transcripts. Native tests
cover slow connections, lost responses, switching versions and revoked access.
Five focused native suites pass, including the owner-Mac worker's synthetic CLI
stream before completion. The final full Go race suite and vet pass against
PostgreSQL 17, and Linux command builds pass. The server tests cover private
cloud plans, source revocation, request leases, replay and keeping planning
output out of anchor chat history. Both audit apps pass strict signature
verification and the staged secret scan is clean. Visual and live multi-device
acceptance remain open.

Schema 29 adds read-only source records for the native history publisher.
The server rejects execution, ownership changes, workspaces and transfers for
these records, while allowing authorized transcript and context reads. The
native app retains their access records but excludes them from ordinary cards,
recent sessions and executor selectors. Existing sessions keep their behavior.
The source-copy publisher now uses frozen snapshots, deterministic identities,
durable publication requests and a full server-content comparison. It can
recover completed copies after a cache reset and let the same owner finish an
incomplete copy from another Mac without changing execution ownership. The
native review screen and coordinator are now connected. The new action appears
in plan and implementation card menus and in the planning workspace. It saves
the approved snapshot, publishes source copies, imports the plan and opens the
shared planning view. A scoped saved-copy link avoids duplicate cards while
preserving newer local work and the original local record.
Six focused native suites pass: promotion, import, sharing, shared planning,
local planning and store state. They cover restarting a persisted import,
manual Retry after a lost response, cache erasure, empty drafts, retained local
history and newer local edits. Both synthetic audit apps have been rebuilt
and pass strict signature verification with the existing development identity.
These checks use synthetic transports; the native visual audit and real
two-Mac/cloud acceptance remain open.
The saved-snapshot publisher and import binding tests pass for long Unicode
messages, lost responses, a cleared local cache, conflicting remote content,
and a partial copy completed on another device. Publisher writes recheck the
cache epoch. Normal live-session writes remain device-bound; the history-only
exception requires the same human owner. Full Go race tests and vet pass, as
do Linux command builds and the focused native suites. Native visual and
two-Mac acceptance remain open.
Native state, worker, import and planning suites pass. Full Go race tests and
vet pass at schema 29, with additional targeted history/board checks after the
last guard change. Tests cover private context, immutable source type,
execution rejection and safe migration rollback. Linux command builds pass;
both synthetic audit apps are rebuilt and signature-verified. The staged
secret scan is clean. The Mac is unlocked and native review has resumed. No
cloud deployment or release was made.

The synthetic transfer window passed a computer-use walkthrough on October 22:
file selection and preview, excluded environment file, imported-context choice,
preparation, source verification, permission review and the final Runs in Cloud
state. This proves the component flow against its fixture, not a live cloud
transfer. Back/re-entry and cancelling a prepared destination also passed.
Source changes, child history, signed-in full-app and
two-Mac checks remain. The main audit build also exposed a helper isolation
regression; that run was stopped before recording and its cache is excluded
from demo evidence. The rebuilt app passes the helper handshake, rejects a
regular helper in audit mode and retains only the twelve synthetic sessions
through refreshes. The local Go race suites, native conversation/approval
checks and strict bundle signature verification pass. Parent/child navigation,
older-session context and the five-turn comparison passed native click-through.

The October 22 native review also corrected two product UI issues: local plan
detail sections now reveal their result and next action, and selecting a
permission profile or sandbox saves the whole choice atomically. The narrow
window walkthrough passed separate executor/model selection, both permission
switches, automatic In progress movement, steering, queued continuation and
retained plan/implementation history. Four focused native suites and fixture
protocol race tests pass. These checks use a synthetic execution context and
permission catalogue; live CLI permission validation remains a release gate.

The notification drawer now supports Tab and Space/Return for its custom
actions and rows, with stable identifiers for duplicate-looking completions.
Native keyboard checks passed filtering, opening chat and saved-plan results,
mark-all-read, settings and Escape back to the bell. The notification suite and
strict bundle verification pass. Linux and Intel Mac helper builds also pass.
Desktop-banner presentation and the full final UI review remain open.

The separate arm64 development app, `build/Capybara Camp V1 Preview.app`, now
builds with its bundled Go helper and the existing development signing identity.
Strict deep signature verification passes. It uses a separate preview bundle
identifier and retains version 0.0.1; it is not a notarized V1 distribution.

Native import preparation now validates all saved versions, local quotes,
planner bindings and copied implementation transcripts before creating the
request. A scoped upload journal persists approval and the exact retry key.
Tests cover 100 versions, long transcripts, changed reviews, lost responses,
local installation failure, revoked sources, sign-out and cache epochs. The
HTTP upload allowance is limited to the exact import route. Historical model,
effort and speed strings survive the import without becoming executable
settings. The focused native suites and full Go race suite pass; Go vet and
Linux builds pass. Source publication and the native review screen now use this
journal through the coordinator described above. Live acceptance remains open.

Schema 28 adds historical implementations and paged attempt records to plan
imports. The transcript stays in the existing versioned session store, pinned
to its imported revision. Import checks the copied message count and 8 MiB
text limit; it cannot claim another person's source or create provider work.
Metadata, attempts and transcript reads check both plan and source access.
The full Go race suite and vet pass, followed by targeted checks for batched
attempt writes, long-message preservation, pagination, revoked access and
oversized histories. Linux builds pass. The native reader now opens saved
implementations from the planning History view; the native importer now supplies
the private source copies for that reader.
The Mac is still locked for computer use.
The native reader has separate Chat and Attempts views within the planning
workspace, using the existing model controls and transcript layout. Its pages
validate source identity, revision, ordering, counts and byte limits. Switching
implementations cancels stale reads, and sign-out or observed permission changes
clear the displayed history. The focused native implementation, planning and
import suites pass, including 230 messages, a 300,000-byte Unicode response,
paged attempts, malformed data, cancellation and revoked access. Visual
acceptance remains open; the native publication flow is now connected.
The signed synthetic audit apps are rebuilt with this reader and pass strict
signature verification. The final staged secret scan is clean.

The local-plan import backend preserves saved versions, planner settings,
step outputs/errors/usage and bounded quoted local context. It creates private
history without creating provider requests. A stable source identity prevents
duplicate tickets after a lost response; changed payloads conflict instead of
overwriting history. Non-ready imports require review and a new planning
version before execution. Source permissions are rechecked on reads and receipt
replays. Schema 26 retains import snapshots and blocks a downgrade that would
discard them. Native import review and implementation-history preservation are
now connected; computer-use acceptance remains open. The contract is in
[planning](planning.md#local-plan-import-api).
The full Go race suite and vet pass with disposable PostgreSQL at schema 26.
Tests cover private imports, replay deduplication, source revocation, input
bounds, empty drafts, reviewed restart and migration retention. Linux server,
runner, controller, helper and migration builds pass; the staged secret scan
is clean. These backend checks do not establish live acceptance.

The native reader retains imported source metadata and displays saved
errors and unconfirmed outcomes in the existing planning workspace. Source
inspection rechecks the plan and source access, including late responses after
sign-out or revocation. Restarted drafts now carry selected quotes into a new
immutable version. The native upload/review flow now feeds this reader.
The focused native import, planning and context suites pass. They exercise
metadata/cache round trips, invalid snapshots, blocked historical execution,
and a read held across revocation or sign-out. The shared app module compiles;
the staged secret scan is clean. Visual acceptance remains pending because
computer use still reports the Mac as locked.
Both synthetic audit apps have been rebuilt with this module and pass strict
signature verification using the existing development identity.

Schema 27 retains selected quoted context when an imported plan starts a new
version. The native draft keeps its source chips, with explicit removal, and
both worker paths pass the saved text through the existing context tool. A
worker must acknowledge the context digest before starting. Citation links
identify the exact plan/version/source; opening them rechecks access. Sign-out
scrubs quoted context from retained local recovery records. Full Go race tests
and vet pass; focused native import, planning, worker and context tests pass.
Final targeted checks cover removing/reinstating old sources, unknown source
IDs, worker acknowledgement and source-access rejection. Linux builds pass and
the synthetic audit apps have been rebuilt and signature-verified. Native
import/upload and copied implementation-history publication are now connected;
full migration and visual acceptance are still open.

The current Local-to-Cloud increment adds native working-file selection, exact
captured-file previews, a pinned Git base, destination preparation, source
verification and final permission review. Upload and cancellation state survive
a restart. Native tests cover a lost create response without duplicating the
transfer, lost cancellation recovery, an intermediate claimed source, explicit
retry after rejection, and original-device ownership. A packaged-app integration
test reaches the actual Go change-list and capture commands. The Go capture
tests cover renames, excluded credentials/links and an explicit no-file move.
These checks do not complete child-history/attachment migration, unpushed commit
transport, Cloud-to-Local moves, or live two-Mac acceptance.

The final focused native run passed transfer, bundled-helper integration,
execution, source handoff, onboarding, cloud account, GitHub and execution-default
suites. The Go transfer/local-CLI race suites and vet passed. A full native run
reached the permission-restore check but could not reopen its protected test
file while macOS was locked; an isolated protected-file check reproduced that
restriction. Repeat the full suite after unlocking. The staged-source secret
scan passed. The native click-through is also waiting for the Mac to be unlocked.

The next child-history increment adds parent/profile-bound local source IDs and
revision-checked export pages, reusing the existing transcript index. Both
provider fixtures export 230 messages without losing or duplicating messages.
Tests reject foreign parents/profiles, changed snapshots, unsafe paths, invalid
cursors and overflowing child lists. Codex database history remains available
after its rollout file rotates; fallback files must identify the selected child.
The packaged Mac helper exposes and exercises both commands. Cloud storage and
inherited access controls are now implemented by the schema-24 API below.
Native publication, runner context and native cloud navigation are implemented
below; live acceptance remains open.

Schema 24 adds versioned child history beneath a parent session. Updates reuse
the existing message store and advance the parent's transcript revision. Child
rows are excluded from the board and ordinary session/execution endpoints.
Private and revoked parent permissions apply to current and historical child
reads. Partial uploads stay unreadable, and a removed child cannot be opened
through an older snapshot. The API has bounded batches and idempotent receipts.
The full Go race suite and vet passed with disposable PostgreSQL, including
the new storage/HTTP tests and the existing main-transcript and runner suites.
The separate controller-role check denies child metadata and message-body reads,
and the Linux server, runner, controller and migration builds pass.
A focused PostgreSQL rerun also verifies a 230-message child uploaded in two
batches and read back over two pages without mixing it into the main transcript.
No cloud has been deployed and no native child-publication flow has been accepted.

The native child publisher now uses that API, including multi-page copies,
separate child message hashes, lost-response recovery and removal/reappearance.
The existing chat sidebar and transcript component can list and read cloud child
histories with older-page loading. Child refreshes preserve the current
selection; a late result is discarded after changing parents or revoking access.
Local source activity supplies observed child status without relabeling stored
messages. The remaining child work includes native/live two-Mac acceptance.
The Mac is
still locked, so the new cloud UI has not had its computer-use walkthrough.
The focused native suites pass for sharing, cloud child navigation state, cloud
state and the packaged helper. They verify a 230-message child upload, replay of
a lost response without duplicate writes, removal/reappearance with the same
identity, separate parent/child message hashes and an untruncated model ID.
Parent refresh, navigation into a plan and revocation reject stale child reads.
The source-only publication endpoint also passes the Go database and HTTP tests.

The cloud runner now exposes complete child histories through its read-only
context tool at the queued parent revision. Catalog lookup is lazy and bounded
to 50 descriptors per call; history retains the existing 16 KiB segments and
48 KiB page budget. Synthetic Codex and Claude processes each read 150 child
messages through the actual tool connection. Database tests preserve a 300,000-byte
Unicode response and reject incomplete, removed and unrelated children; later
edits do not replace the queued snapshot. Native citation tests open the exact
child message and reject cross-parent snapshots. The focused backend race tests
and native child/context suites pass. The full Go race suite and vet also pass
with disposable PostgreSQL, and Linux server/runner/controller/migration builds
succeed. The staged secret scan found no secrets. This is local synthetic
evidence, not live-provider or two-Mac acceptance.

Native cache version 5 adds saved child catalogs and fetched transcript pages,
with a shared 32 MiB/128-entry limit. Network failures can return a labelled
offline copy; server denials and certificate failures cannot trigger fallback.
Observed child events invalidate cached pages atomically with the event cursor.
Parent revocation/removal and cache erasure also invalidate them. A persisted
per-parent counter rejects reads and writes started before an observed child
update; the Camp epoch rejects late writes after cache erasure. Offline reads
cannot observe a remote change until reconnection. The child, cloud (84 checks),
state, context and sharing suites pass, including a real connection failure
against a reserved loopback port. Final visual and first-paint/reconnection
timing acceptance remains open; computer use is still blocked by the locked Mac.
The final shared-module build and child suite pass after adding automatic
reload when a saved child returns to a live state. The staged secret scan passes.

The next child-view refinement paints an already cached catalog and message
page before the network check finishes. It uses the same scope, permission and
invalidation checks as offline reads, and replaces the preview with the verified
response. Loading an earlier page uses one captured message base so the cache
preview and network response do not append that page twice. The child, native
state, cloud and session-context suites pass; visual latency acceptance remains open. Parent
navigation also cancels the previous child-list request, so rapid switching
does not accumulate network reads. The regression test observes cancellation
of the earlier request and still rejects its late response. The staged secret
scan passes.

The final history check is implemented in the current increment below. It
preserves independent activity in other conversations on the same profile
and checks edits between publication, review and commit.

The local exporter now provides a content revision for the selected
conversation, separate from the shared profile-file fingerprint. The native
publisher checks this revision across pages and after child synchronization
before reporting the copy complete. These content revisions are now used by
the final handoff history check. Targeted Go tests cover selected-message
edits and unrelated Codex database writes. The full local-CLI and transfer Go
race suites, vet and Linux helper/runner builds pass. Native publication tests
reject changed pages, recover with the latest complete copy, and retain the
existing parent/child and privacy checks. The packaged bridge and transfer
suites also pass. The staged secret scan passes.

The current history-binding increment saves the published main and child
content revisions with the copied cloud revision. Go capture verifies that
binding before and after reading selected files. The source guard checks it
during the hold and immediately before the persistent commit fence. Schema 25
stores the verified digest; the server rejects missing or mismatched bindings
and prevents an older unverified local handoff from claiming or committing.
The native review shows the included subagent count. Targeted local checks
cover changed main/child contents, removed children, unrelated Codex writes and
stale capture. The full Go race suite and vet pass with disposable PostgreSQL,
including legacy handoff rejection and migration/downgrade checks. Native
publication, transfer, source-guard, child-cache and packaged-helper suites pass.
The packaged helper rejects a child edited after publication. Linux server,
runner, controller, helper and migration builds pass. Live two-Mac transfer
acceptance and the final visual walkthrough remain open.
The two signed synthetic audit apps have been rebuilt and pass strict
signature verification. Computer use still reports the Mac as locked.

The transfer work includes a read-only Go capture layer for selected Git
changes and owner-bound, encrypted, resumable upload storage. Completion checks
payload and review hashes against the pinned source revisions. The staging
helper verifies the Git base and file preimages before applying files, retains
a private recovery journal, and rejects conflicting work. The contract and
remaining integration are in [transfer payload](transfer-payload.md).

The Go runner now consumes an owner-authorized destination job, downloads the
reviewed payload under its preparation claim, stages it in a separate checkout
and reports the required file-verification receipt. The integration test uses
the real Go API and Git transport with disposable PostgreSQL, drops download and
completion responses, and verifies recovery without changing the source. Schema
21 retains older uploads and adds the destination binding. Ordinary Run on
assignment rejects these workspaces outside the reviewed handoff. Complete
history/commit migration and native transfer review remain open.
The targeted upload, staging and runner tests pass, including an abrupt
staging-process exit and recovery, owner-only access, stale snapshots, concurrent
quotas and migration retention. Native end-to-end transfer and deployed Linux
acceptance remain open.
The full Go race suite and vet also pass with disposable PostgreSQL 17 at schema
21. This evidence covers the local Go implementation and synthetic integrations;
it does not complete the provider, native UI or two-Mac acceptance gates.

The server handoff protocol now adds an exclusive five-minute source hold,
blocks new prompts and assignment changes, and accepts source-bound acknowledgements
of the reviewed payload. Existing queued or uncertain work prevents a hold.
Expiry, cancellation and source drift release the guard without changing the
execution epoch. A server acknowledgement alone does not prove a CLI has stopped.
The handoff HTTP and PostgreSQL tests, full Go race suite and vet pass at schema
22. They cover source identity, exclusive holds, expiry, queue preservation and
release without an assignment change.

The cloud source worker now reconciles its terminal turn, verifies the reviewed
source files and acknowledges the handoff under a durable claim. Tests for both
provider protocols confirm the actual fixture processes have exited, recover
lost finish and handoff responses, and reject changed files without starting
another model turn. Complete history migration remains
open. The complete Go race suite, vet and Linux command
build pass with this integration at schema 22.

The native source worker now saves its handoff claim, resolves the original
profile and holds the local conversation lock through the bundled Go helper.
It verifies the reviewed Git files before acknowledging and retains the lock
until release or expiry. Reconnection rechecks the source. A session still open
in an external client is refused. Full native transfer review, live-provider
acceptance are still pending.
The full native regression suite passes, including source verification with
the real Go helper and temporary Git files. It covers lost claim/acknowledgement
responses, app-worker restart, concurrent-start refusal, file drift, expiry,
sign-out and revocation during verification. Go race tests and vet pass; the
transfer and local-adapter suites also pass after adding subdirectory support,
and the Linux commands build. These are synthetic/local checks, not a completed
native transfer walkthrough or cloud deployment.

Cloud continuation now exposes the stored main transcript through paged
`read_session` calls at the queued prompt's revision. A fresh or resumed
provider can recover older context without the previous 120-message startup
limit. The local read channel keeps the runner credential in its parent
process and checks authorization again on every page. Both synthetic provider
protocols read all 150 fixture messages, including a 300 KB Unicode message,
through the actual helper and Go API. History links use the existing native
snapshot inspector and an authorized single-message read, including messages
larger than the `@` selection budget. Child histories, attachments, complete
transfer review remain open.
The Go race suite and vet passed for the paged runtime, followed by focused
citation authorization tests. Native state, cloud and context suites passed
after adding citation inspection and dismissal on sign-out, source removal or
permission changes. Linux commands build and the staged secret scan is clean.
Live provider and full native transfer acceptance remain open.

The final cloud-assignment operation now separates owner approval from fresh
source confirmation. Schema 23 saves the permission choice, challenge and
committed receipt. Completion changes the execution epoch once, retires the old
cloud token and removes temporary upload chunks in the same transaction.
The cloud worker retains its claim through response loss. Within its timed
source hold, the Mac waits for saved and published routing before releasing
the lock. The bundled helper now persists a handoff marker before confirmation
so an expired hold or app restart cannot start the old source locally. It
restores Local only after an authenticated cancellation and unchanged assignment.
Native review UI, child histories, attachments, unpushed commits, cloud-to-local transfer and
live two-user acceptance remain open.
The full Go race suite and vet pass at schema 23 with disposable PostgreSQL 17.
Both provider fixtures continue in the destination after a lost completion
response while retaining the source files. Native handoff, cloud and worker
regressions pass, including failed routing persistence and late file changes.
Linux command builds and the staged secret scan pass. These checks do not
replace native transfer UI or live deployment acceptance.

The persistent local marker now passes the full native regression suite and
the complete Go race suite and vet. Tests refuse local execution after helper
exit or expiry, allow the same handoff to recheck, reject other handoffs and
malformed markers, and restore a cancelled source across coordinator restart.
The actual bundled Go helper persists the marker before final confirmation.
Linux command builds and the staged secret scan also pass. The native transfer
walkthrough remains unaccepted.

Recovery now discovers local blockers independently of the Camp cache and
matches them through an owner-scoped batch endpoint. Signing in again can
restore a committed assignment or release a confirmed cancelled source without
the original local journal or profile path. Unresolved, foreign and revoked
records stay blocked; recovery grants no execution ownership or prompt replay.
The full native suite passed, followed by a focused handoff rerun after bounding
the in-memory recovery cache. The full Go race suite and vet pass with the
owner/viewer authorization cases, expired holds and reauthenticated devices.
The local-helper tests cover opaque recovery after moving the original profile;
native tests cover cache erasure, foreign records, changed assignments and
32-item batches. Linux commands compile and the staged secret scan is clean.
This is local and synthetic evidence; native transfer review and live two-Mac
acceptance are still required.

A bundle-level check found that the Mac bridge still rejected the new recovery
commands before launching the Go helper. The dispatch list now includes them,
and a native integration test packages a disposable app with the real helper
and verifies the production recovery callbacks end to end. The isolated native
app also passed a fresh Run on, main-chat and subagent navigation walkthrough.
The complete transfer review UI remains unfinished.

The native account form and synthetic provider walkthrough now pass locally.
The Go controller provisions per-installation workers, renews managed grants,
and removes old Pods before deleting their storage and Secrets. It fences stale
epochs and preserves the private journal when restarting a terminal worker.
Separate workspace storage survives routine worker-grant rotation and is removed
when its account epoch is disconnected. The checkout worker now uses that
storage, with real Git and disposable-PostgreSQL tests for dropped replies,
partial-fetch restart and cancellation. Synthetic provider turns now run
through the actual Go adapters and stream into
the native chat projection. Mandatory process isolation has passed Linux
adversarial checks and the installed Codex/Claude version/help smoke test.
The chosen cluster runtime still needs to pass the same boundary checks.
No live cloud model turn has run yet.
The signed-in native setup happy path has now run against disposable PostgreSQL
and the real Go API with synthetic Git/provider adapters. It covered private
copy, project/repository selection, preparation, cloud assignment, a queued
prompt and cancellation, and return to the original local chat with its draft.
This caught and corrected private-cloud selection, card-cache and project-label
handoff bugs. It is not live Git or provider acceptance. The per-account worker
now retains separate ready checkouts and provider histories and schedules
preparation and turns serially across tickets. Live multi-ticket acceptance is
still required.
PostgreSQL race tests now cover competing claims across tickets, queued work
selection, login/preparation exclusion, unknown-outcome recovery and bounded
checkout retirement. Both synthetic provider adapters completed three turns
across two retained tickets with separate provider histories and a lost
completion reply. The full Go race suite passed; the affected Camp, runner,
database and HTTP packages passed again after the final query/cleanup review,
along with vet. Migration checks preserve an existing schema-16 ready checkout
when moving to schema 17. This is local automated evidence, not live cloud or
two-Mac acceptance.

Native cloud controls now use the durable outbox for steering and approval
answers. Computer use verified both providers, response-loss recovery,
rejected-prompt restoration, queues, stop, approvals and completed card states.
It also found and corrected a chat layout stall. The final Go race/vet run
passed with schema 18, and 84 native cloud checks plus execution and state suites
passed. Runtime effort is shown from the open cloud stream; complete reporting
across unopened cards remains in scope. See [cloud controls](cloud-controls.md).

The release tooling now separates development archives from public candidates.
It verifies the expected Developer ID team, stable bundle identity, both
architectures, secure timestamps and hardened runtime; it rejects unexpected
bundle files. Notarization keeps the original upload and resumes by submission
ID. Public artifacts appear only after stapling, Gatekeeper checks and fresh
archive extraction, with committed notes, build metadata and checksums.
Synthetic Apple-tool tests cover rejection, response loss, source/archive
changes, wrong receipts and failed verification without any Apple credentials.
The universal build passed on the Mac; both executable signatures and
architectures were checked, and the release verifier correctly refused that
ad-hoc build. ShellCheck and the staged secret scan passed.
This is tooling evidence only. Actual Developer ID signing, Apple notarization,
downloaded-app acceptance, Git push/tag/release and the website demo remain open.
See the [release procedure](releasing.md).

Schema 19 now freezes each planner's reviewed execution assignment. Changed
assignments are rejected before Start or before the next phase; unavailable
cloud execution preserves completed drafts and stops planning. Old revisions
retain unknown locations and require a reviewed restart to queue another phase.
The full Go race suite and vet passed against an isolated PostgreSQL 17.11
instance. Native planning, state, execution and cloud checks passed. The review
also fixed a runner shutdown race that reported a cancelled Git verification
as damaged state; its deterministic cancellation test passes.

Computer use found sizing loops in shared draft and result views. Workspaces
now receive concrete content bounds, and local/shared planning reuse equal-size
panes. The 1370×860 walkthrough passed changed-location review, five synthetic
cloud turns, completion, restart into version 2, old-version history, combined
plan navigation and local planner drill-down. A quiet CPU observation was 0.1%
after completion; it is not a soak or leak test. Minimum-window and the complete
final design checks remain open. The normal arm64 app and bundled helper build
also passed signature verification. No live provider or cloud deployment was used.

Local tests cover concurrency, limited database permissions, cleanup ordering
and restart behavior. The runner image, database bootstrap and rendered
manifests pass local checks. Live sandbox, storage, network and OAuth behavior
still need validation on the target cluster. See [runner provisioning](runner-provisioning.md) and [provider turns](runner-turns.md).

Automatic discovery finds local profile stores without asking for paths. It
does not prove that every historical account is still signed in, or that six
accounts necessarily use six folders. A custom location or an unreadable Mac
user directory remains visible as a limitation instead of being guessed.

The existing release exclusions remain: no automatic account rotation or quota
bypass, no guaranteed never-expiring login, and no silent execution fallback.
Normal provider sign-in and renewal remain provider-controlled.

## Ordered delivery checklist

Keep this sequence aligned with the full active goal. A checked step needs
recorded implementation and acceptance evidence, not just an updated spec.

1. **In progress — native design audit and corrections**: compare the approved
   mock with synthetic sessions in the native app, fix current layout gaps and
   test the affected interactions. Repeat the complete audit at step 5.
2. **In progress — cloud workspaces and execution**: durable Git workspace jobs
   and the claim/token protocol, checkout worker, assignments, provider turns,
   streaming, approval transport, Linux child isolation and native location
   controls and durable cloud steering are implemented; remaining signed-in setup validation,
   recovery, native plan-import acceptance and complete reviewed session transfers remain.
3. **Pending — shared project capabilities**: Cortex consolidation and memory
   inspection, shared Markdown skills, Git publication and actor attribution,
   owner-routed shared implementation and permissions.
4. **Pending — local acceptance**: independent users, two Macs, reconnect and
   revocation cases, full provider flows, performance/leak/soak validation, then
   two consecutive complete code reviews with no unresolved in-scope defects.
5. **Pending — final UI acceptance**: repeat every screen/state against the
   approved design on the finished build and resolve remaining differences.
6. **Pending — joint cloud acceptance**: provision Hetzner with the owner,
   deploy the public Kubernetes templates, validate isolation, backup restore,
   independent GitHub accounts and execution with the owning Mac offline.
7. **Pending — release**: Developer ID signing and notarization, universal
   artifacts and checksums, thorough README/setup/operator documentation and
   committed v1.0.0 notes. Push the finished code, tag the tested commit, publish
   the GitHub release, and verify its tag, downloadable artifacts and checksums.
   Update the website with the shipped features and a recorded native demo
   with playback controls and reduced-motion support, then smoke-test the live
   site and downloads. Follow the [release procedure](releasing.md); development
   build success alone does not complete this step.

The current UI audit pass has corrected the notification drawer and toast,
verified local onboarding through project creation, added native question
and permission-choice fixtures, and retained recorded response metadata in local
and shared transcripts. It now shares the card layout across provider sessions
and plans, adds direct planner/history navigation, and aligns child-chat
navigation and recent-session icons with the approved design. The [audit ledger](ui-audit.md) distinguishes
these checks from the remaining full design, cloud and release gates.

## Final design acceptance

The active discrepancy list and screen inventory are in [the UI audit](ui-audit.md).
The replacement V1 goal begins with this repeated comparison and retains every
cloud, collaboration, validation and release requirement in the checklist above.

After implementation, compare the running native app with the approved
interactive design at `http://127.0.0.1:8770/capybara-camp-interactive.html`.
Review every screen and its empty, active, waiting, error and completed states,
including the board, chat, planning, account setup, projects, memory, skills
and notifications. Check wording, icons, shared controls, card dimensions,
click areas, keyboard navigation, scrolling and streaming behavior. Record
differences, fix them, and repeat the affected computer-use flows before release.
Passing unit tests alone does not satisfy this gate.

The October 22 click-target pass corrected the stretched chat settings header
and moved dropdown padding inside each clickable label. Model, effort and
speed now share compact, left-aligned fields; the group moves below the title
in narrow windows. Native wide/narrow checks, menu selection and pointer clicks
near the model/effort field edges passed. Session/navigation rows and small
action icons also have explicit hit areas. The remaining full pointer and
keyboard inventory stays in the final UI gate; see UI-25 and UI-26 in
`docs/ui-audit.md`.

The native app has not passed this review. The approved interactive design is
the visual reference for layout, wording, icons and shared controls. Capture
the native and reference views at matching window sizes, track each discrepancy
by screen and state, and keep the gate open until the differences are fixed
and the affected flows pass again. Include the plan-to-implementation transition,
uniform board cards and model controls. The release evidence must distinguish
design parity from functional test results.

The first native audit corrected the reported placement of sub-agents beneath
Recent sessions. Main chat and its children now appear above the recent list,
and switching through two synthetic child transcripts passed. The full final
gate remains open, including running children, other states and window sizes.

The planning audit added provider drill-down, the shared stage bar and both
planning conversations above the combined plan, history and implementation.
Native comparison, executor/model selection, restart with preserved revisions,
single planning and a two-message context attachment passed with synthetic data.
The shorter planning panes wrap their own text and follow the bottom after
resizing. Recorded planning settings appear beside each response. Shared-state
and final keyboard/permission/notification checks remain pending.

The same viewport now handles main and child chats. A separate native interaction
harness verified running/approval board transitions, reported effort/speed,
queue editing, steering and cancellation with synthetic Go responses. It makes
no model calls and does not replace live provider or two-Mac acceptance tests.

The GitHub QA walkthrough also found that Command-Q took effect after closing
the Manage sources sheet, but did not close the app from inside that sheet.
Check quit behavior for every modal during the final keyboard-access audit.
The planning audit traced this to AppKit's modal-window termination setting.
The fix passed Command-Q from Sources, Projects and Notifications, helper
cleanup, and reopening the saved plan. Remaining sheets and in-flight operations
still need the final keyboard audit.

This review belongs at the end of implementation, alongside the two clean
code-review cycles, native acceptance tests and performance checks. Preserve
the owner's existing wording and clean, uniform card layout throughout.
