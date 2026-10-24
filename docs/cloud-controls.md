# Cloud chat controls

The session owner can steer, stop and queue prompts from an active cloud chat.
The composer uses the same controls as local chat: Command–Return sends a
direction to the current turn, and Command–Shift–Return queues a new prompt.
New context attachments belong to a queued prompt, because steering cannot
change the current turn's pinned context. Other Camp members retain the normal
request/approval flow and cannot control the owner's running provider process.

Codex adds steering input to its current turn. Claude interrupts the current
response and submits the new direction through its existing CLI conversation.
The Go runner uses the same provider adapters as local conversations.

## Delivery and recovery

Before clearing the composer, the native app saves a control's UUID, exact
body, target request and session in the existing owner-scoped SQLite outbox.
A lost response retries that same control. It never creates a new request or
silently applies the direction to a later turn. Switching chats or restarting
the app preserves pending controls. Signing out or losing session access clears
their private cached content.

Server acceptance and provider acceptance are separate states. After the server
saves a control, the app waits for the provider's acknowledgment. The status row
disappears only after confirmed acceptance. Rejected or unconfirmed steering
stays visible with **Restore prompt**, which returns the original text to the
composer without sending it. A finished turn without a provider acknowledgment
is unconfirmed, even if its HTTP submission succeeded.

Approval answers use the same outbox and the existing approval component.
Answers stay bound to the original approval event sequence. A rejected answer
may be corrected; accepting a later answer does not change the recorded result
of the earlier one. The runner persists delivery IDs before writing to the CLI.
This prevents automatic redelivery after an uncertain write; it is not an
exactly-once guarantee from the provider.

Retries and status reads are bounded and cancelled when the Camp scope changes.
The visible control queue has a bounded scroll area so it cannot displace the
composer or transcript. New commands arriving during an outbox flush are picked
up by that flush rather than waiting for another unrelated event.

## API and migrations

`POST /v1/camps/{camp}/requests/{request}/controls` retains the existing control
UUID and payload contract. The new owner-only
`GET /v1/camps/{camp}/requests/{request}/controls/{control}` returns the control
ID, request ID, kind and `pending`, `accepted`, `rejected` or `unconfirmed` state.
It returns no submitted text or credentials. Current membership and request
access are checked on every read.

Server schema 18 stores the resolution on the exact control row. Existing
accepted steering is retained; older approval records without an acknowledgment
remain unconfirmed. Native cache version 4 adds the target session to outbox
records and retains existing queued work. Apply the server migration with the
matching release before deploying the updated app and runners.

Live provider and two-Mac acceptance remain part of V1 delivery. The interactive
test service uses synthetic provider events and does not execute project tools.

## Local validation

The full Go race suite and vet passed with schema 18. Provider subprocess tests
exercise both protocols, duplicate control submissions and accepted steering in
the saved transcript. A rejected approval retains its result after a later
answer succeeds. The migration test preserves earlier accepted steering.

Native cloud/cache checks, execution and state suites passed after the final
UI fixes. Computer use covered both providers, a dropped submission response,
steering rejection/restoration, keyboard queueing, cancellation, approvals and
completion. See [the UI audit](ui-audit.md#cloud-chat-controls-review). These
results do not replace live cloud provider or two-Mac acceptance.
