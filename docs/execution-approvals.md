# Execution approval transport

[Iteration 2](iteration-2.md) adds explicit per-session permission controls and capability validation. The transport below is implemented; those controls are pending. Planning uses `on-request` with its read-only sandbox.

The native CLI bridge has a separate implementation mode. Ordinary chat and
planning retain their read-only configuration. Implementation requires an
explicit plan approval and can create a new persistent provider session or resume
the ticket's existing one. The local controller binds approval to the immutable
plan revision, selected profile/model settings and project. It saves each attempt
before provider submission and retains the same logical card and plan history.
The internal marker is not a remote authorization credential.

The bridge checks a fingerprint of the project/profile paths and relevant local
CLI configuration files immediately before launch. A changed fingerprint blocks
the turn and requires another review. This snapshot does not attest to the
account identity, entitlement or all dynamically supplied provider policies;
the installed CLI remains responsible for those checks and permission decisions.

Implementation uses the installed CLI's configuration and permission rules. It
does not pass permission-bypass flags or automatically save new permission rules.
The context tool can be added alongside existing MCP configuration. The Codex
app-server transport is experimental upstream and needs compatibility checks
against the supported CLI version before release.

The bridge relays these requests to the native approval component:

| Provider | Request | Response |
| --- | --- | --- |
| Codex | Command execution | Accept once or decline, limited to the offered decisions |
| Codex | File changes | Accept once or decline; approval requires the complete change preview |
| Codex | Additional permissions | Requested permissions for the current turn, or an empty grant |
| Codex | User input | Answers keyed by the original question IDs |
| Claude | Tool permission | Allow the original tool input once or deny |
| Claude | AskUserQuestion | Validated answers in the original question/input structure |

Pending requests are bounded to 16, each preview to 64 KiB and each question set
to eight. Oversized requests are declined without a truncated approval preview.
The native panel presents one pending request at a time, with decision buttons
outside the scrollable details so a long preview cannot hide them.
Codex requests are checked against the active thread and turn. Opaque local IDs
bind answers to their original requests. Cancellation removes pending UI state;
stale responses cannot authorize another request. Unknown control requests are
rejected. No request is automatically approved.

An answer leaving the native panel means it was written to the CLI channel. It
does not prove that a tool ran or that its effects completed. The turn still
requires its terminal result; interruption after a possible submission requires
review before retrying. Approval details and answers are held in memory, not
saved as reusable grants.

Validation includes fake CLI processes exercising both wire protocols, native
answer/rejection/cancellation tests, and a separate native UI fixture. A live
Codex no-tools probe created a persistent session and completed. A second live
probe completed two consecutive implementation turns and verified context
continuity when resuming the same provider session. Neither probe requested
project tool use or file changes. Claude's live
control channel initialized but the selected account returned its weekly limit;
live Claude execution is not validated.

Protocol references: [Codex app-server approvals](https://learn.chatgpt.com/docs/app-server),
[Claude approval and input handling](https://code.claude.com/docs/en/agent-sdk/user-input),
and [Claude permission rules](https://code.claude.com/docs/en/permissions).
