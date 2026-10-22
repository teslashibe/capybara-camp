# V1 delivery status

October 21, 2026 · [Release epic](https://github.com/teslashibe/capybara-camp/issues/1)

V1 is not released. The public app and website still describe v0.0.1. Local
development builds contain more functionality, but implementation, deployment
and acceptance are separate milestones.

| Area | Implemented locally | Still required for V1 |
| --- | --- | --- |
| Local sessions | Automatic multi-profile import, board/chat, model/effort/speed controls, sub-agents, streaming, dictation, steering and queues | Finish live provider/recovery and performance validation after the Go migration; complete the final design comparison below |
| Planning and projects | Single-model plan, provider comparison, saved versions, same-ticket local implementation, project assignment across repositories | Shared implementation and owner-routed approval completion; cross-device validation |
| Shared Camps | Go/PostgreSQL auth, membership, sessions, events, requests, native cache/outbox and owner-Mac worker | Real independent users on two Macs, remaining permission/recovery cases, production deployment |
| Onboarding | Automatic profile discovery before full import, optional Camp sign-in, owner-only cloud account setup, GitHub connections, projects, notifications, skip/resume | Default execution target; final onboarding validation after remaining services land |
| Go migration | All native runtime actions use bundled Go; Python source, native fixtures and build/test/CI requirements removed | Final live-provider and release acceptance gates below |
| Cloud execution | Owner-only accounts/installations, native account forms, scoped grants, sign-in recovery, credential encryption, Kubernetes provisioning, explicit Local/Cloud assignments, native Run on controls and private transcript copy, multi-ticket per-account scheduling, native steering/queue/stop controls, durable approval/control delivery, a shared request queue, provider subprocess consumption, event streaming and credential revision fencing, mandatory Linux child-process isolation | Execution defaults, remaining signed-in recovery states, target-runtime isolation validation, reviewed transfers, complete reported runtime settings and usage states, live sandbox/network/login/control validation and disconnected-Mac execution |
| Git | Project repository links, local CLI project execution, owner-bound GitHub App authorization, native repository browsing, encrypted tokens, signed revocation, exact-commit and scoped read-token adapters, workspace records, exclusive claims and isolated runner checkouts with restart/cancellation recovery | Execution grants, Git actor attribution in execution, branch/push/PR recovery and live two-user GitHub validation |
| Memory and skills | Approved Cortex/shared Markdown design | Actual native and Go integrations, provider/model consolidation, memory inspection, skill editing/version rollout and permission tests |
| Hosting and release | Public Kubernetes/K3s base, optional runner overlay, pinned provider image and local development builds | Cortex deployment templates, Hetzner provisioning with the owner, backup restore, two clean reviews, soak tests, Developer ID/notarization, universal V1 release and recorded website demo |

Next: finish execution defaults and reviewed transfers, then memory/skills
and shared execution. Continue the native design audit as those surfaces are
implemented. Validate the full workflow locally before the owner provisions
the Hetzner box with us. Cloud sign-in and
the two-Mac acceptance run follow that deployment. Hardware has not been bought
or provisioned for this goal.

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
   recovery, Local/Cloud defaults and reviewed session transfers remain.
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
