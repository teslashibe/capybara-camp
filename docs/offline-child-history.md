# Offline child history

The V1 development app saves child catalogs and transcript pages after an
authenticated read. If the Camp service becomes unreachable, an already-opened
child can be read from those saved pages. This does not download every child in
the background or make provider execution available offline.

## What is saved

The native SQLite cache stores the latest fetched child catalog and the pages
the user has opened, including earlier pages loaded explicitly. Each entry is
bound to the server origin, signed-in user, Camp, parent session, permission
revision and parent transcript revision. Child transcripts remain separate
from the main conversation.

The shared child-cache budget is 32 MiB of encoded payload and 128 entries across
all Camps in that cache. SQLite indexes, journals and reusable free pages add
storage overhead. Catalogs are retained before transcript pages; older pages are evicted
first. Pages without a catalog are removed. An eviction means that page needs a
connection before it can be opened again. The cache lives in the existing
owner-only Camp cache directory and is cleared with that Camp's cache.
Native cache version 5 adds this storage without changing saved sessions or
queued requests. Earlier development builds that only accept version 4 cannot
open it; keep the current build when resuming pending work.

## Offline behavior

A saved transcript is labelled **Offline copy** with its saved time available
on the label. The sidebar reports that it is showing saved subagents. Only
connection failures such as an unreachable host, timeout or lost network can
use this fallback. Cancellation, an invalid response, a certificate error or a
server error does not silently switch to cached content.

An authorization failure or a missing parent/child clears the parent's saved
child history. A fetched catalog replaces the prior catalog and invalidates
pages when its revision or child metadata changes. A `children.updated` event
invalidates those entries in the same SQLite transaction that advances the
event cursor. A parent permission change, parent removal, sign-out or cache
erasure also removes them. Each parent has a persisted invalidation counter, so
a request started before a child update cannot restore its old catalog or page.
Late responses also cannot repopulate an erased Camp cache.

While disconnected, the Mac cannot learn about a remote permission change.
The offline label identifies the content as a previously authorized saved
copy. Reconnection applies the server's current permissions and updates before
subsequent reads. Exact historical citation inspection continues to require
a connection; offline browsing does not grant access to uncached revisions.

## Verification

Run the focused native suites from the repository root:

```sh
./test.sh CampChildHistoryTests CampCloudTests CampStateTests SessionMentionsTests
```

The child suite checks restart persistence, earlier pages, account and revision
isolation, removal and permission changes, stale-response rejection, row and
byte-budget eviction, and transport-error classification. A non-listening,
reserved loopback port exercises actual connection failure and the native
fallback without contacting a real service. Native visual and
first-paint/reconnection timing acceptance still belong to the final
computer-use and performance review.
