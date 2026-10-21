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
owner-only local file retains inbox/read state, watched shared requests and up
to 4,096 completion fingerprints. One serial writer coalesces pending snapshots
off the UI thread. An alert is eligible for delivery only after its entry is
saved. A failed save leaves a visible error and suppresses desktop delivery.
The operating system controls actual presentation; Camp does not retry an
ambiguous desktop-delivery result.

Shared entries are partitioned by server, Camp and signed-in person. Switching
scope removes old desktop alerts. Losing access removes affected local inbox
entries and pending alerts. Opening a shared result makes a fresh authorized
session read before navigation. Opening an alert never approves execution.
Initial historical snapshots do not replay old alerts. A watched request that
finishes during a disconnect produces one inbox entry on reconnect without
replaying a desktop alert. Live fast completions do not require a separately
rendered started state.

The native inbox is a development slice. Server-owned recipient notifications
and synchronized read receipts across Macs remain to be implemented. Shared
result links currently open the authorized parent session; linking an older
completion to an exact transcript revision, child or memory result remains part
of the corresponding feature integration. Fully quit delivery is out of scope;
an APNs service is not included.

Validation uses a fake desktop adapter for persistence, duplicate suppression,
scope changes, access loss, permission denial, reconnects, bounded history and
native conversation routing. Computer-use checks verified the native inbox,
foreground toast, unread badge, retained entries and navigation to a combined plan. macOS permission
was enabled for the development-signed test app after the initial request was
denied. Actual desktop-banner presentation and the complete closed-window
workflow remain unverified; they are release gates, not inferred from API success.

API references: [notification authorization](https://developer.apple.com/documentation/usernotifications/unusernotificationcenter/requestauthorization(options:completionhandler:))
and [notification delegate](https://developer.apple.com/documentation/usernotifications/unusernotificationcenterdelegate).
