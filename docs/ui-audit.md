# Native UI design audit

Reference: the approved [interactive design](http://127.0.0.1:8770/capybara-camp-interactive.html).
The native app has **not passed the full design audit**. This is the working
discrepancy list, not a release approval.

## Review method

Use a separately identified native development app and synthetic provider
profiles. Leave the user's running sessions untouched. For each surface,
capture the reference and native view at matching window sizes, compare visual
details, click through its interactions, fix discrepancies, rebuild and repeat.
Check empty, loading, active, waiting, error and completed states. Record what
was actually verified; do not infer full parity from one screen or unit tests.

Compare glyph geometry and color, font size/weight, spacing, borders, backgrounds,
button/menu treatment, card dimensions, copy, focus, keyboard actions, pointer
targets, resizing and scrolling. The final pass repeats the entire inventory on
the release candidate after remaining capabilities are implemented.

## Findings and verification

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
- Memory and skills: all tabs, scope/provider/account/model controls, provenance,
  consolidation and sync states, editing and conflicts
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

Launch `build/Capybara Camp Interaction Audit.app` with `open -n --env`, supplying
`CAMP_UI_AUDIT=1`, an absolute `CAMP_DEV_PROFILE_ROOT`, and an absolute
`CAPY_SUPPORT`. Both directories must contain a `.capybara-ui-fixtures` marker;
the profile root also needs the Go helper's private-directory permissions. Use
only synthetic profiles and state. The recorded run used artificial read-only
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
