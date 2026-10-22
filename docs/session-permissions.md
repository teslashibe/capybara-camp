# Session permissions

Local V1 development supports explicit permission settings for imported sessions
and plan implementations. The public v0.0.1 release remains a chat-only preview.

Open Permissions from a card's menu or the shield button in chat. Existing
imports keep Chat preview only enabled until the user explicitly applies project
permissions. Disabling preview enables the selected CLI's project tools on the
next Camp turn. Planning drafts remain bounded, tool-disabled calls. The
implementation setup uses the same permission fields as the session editor.

Codex settings keep named permission profiles, the legacy sandbox, approval
policy and reviewer separate. Selecting a named profile clears an explicit
sandbox and vice versa. The catalog reads configuration and managed requirements
from the installed app server without starting a thread. It includes installed
named profiles, on-request/never and user/auto_review where requirements allow.
An empty managed allowlist permits no corresponding choices. Retired untrusted
is never sent as a selected policy.

Claude choices come from the installed CLI's help: Manual, acceptEdits, plan,
auto, dontAsk and bypassPermissions. Camp passes the exact permission-mode value.
Auto availability and managed restrictions remain Claude's responsibility.
dontAsk denies actions that need approval; it does not grant unrestricted access.

## Persistence and turn boundaries

Private per-session choices live in owner-only `session-permissions.json`.
The permission selection is included in the existing execution fingerprint,
alongside provider, profile, working directory and relevant configuration files.
Changing those inputs requires another review before the next project-tool turn.
No settings or rules are silently written into the provider's configuration.

A saved change applies to the next Camp turn. It never changes a running
external session, which remains watch-only. A running Camp turn keeps its original
settings and pending requests. The editor shows its reported effective policy
separately. Queued prompts retain their permission revision; a changed revision
pauses the queue and requires review and requeueing. A plan implementation also
records the selected permissions and fingerprint on each attempt.

Codex must acknowledge the selected policy in its thread response before Camp
submits a prompt. Claude reports its effective mode through the initialized
stream. Missing or different confirmation fails the turn instead of being
displayed as a successful policy change. Provider restrictions are never
replaced by an Allow fallback.

## Approval requests

Pending local requests move the card to Needs you and create an attention
notification. The same request remains accessible in chat. Codex command
decisions use the actual offered choices, including session-scoped approval and
exact proposed rule amendments when offered. The reply uses the original
decision payload; the UI cannot invent another command or rule. File approvals
still require a complete diff. Claude tool approvals and questions retain their
original input. Duplicate, cancelled and mismatched request answers are rejected.

## Validation and remaining work

Python tests cover provider-specific validation, managed allowlists, immutable
fingerprints, explicit CLI mode arguments, acknowledgment before submission,
and offered approval choices. Native tests cover saved choices, next-turn
behavior, Needs you, stale queues and restart persistence. The native fixture is
synthetic and uses no provider credentials.

Live Codex 0.163.0 no-tools turns completed with read-only/on-request/user,
named :workspace/on-request/auto_review, and danger-full-access/never/user.
Claude 2.1.306 acknowledged Manual mode, then returned the account's weekly limit.
This is not evidence of successful Claude tool execution or classifier behavior.
A separate live Codex file-edit probe reached the approval relay. Declining it
left the requested file absent. Native computer use verified both providers'
menus, saved choices, and a complete synthetic approval flow.

Shared request policy editing, remote approval delegation/routing, cloud runner
settings and two-Mac validation remain open in issue #12. Shared requests keep
their existing approval contract and preview runtime; local settings do not
silently enable tools for a teammate's queued request. Long-running performance,
full live tool-mode coverage and release validation remain V1 gates.

References: [Codex permissions](https://learn.chatgpt.com/docs/permissions),
[Codex app-server protocol](https://learn.chatgpt.com/docs/app-server),
[Claude permissions](https://code.claude.com/docs/en/permissions).
