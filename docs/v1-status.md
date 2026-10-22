# V1 delivery status

October 21, 2026 · [Release epic](https://github.com/teslashibe/capybara-camp/issues/1)

V1 is not released. The public app and website still describe v0.0.1. Local
development builds contain more functionality, but implementation, deployment
and acceptance are separate milestones.

| Area | Implemented locally | Still required for V1 |
| --- | --- | --- |
| Local sessions | Automatic multi-profile import, board/chat, model/effort/speed controls, sub-agents, streaming, dictation, steering and queues | Finish live provider/recovery and performance validation after the Go migration; complete the final design comparison below |
| Planning and projects | Single-model plan, provider comparison, saved versions, per-Camp execution defaults, shared-draft per-planner destination selection, pinned execution assignments, same-ticket local implementation, project assignment across repositories | Standalone local-plan promotion, shared implementation and owner-routed approval completion; cross-device validation |
| Shared Camps | Go/PostgreSQL auth, membership, sessions, events, requests, native cache/outbox and owner-Mac worker | Real independent users on two Macs, remaining permission/recovery cases, production deployment |
| Onboarding | Automatic profile discovery before full import, optional Camp sign-in, owner-only cloud account setup, GitHub connections, projects, execution defaults, notifications, skip/resume | Final onboarding validation after remaining services land |
| Go migration | All native runtime actions use bundled Go; Python source, native fixtures and build/test/CI requirements removed | Final live-provider and release acceptance gates below |
| Cloud execution | Owner-only accounts/installations, native account forms, scoped grants, sign-in recovery, credential encryption, Kubernetes provisioning, explicit Local/Cloud assignments and new-plan defaults, native Run on controls and private transcript copy, multi-ticket per-account scheduling, native steering/queue/stop controls, durable approval/control delivery, a shared request queue, provider subprocess consumption, event streaming and credential revision fencing, mandatory Linux child-process isolation | Standalone local-plan promotion, remaining signed-in recovery states, target-runtime isolation validation, reviewed transfers, complete reported runtime settings and usage states, live sandbox/network/login/control validation and disconnected-Mac execution |
| Git | Project repository links, local CLI project execution, owner-bound GitHub App authorization, native repository browsing, encrypted tokens, signed revocation, exact-commit and scoped read-token adapters, workspace records, exclusive claims and isolated runner checkouts with restart/cancellation recovery | Execution grants, Git actor attribution in execution, branch/push/PR recovery and live two-user GitHub validation |
| Memory and skills | Approved Cortex/shared Markdown design | Actual native and Go integrations, provider/model consolidation, memory inspection, skill editing/version rollout and permission tests |
| Hosting and release | Public Kubernetes/K3s base, optional runner overlay, pinned provider image, local development builds and resumable notarization/packaging tooling | Cortex deployment templates, Hetzner provisioning with the owner, backup restore, two clean reviews, soak tests, actual Developer ID/notarization acceptance, universal V1 release and recorded website demo |

Next: finish native transfer review and local-plan promotion, then memory/skills
and shared execution. Continue the native design audit as those surfaces are
implemented. Validate the full workflow locally before the owner provisions
the Hetzner box with us. Cloud sign-in and
the two-Mac acceptance run follow that deployment. Hardware has not been bought
or provisioned for this goal.

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
   recovery, standalone local-plan promotion and reviewed session transfers remain.
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
