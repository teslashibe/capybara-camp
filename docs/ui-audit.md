# Native UI design audit

Reference: the approved [interactive design](http://127.0.0.1:8770/capybara-camp-interactive.html).
The native app has **not passed the full design audit**. This is the working
discrepancy list, not a release approval.

## Shared implementation history

The native shared-plan History and Implementation sections now discover saved
runs and page through their requests. They reuse the provider marks, model
settings, transcript viewport and stream renderer. Only the selected request
has an active stream projection; history pages replace one another rather than
accumulating transcripts.

Computer use reported a locked Mac and disabled browser control during this
pass. Native discovery, paging, stream and revocation regressions pass. The new
view still needs matching-size comparison with the approved design,
pointer-edge and keyboard checks, direct opening of the only run,
version switching, empty/error/revoked states and stream-following checks. Its
executor setup, owner approval and follow-up controls are not complete. Do not
treat the compiled view or API tests as visual acceptance.
The signed development candidate is `build/Capybara Camp Shared History.app`,
with the isolated bundle ID `com.capybaracamp.shared-history-review`.

## Saved planning history

The October 22 native walkthrough signed in to the actual Go API backed by a
disposable local PostgreSQL database. Save to Camp imported a synthetic private
ticket into Inbox: three planning revisions, one quoted context source and one
implementation with six messages and two attempts. API readback confirmed those
counts and `shared: false`. No personal sessions or provider credentials were
uploaded, and the import did not invoke a provider.

The walkthrough found and corrected an extra navigation step to reach the only
implementation, a separate system-styled Chat/Attempts control, raw effort/speed
labels in attempts, a missing completed Implementation stage and a Cancel action
on read-only imported history. Changing versions from Implementation also left
an empty pane; the rebuilt app now opens the selected revision's provider plans.
The import sheet uses the shared close-button target and singular count labels.

Computer use verified direct implementation opening, both attempt records and
their saved permissions, bottom-visible chat text, revision switching, preserved
unknown outcomes and the two-message source snapshot. Escape closed the snapshot.
The final build exposes no execution action on imported revision 1. Captures:
`build/ui-audit/native-imported-implementation-oct22.png`,
`native-imported-attempts-oct22.png` and `native-imported-revision-oct22.png`.
The native promotion, imported-planning and implementation suites pass; the
final navigation rebuild reran the latter two. The bundle passes strict deep
signature verification with the existing development identity.

Pointer input reported no available windows at the Attempts edge test, while
accessibility selection and screenshots worked. Edge clicks and the remaining
keyboard inventory therefore remain open. This is a local synthetic-data import
walkthrough, not two-Mac acceptance, live cloud execution or the final design
audit. Updating an already saved copy after newer local work also remains to be
resolved: the current link preserves that local work but offers only the earlier
saved copy.

The next pass found that provider child tiles still said Ready after an
unconfirmed draft, or Draft ready after a failed review. Local and shared
planning now use the same outcome presentation, with the status exposed as an
accessibility value. A separate private fixture verified Needs review, Failed,
Paused, Cancelled and Reviewed in the native workspace. The saved captures are
`build/ui-audit/native-planner-needs-review-oct22.png` and
`native-planner-failed-oct22.png`. The existing reference planning capture was
used to check the provider marks, child tiles and placement above Recent sessions.
Local planning and imported-planning tests pass, including an unconfirmed draft
followed by cancellation and a successful draft followed by a failed review.

The same pass reproduced a keyboard gap: Tab cycled from Notifications through
Board/Chat and Search back to Notifications, skipping the planning navigation.
Making the buttons focusable exposed a second issue: Space did not activate
them. Navigation and stage buttons now share a plain button component with
explicit Space/Return handling and an enabled-state check. The final native
build passed Tab to Codex, Space to open its pane, Shift-Tab to Both chats,
Return to restore both panes, Space on Reviews and Return to open Claude. Tab
skipped the disabled Implementation stage. The focus ring is visible in
`build/ui-audit/native-planning-keyboard-oct22.png`. The final native regression
and strict deep signature verification pass. Other controls' keyboard behavior
still needs the full inventory audit. Pointer activation at a recent-session
row edge continued to report no available windows, including after successful
keyboard input; that hit area has not passed this check.

## Chat keyboard navigation

The October 22 follow-up reproduced Tab skipping Main chat, the subagent
disclosure, child rows and Recent sessions. These controls now reuse the
planning button's focus and Space/Return behavior. The bordered Main chat
footer uses the same keyboard modifier without changing its appearance.

Computer use on the rebuilt synthetic audit app verified Tab to Main chat,
Space to collapse Subagents, Return to expand it, Space/Return to open a child,
and both the sidebar and footer returns to the main transcript. A Recent-session
row opened the expected planning ticket. Shift-Tab from the first child to
Subagents and then Main chat passed on the repeated check; an earlier attempt
had left focus on the child, so repeat this transition during the final audit.
The native focus order can interleave the footer with visible Recent rows.
The capture is `build/ui-audit/native-chat-keyboard-oct22.png`.

`CampStateTests`, `CampChildHistoryTests` and `ImportedPlanningTests` pass in
`build/chat-keyboard-tests.log`, along with the bundled Go helper race suites.
The rebuilt audit bundle passes strict deep signature verification. Model,
effort, speed and filter menu keyboard access still needs work: those menus
were skipped in this Tab sequence. The far-right child-row pointer attempt
again returned no available windows. This check does not close those findings
or the full interaction audit.

The next rebuild fixes the Model/Effort/Speed and Recent filter keyboard gap.
Those fields retain `CampDropdownLabel` and use one native menu trigger for
pointer, accessibility and keyboard activation. Compact card menus and expanded
fields consume the same option/action lists. The bridge holds its native button
weakly; menu actions retain only the selection snapshot while the menu is open.

Native computer use verified Tab/Shift-Tab to the fields, Space opening Model,
Escape cancellation, Return opening Effort, type selection plus Return saving
High, and Down opening Speed. Sonnet still exposes only Standard speed. The
Recent filter opened with Space and selected Codex through keyboard input,
showing six Codex sessions while preserving the open Claude chat. Pointer clicks
at Model's lower-left padding and Effort/Speed's lower-right padding opened the
correct menus. The following child-row edge check failed at screenshot capture;
that does not invalidate the three completed menu clicks or prove that child
target passed. Captures: `native-menu-keyboard-oct22.png` and
`native-dropdowns-filter-oct22.png` under `build/ui-audit/`.

`build/native-menu-final-tests.log` records the passing state and imported-plan
suites and Go helper race checks. Strict deep bundle verification passes.
VoiceOver speech and the remaining whole-app pointer/keyboard inventory are
still release gates. The automation exposes each native popup's label and value,
but does not report the focused element while its SwiftUI focus wrapper owns
keyboard focus; the visible focus ring and menu activation were checked directly.

## Transfer review

`tools/transfer-ui-audit.sh` builds a separate native window using the production
transfer review component and Go capture helper. Its Git files and cached state
are synthetic and private under `build/.transfer-ui.*`; its server transport is
a fixture. It never signs in to a provider or uploads files to a server.

The component adds file selection, captured-file inspection, excluded paths,
pinned base, preparation, source verification and final permission review to
the approved Move to cloud layout. Automated tests cover those state transitions
and the bundled helper. The October 22 computer-use walkthrough passed file
selection, excluded `.env` inspection, README content preview, history-mode
selection, preparation, source verification, explicit permission review and
the final Runs in Cloud state. Back and re-entry passed, as did cancelling a
prepared destination with Transfer cancelled · The source was kept. Captures
are `build/ui-audit/native-transfer-completed-oct22.png` and
`native-transfer-cancelled-oct22.png`. This used a synthetic
server response; no files were uploaded and no cloud CLI ran.

A subsequent audit uses the real `CampLocalHandoff` coordinator and bundled Go
source guard with a synthetic main conversation and two child transcripts. The
mock implements only server responses; it no longer acknowledges the source
automatically. Computer use verified rejection when a reviewed project file
changed before verification, and when a child transcript changed after Source
verified. Both cases removed the move action and allowed cancellation while
keeping the source. Unchanged files/history completed the explicit Manual
permission review and reached Runs in Cloud through the real local guard.
Captures: `native-transfer-changed-file-oct22.png`,
`native-transfer-changed-child-oct22.png` and
`native-transfer-real-guard-completed-oct22.png`.

This uncovered a stale confirmation state: the review stopped polling while
the user chose permissions. It now follows the hold until commit, clearing
Source verified when the hold changes or ends. Regression tests cover
invalidated, expired and released holds, refusal to confirm and cancellation.
The transfer and bundled-helper suites pass; the audit app passes strict
signature verification. The full app's signed-in transfer, complete history
publication and two-Mac acceptance remain open. These fixtures do not prove
cloud execution or real transcript upload.

## Cloud child history

The implementation reuses the existing subagent tree and `ChildTranscriptView`.
It keeps children above Recent sessions, uses the parent's provider icon, shows
an incomplete upload as Syncing, and provides the main chat's Load earlier
messages control. Automated native tests cover stable child selection on a
refresh, late responses after changing parents or opening a plan, and removal
after a parent is revoked.

The rendered shared-child audit remains open. Verify the following with two
independent users:

- Publish a parent with multiple children, including a transcript longer than
  one page, and open each child from the shared sidebar
- Confirm that the child header, recorded model and transcript stay separate
  from the main chat at wide and minimum window sizes
- Receive a child update while it is selected and confirm that selection and
  bottom-following are preserved
- Load older messages, return to Main chat, then switch to another parent
- Revoke the parent while the other Mac is reading a child and confirm that
  its content and sidebar entries disappear

These checks are still required on the final signed release candidate.

## Review method

Use a separately identified native development app and synthetic provider
profiles. Leave the user's running sessions untouched. For each surface,
capture the reference and native view at matching window sizes, compare visual
details, click through its interactions, fix discrepancies, rebuild and repeat.
Check empty, loading, active, waiting, error and completed states. Record what
was actually verified; do not infer full parity from one screen or unit tests.

An October 22 audit launch exposed a test isolation error: replacing the
development helper with the ordinary helper enabled normal profile discovery.
That run was stopped and its cache is excluded from demo evidence. UI audit
mode now refuses an ordinary helper or a missing fixture root. The native
harness checks the bundled helper before starting the store, development
discovery excludes saved paths and symlinks outside the fixture root, and the
build script creates a fresh support folder for each run. No recording from
the affected cache may be published.

The rebuilt helper passed the startup handshake and returned only the two
synthetic profiles. An ordinary helper refused UI audit mode. The full local
Go race suites and the two native conversation/approval suites passed. The
native walkthrough then verified twelve fixture sessions through automatic
refreshes, separate parent/child chats, ten Recent sessions, provider settings,
a pinned older-session context, retained planning revisions and a completed
five-turn comparison. Planning fixtures now emit cumulative text snapshots and
handle planning separately from the chat approval triggers. These checks do
not exercise real model inference or shared-Camp publication.

Compare glyph geometry and color, font size/weight, spacing, borders, backgrounds,
button/menu treatment, card dimensions, copy, focus, keyboard actions, pointer
targets, resizing and scrolling. The final pass repeats the entire inventory on
the release candidate after remaining capabilities are implemented.

## Findings and verification

The October 22 pointer retry opened Speed from its lower-left padding, opened
Accessibility review from the far right of its row, and returned to Main chat
from that row's lower-right padding. Each action showed the expected menu or
transcript. Recent sessions also switched to Codex and back to All types through
accessibility, preserving the open Claude chat and restoring the ten-session
list. Capture: `build/ui-audit/native-chat-click-retry-oct22.png`.
Pointer access then again reported no available windows, including after raising
the app. The filter's edge target and the remaining card/icon targets therefore
remain unverified by pointer; this retry does not close the whole-app audit.

| ID | Finding | Current evidence and next check |
| --- | --- | --- |
| UI-01 | Main chat and subagents were below Recent sessions | Corrected and clicked through in the native review build: main chat, Accessibility review, Layout review, back to main. Each shows its own transcript. Repeat with running children, empty children and session changes |
| UI-02 | Chat used a compact combined model menu where the design has labeled Model, Effort and Speed fields | Shared component now has compact and expanded presentations. All Claude aliases/versions were visible and a version change worked. Replaced custom chevrons with native menu indicators. The final capture verifies proportional fields that fill the implementation row; narrow planner columns use two rows. Remaining account/source controls and shared execution still need comparison |
| UI-03 | Native Claude glyph, Codex glyph and planning symbol differed from the reference | Updated cached native images from the approved vector paths and colors. Pass 3 shows the matching robot, terminal and bulb; intrinsic image sizes now remain small inside AppKit menus |
| UI-04 | Board status was below the subagents footer; design places it above that footer | Reordered within the existing uniform card. Pass 2 exposed bottom clipping. Later captures verify ordinary, completed-comparison, running and approval cards contain their controls. The interaction fixture moved a Codex card into In progress, then Needs you for approval. Real provider and queued-card edge cases remain |
| UI-05 | Native recent-session filter used an icon plus a detached value | Replaced with a labeled filter and visible count. Verified Codex filtering preserves the open Claude chat. Ten-item cap was visible before filtering; verify keyboard and small-window treatment |
| UI-06 | Board/Chat selection used a bright system-blue treatment | Changed to the reference's neutral segmented treatment. First native capture verified; recheck focus and keyboard semantics |
| UI-07 | Planning navigation and stage presentation differ from the reference | Added the shared planning navigation, provider child tiles and stage bar. Native passes 4–6 verified both chats, provider drill-down, combined result, history and implementation on the same ticket. Combined result, history and implementation retain both conversations above their content. Restart retained version 1 and created editable version 2; switching that revision to single planning removed the second provider. A two-message session snapshot was attached through the picker. Shared-result and running states still need validation |
| UI-08 | Memory, skills and cloud execution controls are in the design but not fully implemented natively | Open. Integrate the real services, then audit these screens; a placeholder does not pass |
| UI-09 | Quit did not take effect while Manage sources was open in earlier QA | Corrected using AppKit's per-window termination setting and the existing save/delegate path. Audit 9 verified Command-Q from Sources, Projects and Notifications, actual process exit, helper exit, and reopening the saved five-step plan. Remaining sheets and interrupted/in-flight operations still belong to the final keyboard audit |
| UI-10 | Message metadata, composer/context placement and notification styling need detailed comparison | Planner metadata and long-transcript append passed earlier. The native interaction fixture verified reported High/Ultra Fast for Codex, High/Fast for Claude, queue/edit/requeue, steering, command approval and completion notifications. Recorded main-chat metadata and child metadata now pass the local fixture checks. Dictation, the remaining provider variants and final navigation checks remain |
| UI-11 | Projects and new account onboarding need visual integration with the approved style | Native click-through verified automatic discovery of two synthetic profiles, optional Camp validation, Accounts/Git steps, pause and resume at step four, empty project-name validation, project creation, and the final summary. The new project appeared in the sidebar. Signed-in, loading and recovery states remain open |
| UI-12 | Remaining native segmented controls still use the system-blue treatment | Projects, Sources, notifications and Board/Chat now share one neutral segmented component. Native pass 6 verified distinct Local profiles, Cloud accounts and GitHub accessible names and successful source-tab switching. The later source-manager pass verified Right-arrow navigation from Local profiles through Cloud accounts to GitHub, and quitting from that sheet. Other sheets and the full keyboard inventory remain open |
| UI-13 | Implementation's shorter planning panes allowed text to spill into the adjacent provider | Pass 5 verifies transcript wrapping inside both columns after constraining each scroll viewport's content width. Bottom-following is preserved; repeat at the minimum window size |
| UI-14 | Completed planning stages were all reported as selected to accessibility | Native passes 6 and 9 verify button roles, correct selection/completion values and the unavailable description. The unfinished Implementation control is disabled and refuses activation |
| UI-15 | Resizing a planning pane can leave the latest lines below its scroll position | Pass 7 verifies that both planner transcripts follow the bottom after narrowing the window. Model controls and response metadata wrap without overlapping, and the final line stays visible. The size-bound task cancels when the viewport disappears or resizes again |
| UI-16 | Main chat watched only height changes; child chat did not follow a resize | Both now use the same width-constrained transcript viewport as planning. The live fixture's last marker stayed visible after resizing. Parent/child switching stayed isolated while the parent ran |
| UI-17 | Toast placement and main-chat response metadata differ from the reference | The foreground toast now uses the compact dark bottom-center layout. Notification history uses the right-side drawer, unread rows, separate settings and count badge. The second native pass verified Escape returns focus to the bell, Tab and arrow-key filtering, result navigation and mark-all-read. Native history and live replies now preserve the recorded model, effort and speed. The synthetic Claude capture shows distinct settings for two replies and provider-only text for an unknown reply. Changing the next model did not relabel past messages; child transcripts also showed their own model. Missing provider metadata remains unset |
| UI-18 | Provider questions were centered and the scroll area collapsed to a partial option list | Left alignment and question headers are corrected. The review caught that an ideal height still shrank under the main transcript, so the question viewport now reserves 180 points and scrolls longer content. Verified the final form at 1370×860 and after native zoom to 1728×1005 and back: both questions and all choices remained visible, and submitting a selected option plus text resolved the request. The 1060×720 minimum and larger question sets remain in the final audit |
| UI-19 | An acknowledged Stop showed the unconfirmed-exit warning | Confirmed cancellation now says Stopped. The native fixture verified the banner and Active now returning to zero; the conversation regression still distinguishes uncertain exits |
| UI-20 | Card types used different internal spacing and planning put its status in the footer | One shared card now fixes the heading, summary, models, status and actions into the reference's slots. Planning has compact provider/model rows and separate History/Open plan actions. The first native check passed Claude planner drill-down and equal-height single/comparison/session cards. Final signed-build checks are recorded below |
| UI-21 | Child chats lacked the reference's title, status row and return action | The selected child now updates the main heading, shows its own read-only transcript and returns through Main chat. The tree has a connecting line, provider icons and separate status text. Recent sessions use tinted provider icons. Native child navigation and return passed at wide and narrow sizes |
| UI-22 | Opening Combined plan or Implementation left the result and next action below the viewport | The local detail sections now reuse the transcript viewport and follow their bottom when opened or resized. Native click-through verified the combined result with Continue to implementation visible, then the executor's separate model, effort, speed and approval action. Minimum-width window review also retained the bottom action. Captures: `native-combined-follow-oct22.png` and `native-implementation-follow-oct22.png`. Final release checks remain |
| UI-23 | Choosing a Codex sandbox in the executor form could revert to Inherit | The mutually exclusive profile/sandbox fields now publish one complete settings value. Rebuilt native click-through passed sandbox → profile → sandbox while preserving approvals/reviewer. The same-ticket implementation then ran with the chosen model and Read only · Ask when needed · Manual permissions, accepted steering, drained its queued follow-up and retained both attempts in history. Four focused native suites and fixture protocol race tests pass. Capture: `native-permission-choice-fixed-oct22.png` |
| UI-24 | Notification actions/rows were skipped by Tab, and duplicate row descriptions were ambiguous to accessibility automation | Rows now have stable identifiers; Tab reaches actions and rows. Native keyboard checks passed Space on the Unread filter and a saved-plan row, Return on a completion row and settings, mark-all-read, closing with Return, and Escape back to the bell. Clicking duplicate completion notices also opens the correct result. The shared segmented control supports Space/Return. Notification regression tests and strict bundle verification pass. Capture: `native-notifications-keyboard-oct22.png` |
| UI-25 | Chat model fields stretched across the header, centered their values and put visible padding outside the menu label | Model, effort and speed share left-aligned outlined fields with a trailing chevron and a 30-point clickable label. Chat caps the group at 396 points and moves it below the heading at narrow widths. The native walkthrough verified wide and minimum-width layouts, model/effort/speed menus, a persisted effort change and Claude's restricted speed choices. Pointer clicks in the model field's lower-left padding and the effort field's lower-right padding opened the correct menus. Final accessibility inspection exposes one menu per field, including the Recent sessions filter. Native state, permission and planning suites pass. Capture: `native-chat-controls-narrow-oct22.png` |
| UI-26 | Plain buttons left empty row space and small icon padding outside their hit areas | Explicit label hit areas cover recent sessions, main/child chats, provider/profile/project rows, planning navigation, segmented controls, context attachment and the 28-point action icons. The card model/logo layout is preserved. The full pointer/keyboard inventory remains a release gate; intermittent computer-use window errors prevented completing every edge-click case in this pass |
| UI-27 | Transfer review kept Source verified after the source hold ended while permissions were open | The confirmation phase keeps following the source hold. Native checks using the real local guard rejected a changed project file and a changed child transcript; unchanged history completed. Invalidated/expired/released hold regressions pass. Real cloud and two-Mac transfer acceptance remain open |
| UI-28 | Imported implementations required an extra history detour, used inconsistent controls and could leave an empty pane after revision changes | The native API walkthrough verified direct opening, shared Chat/Attempts controls, formatted saved settings, complete-stage indication and revision navigation. Read-only imports hide execution actions. Pointer edges and the final whole-app audit remain open |
| UI-29 | Provider tiles labelled unconfirmed or failed planning work as Ready or Draft ready | One presentation function now serves local and shared planning. Native synthetic-state checks verify Needs review, Failed, Paused, Cancelled and Reviewed; imported/local regressions pass. Status is also exposed to accessibility |
| UI-30 | Tab skipped planning navigation and stages; focus alone did not make Space activate them | The shared plain button now supports focus and Space/Return activation. Native checks passed provider drill-down, Both chats, Reviews, reverse focus and skipping disabled Implementation. The remaining whole-app keyboard inventory is open |
| UI-31 | Tab skipped the main/child chat navigation and Recent sessions | Shared keyboard handling now reaches those rows and the child footer. Native checks passed child selection, collapse/expand, both returns and recent-plan navigation. Menu keyboard access and the full pointer audit remain open |
| UI-32 | Expanded model/effort/speed and Recent filter menus could be focused but did not open from the keyboard | Shared native menu activation now supports Space/Return/Down, native selection and Escape. Native checks passed keyboard selection and three padded-edge pointer clicks. Card menus reuse the same options and retain their compact labels. Full VoiceOver and whole-app audits remain open |

Local captures are in `build/ui-audit/`: `reference-chat.png`,
`reference-board.png`, `reference-planning.png`, `native-chat-before.png`, and
`native-chat-after.png`, `native-chat-pass3.png`, `native-board-pass3.png`,
`native-planning-pass4.png`, `native-implementation-pass5.png`, and
`native-sources-pass4.png`, `native-combined-plan-pass6.png`,
`native-single-plan-context.png`, `native-resize-follow-pass7.png`,
`native-planning-verified.png`, `native-implementation-verified.png`,
`native-implementation-final.png`, `native-implementation-narrow-final.png`,
`active-chat-fixture.png`, `running-card-fixture.png`, `approval-chat-fixture.png`,
`approval-card-fixture.png`, `child-transcript-resize.png`, and
`live-chat-resize.png`.
Captures are development evidence, not release demo
assets. Native captures use twelve synthetic sessions and two child transcripts;
no real provider credential or chat is required.

The signed development build passed after these changes. Focused native suites
passed for state/transcript notifications, local conversations and queues,
single/comparison planning, same-ticket implementation, and onboarding. Those
tests do not prove complete visual parity or the unimplemented cloud surfaces.
The latest focused run also passed shared planning, project persistence and
notification recovery. Audit 9 checked switching from a focused Claude planner
to a single Codex plan without losing the visible pane.

The final development build for this pass is
`build/Capybara Camp UI Review Current.app`, with a separate bundle identity and
synthetic profile/support directories. The final field-width adjustment passed
compilation, signing verification and native wide/narrow screen checks. No
release or production app replacement was performed.

## Shared planning assignment review

The October 21 shared-planning pass found two layout stalls: opening a draft
stalled during intrinsic window sizing, and the results pane repeatedly
measured its flexible children. CPU sampling located both in SwiftUI layout.
The root workspace now supplies concrete bounds and one `PlanningPaneRow`
gives local and shared planners equal, explicit dimensions.

Computer use on the isolated signed review app at 1370×860 verified:

- A saved draft reopens and labels both assigned cloud sources
- An assignment change disables Start without changing the draft's selection
- Review current locations adopts the new assignment without starting work
- Five synthetic cloud planning turns complete and show both chats and the
  combined plan, with saved locations beside each planner
- Restart retains the ticket, creates version 2 and keeps version 1 accessible
- Local comparison, provider drill-down and return to both chats still work

The completed view settled to a 0.1% CPU observation. This does not establish
memory-leak or sustained-performance acceptance. The reference's per-planner
Run on selectors are still part of the unfinished per-planner
destination-selection flow; the current labels report the source's assignment.
Minimum-window coverage remains open. A computer-use resize attempt did not
change the window, so it provides no narrow-layout evidence.

Captures: `build/ui-audit/planning-location-review.png`,
`planning-locations-completed.png`, and `planning-locations-completed-v2.png`.
The before-fix CPU samples are `build/planning-locations-ui-sample.txt` and
`build/planning-locations-result-sample.txt`. These use only synthetic accounts,
sessions and responses. No provider model or real Git repository was used.

## Execution defaults

The native Defaults tab reuses the source-manager layout, provider icons and
Local/Cloud segmented control. Onboarding uses the same form. A computer-use
pass on the isolated review app verified saving Claude for local planning,
opening a single Claude draft, and refusing to save Cloud defaults before
connecting a Camp. With the local Go fixture connected, it saved a project and
both provider accounts, opened a private comparison with two Cloud assignments,
and completed its five synthetic turns. The completed plan reopened from the
board with both transcripts intact.

Selecting a project without a prepared workspace returned to Defaults with an
explanation and created no substitute local plan. That explanation now appears
above the form so it does not require scrolling. Full onboarding, minimum-size
layout, real account expiry and two-Mac acceptance remain separate checks.
After restarting the review app without a cached login, a new plan retained
its Cloud preference, displayed the sign-in explanation at the top of the
sheet and did not create a local draft. Command-Q from that sheet exited.

The full Go race suite and vet passed with PostgreSQL 17. Native defaults,
onboarding, local/shared planning and execution recovery suites passed. The
defaults tests cover scoped persistence, unavailable profile refusal, keeping
old plans unchanged and signed-out Cloud intent. Screenshots are in
`build/ui-audit/execution-defaults-local.png`, `execution-defaults-cloud.png`,
`execution-defaults-comparison.png` and `execution-defaults-unavailable.png`. These are development evidence, not
the website's release recording.

## Per-planner destination review

The planner destination pass added Run on below Model, Effort and Speed in
shared drafts. All four fields reuse one native styling component. Computer
use verified changing only Codex to a prepared Cloud source, retaining its
model, then choosing a saved session on this Mac while Claude stayed in Cloud.
The mixed plan completed all five synthetic turns. The workspace setup sheet
opened from that chooser and closed normally. Cancelling a restart preserved
the completed version and both transcripts.

Captures: `build/ui-audit/reference-planning-run-on.png`,
`planner-run-on-mixed.png` and `planner-run-on-mixed-complete.png`. The test
uses disposable Go/PostgreSQL state and synthetic CLI output. It does not
establish live provider, minimum-window or full design acceptance. Standalone
local-plan promotion remains open.

## Full inventory still to pass

- Board: each provider, single/comparison plans, implementation cards, all stages,
  long titles, model menus, context/history actions and consistent dimensions
- Chat: parent and children, ten recent sessions, filtering and @ discovery,
  model/settings, history pagination, live scrolling, dictation, steering,
  queues, permissions and approval/error states
- Planning: new single/comparison plan, both chats, participant drill-down,
  settings, context, cancellation, restart, budget, history, combined result and
  same-ticket implementation
- Sources/onboarding: automatic discovery, local profiles, cloud accounts,
  GitHub, projects, empty/loading/error/connected states, skip/resume and sign-out
- Multiplayer/execution: shared sessions, invitations, permissions, offline
  requests, Local/Cloud defaults/transfers, Git selection and publication
- Shared skills: personal/project scope, editing, revisions, sync states and conflicts
  Memory and consolidation are deferred from V1 by the owner
- Notifications: foreground toast, history/unread state, settings, native desktop
  notification and navigation to its result

Release acceptance also requires a final keyboard/accessibility, resize,
performance and memory-growth pass. Keep this audit open until all inventory
items have direct evidence and remaining defects are resolved.

AppKit's [modal-window termination setting](https://developer.apple.com/documentation/appkit/nswindow/preventsapplicationterminationwhenmodal)
documents the default refusal before consulting the app delegate. The native
window modifier applies the setting only to Camp's views and sheets, and enables
automatic keyboard-view ordering. Persistence still uses the existing deferred
termination path. Development-only opt-in lifecycle logs contain stage names,
not session data.

## Synthetic interaction checks

`tools/ui-audit.sh` builds the real native views and state machine against the
existing test-support module, with a synthetic Go chat transport. It first runs
the conversation and execution-approval suites. Close an older copy of the
interaction app before rebuilding it. `build.sh` never includes this harness or
the test provider in the released app.

Build with an absolute `CAMP_DEV_PROFILE_ROOT` containing synthetic profiles
and a `.capybara-ui-fixtures` marker. The script creates a fresh private support
folder and saves its launch settings in the audit bundle. Open
`build/Capybara Camp Interaction Audit.app` through computer use. The native
harness checks both markers and the helper's fixture handshake before loading
the store. Use only synthetic profiles and state. The recorded run used artificial read-only
permission grants in its separate support directory to exercise approval UI.

The test provider holds a response open so the UI can be inspected. A prompt
containing `approval` shows a harmless command approval. Steering text containing
`finish` completes the active turn, and a queued prompt containing `complete`
finishes immediately. Other prompts can be stopped through the normal UI.
It does not execute commands or contact a model provider, and it does not write
provider transcript files. These checks prove UI transitions and transport
handling, not real provider continuation or final provider-history persistence.

The recorded run verified an empty durable queue after the steered turn and its
follow-up completed, two completion notices, the approval-to-completion
transition, Claude Fast, and no remaining fixture-provider process after Stop.
The regular signed development build at `build/Capybara Camp Chat Review.app`
also opened the 65-message fixture at its latest marker. Its read-only capture
is `build/ui-audit/regular-chat-resize-build.png`. It contains the viewport fix
without the synthetic conversation transport.

## Notification and setup review

The notification pass compared the approved drawer and settings with the native
views, then rebuilt after finding an Escape/focus regression. Notifications now
use the existing neutral segmented control, a 32-point bell and count badge,
95-point rows with unread treatment, and separate settings. Opening the drawer
dismisses its toast; an incoming toast stays hidden while history is open.
Clicking a result marks it read and opens its session. Native desktop delivery
still uses the existing opt-in macOS implementation. This pass did not validate
real desktop delivery, background system settings, or Focus mode.

Notification provider lookup scans the session list once per render, only for
the displayed notification IDs. It does not search the entire library again for
every row. This is an implementation bound, not a completed performance or
memory-leak acceptance test.

Captures: `reference-notifications.png`, `notification-drawer-final.png`,
`notification-settings.png`, `onboarding-profiles.png`, and
`onboarding-ready.png` in `build/ui-audit/`. The reference and native captures
have different window dimensions; this pass verifies the recorded details and
interactions, not a complete same-size pixel comparison.

The synthetic transport now includes multiple questions and provider-offered
permission choices. It exposes both Allow once and Allow for this session; the
recorded test selected Allow once, resolved the request and finished the turn.
No project command was run. Question submission was tested with one selected
option and a free-text answer. The final viewport passed the recorded 1370×860 and 1728×1005 resize checks.
The native permission sheet also showed its unavailable-catalog error, disabled
Apply, and closed with Escape. Its synthetic profile cannot establish a real
Codex CLI handshake, so this is an error-state check, not provider authentication.

Additional captures: `question-approval-wide.png`,
`question-approval-resized.png`, and `permissions-unavailable.png`.
The latest normal development build is `build/Capybara Camp Notification Review.app`;
it uses its own bundle identity, synthetic state and no registered source-link
scheme. The real running app was not replaced. The development signature is
Apple Development, not Developer ID or notarization. No release was published.

The normal signed app also opened the saved notification history, with unread
rows and the new drawer visible in `notification-normal-final.png`. The focused
notification, onboarding and permission suites passed, as did the final
conversation and execution-approval suites. Full UI acceptance is still open.

## Message metadata review

The Go transcript reader supplies per-message model, effort and speed when the
provider records them. Codex rollout messages use the current turn context; a
future settings event does not relabel the current answer. SQLite messages use
only fields present on that item. Claude reads each message independently.
Missing and invalid values stay empty rather than inheriting profile defaults.
The export fingerprint changes once so previously indexed transcripts can be
refreshed with the newly retained fields.

The native message type preserves those optional values through local history,
child transcripts, saved implementation history and shared transcript
conversion. Live responses retain their settings at message boundaries. The
existing message component renders the header. The selected next model remains
independent from older replies.

Context attachments still encode exactly `id`, `role` and `text` per message.
Their digest covers those same fields. Display metadata remains available for
inspection without changing the approved context bytes or accepting new fields
in the Go context tool. Tests cover legacy message decoding, strict context
shape, unknown settings, multiple response boundaries and shared export fields.

The computer-use pass showed Opus/High/Fast, Sonnet/Low/Standard and an unknown
reply together; switched the next model; opened a child with a different model;
and inspected a live High/Fast response. Captures are
`message-metadata-before.png`, `message-metadata-after.png`,
`child-message-metadata.png` and `live-message-metadata.png` in `build/ui-audit/`.
Real provider/version coverage and the final complete design audit remain open.

The follow-up native check verified `Stopped` after provider acknowledgement,
with no active fixture process left. Unconfirmed exits retain their warning.
The source manager now uses the shared cream background and passed arrow-key
navigation through Local profiles, Cloud accounts and GitHub, then Command-Q
from the sheet. Captures: `acknowledged-stop.png` and
`sources-shared-style.png`.

Validation for this change passed the Go local CLI tests and the native
conversation, context, sharing and state suites. The cancellation wording then
passed the conversation suite again. The latest normal development build is
`build/Capybara Camp Message Review.app`, using a separate bundle identity and
synthetic profiles. This does not complete the full UI or release acceptance.

The final normal signed build also displayed the three metadata cases together
in `message-normal-final.png`. It uses synthetic state, leaves the existing app
in place, and does not register the production source-link scheme.

## Card and conversation layout review

Compared the reference's final CSS with the native board, ordinary chat and
child chat. Card height is 264 points, with fixed heading, summary, model,
status and action rows. Local and shared plans use the same card component as
provider sessions. The compact planning rows retain settings in their menu or
accessible description, and their provider buttons open the corresponding chat.
History has its own action. Colors remain neutral, including the separators.

The first computer-use cycle checked single and comparison cards, opened the
Claude planner, entered planning history, selected the accessibility child and
returned to Main chat. The child title, read-only status, parent link and
separate recent-session list were visible after narrowing the window. Captures
are `native-board-pass21.png`, `native-child-pass21.png` and
`reference-child-pass21.png` in `build/ui-audit/`.

That cycle caught the loss of the Claude robot's eyes when applying a template
tint. The muted icon now has its own colored drawing. It also identified the
extra empty context row above the composer. The shared context chips now appear
above the transcript, and the chat picker opens from `@ Context` beside Send.
The composer follows the reference's 100-point outer height.

The state, local/shared planning, implementation, conversation and context
suites passed. The normal signed development build then verified the compact
Claude icon, the Context button, typing `@`, choosing a two-message snapshot,
the chip above the transcript, removal, and clearing the synthetic draft. The
strip retained its bounded height. Captures are `final-board-pass21.png` and
`final-context-pass21.png`.

The current build is `build/Capybara Camp UI Audit Current.app`, with bundle ID
`com.capybaracamp.current-layout-review`, isolated synthetic state and an Apple
Development signature. It does not register the production source-link scheme
or replace the user's existing app. The diff passed the local secret scan.
This review does not complete the remaining cloud, memory, skills or full
release audit.

## Streaming regression check — October 21

The isolated `Capybara Camp Stream Review.app` used the current shared native
module and synthetic provider transport. Computer use verified the main chat,
separate subagent transcript, return to Main chat, and a running response with
its final marker visible above the composer. The sidebar retained the subagent
tree above ten recent sessions. Reported High/Fast settings appeared for the
running fixture without relabeling old messages. Stop returned Active now to
zero and displayed Stopped. The capture is `build/ui-audit/stream-review.png`.

This check validates the local rendering used by the new cloud projection; it
is not a live cloud or two-Mac acceptance run. The cloud projection and shared
approval format also have native regression checks. Full parity, minimum-width
cloud controls and long-running memory/CPU measurements remain open.

## Execution location review — October 21

The same isolated native build now opens **Run on** from both the card footer
and main chat. Computer use verified Local/Cloud switching, the disconnected
Camp entry point, opening and closing nested sign-in, and returning to the
original session. The first screenshot exposed a full-width selector without
the reference icons. The shared segmented control now supports icons, and the
sheet uses its compact presentation. The rebuilt sheet passed pointer and
left/right keyboard switching. The final capture is
`build/ui-audit/execution-location.png`.
Command-Q from this sheet also exited the app and its local helper, verified
by process inspection without reopening the app.

Native execution, sharing, cloud, Git connection and worker suites passed.
They cover lost prepare/assignment replies, restart with the same command key,
stale-review rejection, foreign workspace rejection, sign-out races, revoked
cache writes and private transcript copying. Local publication preserves the
cloud title, model, activity and transcript after assignment. The full Go race
and vet run passed, including owner-only workspace discovery and the guard
against moving away from published cloud history without a reviewed transfer.

This pass does not accept the signed-in setup flow, actual provider execution,
two-Mac behavior, transfer flow or the full final design audit. Those remain in
the V1 checklist.

## Signed-in cloud handoff — October 21

The isolated native build signed in to a disposable PostgreSQL-backed Go API.
Test-only adapters supplied synthetic Git and provider accounts; no provider
process or remote Git operation ran. Computer use covered selecting a Camp
project, copying the transcript privately, choosing the provider/Git accounts
and repository, preparing and reviewing the pinned revision, and assigning it
to Cloud. The server confirmed `shared=false` and `allow_requests=false`.

The walkthrough found and fixed three handoff defects: a background update
could switch a private cloud chat back to its local copy; the card cache did
not invalidate when only execution metadata changed; and the returned local
ticket lost its Camp project label. A private cloud card also incorrectly used
the shared-session icon. The corrected build queued and cancelled a synthetic
prompt while retaining the cloud chat, returned to the local chat with an
unsent draft intact, and kept both returned tickets in the Demo project filter.
Captures are `build/ui-audit/private-cloud-chat.png` and
`build/ui-audit/local-handoff-project.png`.

The execution, projects, state, sharing, cloud and worker native suites passed.
The full Go race run passed all packages except an HTTP test that still
expected manual workspace retirement. Its expectation now checks the atomic
retirement on handoff; the complete HTTP package then passed with the race
detector. `go vet ./...` passed. A Go domain test confirms the same account can
claim the next workspace after this return. This is not the final pair of
complete code reviews or a live cloud acceptance run.

## Cloud chat controls review

The native walkthrough uses private synthetic Codex and Claude cloud tickets
against the real Go API and disposable PostgreSQL. No provider login, model
call or project command runs. The fixture deliberately drops the first control
submission response after committing it.

Computer use verified Command–Return steering, automatic recovery of the lost
response, rejected-steering restoration without resending, Command–Shift–Return
queueing, stop, and a queued turn completing. Claude's approval used the existing
approval component; both providers showed their acknowledged direction in chat.
The transcript retained accepted steering after completion. Reopening the app
and reconnecting retained the saved conversations and notifications.

This caught a native layout stall when the first cloud turn started. Sampling
showed repeated SwiftUI minimum-size calculations. The chat now lays out inside
its concrete viewport, and the same open/start/steer flows remained responsive.
The control-status list is height-bounded and scrolls independently.

Two additional corrections came from the walkthrough: an activity-only server
update now moves a completed card into Done, and successful traffic clears a
temporary offline badge. The final walkthrough showed both cloud cards in Done
and High effort in an active chat from the reported stream settings.
Captures are `build/ui-audit/cloud-steering.png` and
`build/ui-audit/cloud-controls-completed.png`.

This is one-Mac synthetic UI evidence. Complete cloud runtime settings across
unopened cards, live provider controls, two-Mac operation, the full design audit
and the release performance/soak gates remain open.

## Transfer review and native bridge check — October 21

Computer use inspected the reference's Run on and Move to cloud sheets. The
review keeps the selected ticket, plans, subagents and conversation together,
shows the pinned Git revision and reviewed changes, lists excluded credentials,
and distinguishes imported context from an unavailable native-resume option.
The reference capture is `build/ui-audit/reference-move-review-current.png`.

The current native Run on view does not yet implement that complete transfer
review. Its existing Local/Cloud selector, heading, spacing and connection
entry point were rechecked in a fresh isolated, signed development build.
Left/right keys switch the selector. Main-chat navigation still places saved
subagents above ten recent sessions, opens a separate child transcript and
returns to the parent at its latest messages. Captures are
`build/ui-audit/native-run-on-current.png` and `native-chat-current.png`.

A production integration gap was found alongside this review: the native bridge
allowlist rejected the Go recovery actions. Both actions are now accepted.
`BridgeIntegrationTests` builds a disposable app bundle and calls the actual
coordinator's default callbacks through its bundled Go executable, with a
synthetic support directory. It verifies marker discovery, resolution and
rejection of an unknown command. Store-level and mocked transport tests alone
did not cover this boundary.

This is a focused local review, not completion of the final whole-app audit or
of the missing native transfer workflow. No live provider or user files were
moved during this check.

## Cloud child history — October 22

The native child view now opens separately published cloud transcripts and
exact child-message citations. Previously opened child catalogs and pages have
a bounded offline cache. Saved children show **Saved**, and a cached transcript
shows **Offline copy** with the save time in its help text. The existing main
chat link and transcript layout are retained.

The focused native checks cover cache restart, pagination, parent/account
isolation, access changes, late responses and size limits. The final iteration
also tests a real connection failure against a reserved loopback port.
Computer use still reports that the Mac is locked. These checks do not verify
the final visual layout; compare the online, offline, reconnect, removed-child
and citation states against the reference once the Mac is available. The
native transfer review also still needs its full click-through.

## History-bound transfer review — October 22

The review now reports the included subagent count and requires a synchronized
history binding before capture. Native and packaged-helper tests cover missing
or stale bindings, changed children and final source verification. The main and
transfer audit apps were rebuilt with the existing Apple Development identity;
their strict code-signature checks pass. The transfer harness uses the real Go
capture helper and synthetic files, with a mock cloud coordinator.

Computer use still reports a locked Mac. The added review line, disabled/ready
states, changed-history error and full transfer click-through have not received
visual acceptance. These development signatures do not satisfy the public
Developer ID/notarization release gate.
