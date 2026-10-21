# Execution approval transport

The native CLI bridge has a separate implementation mode. Ordinary chat and
planning retain their read-only configuration. Implementation requires an
explicit internal approval marker and can create a new persistent provider
session or resume an existing one. The marker is not a remote authorization
credential; the approved-plan controller must bind it to the selected plan,
profile, model, project and permissions before this becomes a user-facing flow.
That controller and the same-ticket implementation transition are still pending.

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
Codex no-tools probe created a persistent session and completed. Claude's live
control channel initialized but the selected account returned its weekly limit;
live Claude execution is not validated.

Protocol references: [Codex app-server approvals](https://learn.chatgpt.com/docs/app-server),
[Claude approval and input handling](https://code.claude.com/docs/en/agent-sdk/user-input),
and [Claude permission rules](https://code.claude.com/docs/en/permissions).
