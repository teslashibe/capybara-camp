# Iteration 2: planning, client projects and multiplayer execution

Approved scope, October 21, 2026. These are implementation tickets; the published app has not gained these features yet. Parent: [V1 epic](https://github.com/teslashibe/capybara-camp/issues/1).

## Delivery order

1. [Fix live Codex state](https://github.com/teslashibe/capybara-camp/issues/9). The live Codex observer is implemented locally; finish approval-wait, terminal lifecycle and reconnect validation without relying on transcript age
2. [Add client projects](https://github.com/teslashibe/capybara-camp/issues/10) and [permission controls](https://github.com/teslashibe/capybara-camp/issues/12). They can be developed independently
3. Extend [planning](https://github.com/teslashibe/capybara-camp/issues/4) and add [GitHub workspaces](https://github.com/teslashibe/capybara-camp/issues/11), then integrate with [local/cloud execution](https://github.com/teslashibe/capybara-camp/issues/8)

## Planning entry points

| Path | Setup | Result |
| --- | --- | --- |
| Plan | One Codex or Claude profile, model, effort and speed | One planning turn, one chat pane, one board ticket |
| Plan with comparison | Existing Codex and Claude planner slots with independent settings | Two drafts, two reviews and one combined plan, with both chats |

Both paths accept pinned @ context, run in the background, retain version history and continue into implementation with a separately selected executor. Choosing comparison later starts a new explicitly approved version. Preserve existing comparison histories during migration. Single-model planning never requires a second login or makes an invisible comparison call. Keep the same neutral card dimensions and shared model controls.

## Projects and repositories

A Project is a client/work collection in a Camp. It contains tickets and may link several repositories. A Repository is a Git source. A workspace is one attempt's checkout. Store these as separate identities.

Create, rename and archive projects; assign or reassign imported sessions, plans and implementation tickets. Include an Inbox for unassigned work and allow tickets without a repo. A project can span multiple repos, and a repo can contribute to multiple projects. Reassignment changes grouping, not the execution directory, branch, sharing or permissions. Shared assignments use the existing Go API/PostgreSQL revisions and events; private local assignments use SQLite.

## Multiplayer Git

Each person connects GitHub independently. The repository owner installs the Camp GitHub App on selected personal or organization repositories. For work attributed to a person, use their user authorization, bounded by both their access and the app's installation permissions. An organization needs one installation, not a separate installation for every member. Provider logins determine model usage and remain separate from GitHub authorization.

The approved execution records the Camp actor, executor/provider account, Git actor, repository and base commit. The default Git actor is the executor owner. Cloud runners receive only the selected repo grant through a credential helper. Secrets remain encrypted and outside model context, transcript streams, command arguments and remote URLs. Reconnect never substitutes another person's credentials.

Use one isolated worktree and branch per implementing ticket. V1 writes to one primary repo per ticket; coordinate cross-repo work through linked tickets. Two people can work in the same repo without sharing a writable checkout. Push and PR controls show whose identity is used. Record pusher identity separately from commit author metadata. Permission bypass cannot override Camp ownership, repository grants or branch protection. Handle conflicts and uncertain push/PR replies explicitly; no automatic force-push, merge or deployment.

This uses the existing Go service and runner. Self-hosters configure their own GitHub App/callback against their Camp origin. GitHub's [user-token permissions](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app) support the intersection of user/app access; [installation tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app) are a distinct credential and do not impersonate a human.

## Session permission controls

Add a compact Permissions control to the existing card menu, chat and setup views. Persist the selection separately from the model. Show saved and effective policy, capability restrictions and any pending next-turn change. Preserve Inherit as the migration default.

Codex has separate sandbox/profile, approval-policy and reviewer settings. Include supported read-only/workspace/full-access profiles, interactive or never-ask policy, and automatic review. The bridge now uses `on-request` with its read-only planning sandbox instead of hard-coding `untrusted`. The installed 0.163.0 CLI and [current docs](https://learn.chatgpt.com/docs/agent-approvals-security) define the remaining per-session controls. Named [permission profiles](https://learn.chatgpt.com/docs/permissions) must also respect managed restrictions.

Claude's installed 2.1.306 CLI exposes Manual, acceptEdits, plan, auto, dontAsk and bypassPermissions. [Auto and bypass differ](https://code.claude.com/docs/en/permissions): auto evaluates actions, while dontAsk denies operations that would prompt. Availability and managed restrictions are provider-owned. Do not simulate an unavailable mode or assume bypass removes every user question.

Keep the existing approval transport. Display provider requests the selected mode still produces, using only offered decisions. Bind responses to owner, session, turn, request and policy revision. Waiting tasks show Needs you and notify the owner; another member cannot approve without an explicit grant. Apply a policy change live only when confirmed by the provider protocol, otherwise at a displayed safe boundary. Invalidate unstarted execution approvals bound to an old policy. Observing an external session never changes it.

## Acceptance

- Create a single-model plan with only one provider account; compare two providers in a separate path; implement either on the same logical ticket
- Put a no-repo plan and tickets from two repos in one client project; reassign a running ticket without moving its checkout
- Two independent users run in separate branches using their own Git authorization; wrong-user and revoked grants fail
- Codex automatically moves Ready → In progress → Needs you, including external sessions and approval waits; stale process/log evidence never shows a false running state
- Manual/auto/bypass settings reflect real provider behavior, with remaining requests routed to the correct owner and stale answers rejected
- Run native computer-use, privacy/recovery and performance checks; include these flows in the final recorded demo

No new backend language, queue or database; no CRM, new Git hosting provider, automatic merges or multi-repo atomic execution. Hardware provisioning and final release remain the existing V1 milestones.
