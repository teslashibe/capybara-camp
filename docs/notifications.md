# Completion notifications

The V1 development app has a native notification inbox with All and Unread
views, read controls and result links. Local turns, completed five-step plans,
observed terminal completions and shared request updates feed the same inbox.
Planning steps and subagent events do not each produce an alert.

The foreground app shows a dismissible toast. Closely spaced completions share
one summary toast; hover and keyboard focus pause dismissal. The header bell
retains the unread count after the toast closes. Desktop delivery is off until
the person enables it in Notifications and grants macOS permission. Session
titles and sounds are independently optional and off by default. Desktop
payloads contain only the local routing ID, with a generic message unless title
previews are enabled. The system's Focus and notification settings apply.
The Dock badge follows the unread count when enabled in macOS settings. Closing
the window keeps the app and its work running; reopening from the Dock restores
the same store. Quit flushes pending notification writes before stopping local
CLI processes.

History is bounded to 1,000 entries or 30 days and shown in pages of 50. An
owner-only local file retains inbox/read state and up
to 4,096 completion fingerprints. One serial writer coalesces pending snapshots
off the UI thread. An alert is eligible for delivery only after its entry is
saved. A failed save leaves a visible error and suppresses desktop delivery.
The operating system controls actual presentation; Camp does not retry an
ambiguous desktop-delivery result.

Shared entries are partitioned by server, Camp and signed-in person. Switching
scope removes old desktop alerts. Losing access removes affected local inbox
entries and pending alerts. Opening a shared result makes a fresh authorized
notification and destination read before navigation. Opening an alert never approves execution.
Initial historical snapshots do not replay old alerts. A request that
finishes during a disconnect appears in the inbox on reconnect without
replaying a desktop alert. Live fast completions do not require a separately
rendered started state.

The Go service stores one notification per completion and recipient in the
same transaction as the completed result. Chat recipients are the requesting
person and session owner; planning recipients are its creator and planner
owners. A person with several devices has one shared read/archive state. Other
people keep their own state. The event stream carries only an inbox-change
signal; clients fetch authorized entries. Read and archive commands use the
durable native outbox and idempotent server receipts, including offline changes.

Schema 6 is additive. Apply its migration before starting the matching server.
Each recipient's cloud inbox is bounded to 1,000 entries or 30 days. Current
membership and access to the destination and a plan's pinned source sessions
are checked on every read and change, including a repeated command. Signing out
clears the scoped local cache. Plan notifications open their saved version with
both provider drafts, reviews and the combined plan. Chat result links currently
open the authorized parent session; linking an older
completion to an exact transcript revision, child or memory result remains part
of the corresponding feature integration. Native shared-planning execution is
still in development. Fully quit delivery is out of scope;
an APNs service is not included.

Validation uses a fake desktop adapter for persistence, duplicate suppression,
scope changes, access loss, permission denial, reconnects, bounded history and
native conversation routing. Computer-use checks verified the native inbox,
foreground toast, unread badge, retained entries and navigation to a combined plan. macOS permission
was enabled for the development-signed test app after the initial request was
denied. Actual desktop-banner presentation and the complete closed-window
workflow remain unverified; they are release gates, not inferred from API success.

The shared-inbox check used the real Go API and disposable PostgreSQL, one native
Mac client and a second authenticated device connection on the same Mac. It
verified live completions without refresh, read receipts in both directions,
archiving, and opening the saved shared plan with both provider drafts. This
does not substitute for the release's two-physical-Mac test. The PostgreSQL race
suite checks recipient isolation, source revocation, replay and bounded history.

API references: [notification authorization](https://developer.apple.com/documentation/usernotifications/unusernotificationcenter/requestauthorization(options:completionhandler:))
and [notification delegate](https://developer.apple.com/documentation/usernotifications/unusernotificationcenterdelegate).
