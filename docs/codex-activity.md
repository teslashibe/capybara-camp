# Codex activity observation

V1 development builds observe Codex profiles without starting or resuming their sessions. The published v0.0.1 does not include this observer.

One supervised Go helper watches the selected profiles. When a profile has a private local daemon socket, the helper reads loaded-thread metadata and subscribes to status events. Active turns appear in In progress; approval and user-input waits appear in Needs you. The observer never answers provider requests.

Codex Desktop can run a separate private stdio server. For those sessions, Camp checks which Codex process holds the exact rollout inode open for writing, then reads the turn lifecycle markers incrementally. A transcript's age, a process ID, or an unfinished saved turn alone cannot establish live activity. Discovery runs every two seconds; known files are checked every 100 milliseconds, and only changed snapshots cross into the app. Large existing rollouts show unknown until the initial scan catches up.

The private desktop rollout does not persist approval or user-input requests. Camp can show that turn's running/idle lifecycle, but cannot distinguish its approval wait or answer it remotely through this path. Camp-owned conversations retain their own approval transport. Exact external waiting-state support requires a provider-supported connection to that desktop runtime and remains part of issue #9.

Session identity includes the profile. Saved child edges group subagents under their parent; a child's running state comes from its own live observation. Loaded or open external sessions remain owned by their original client, including when idle. Camp refuses to resume them until they are closed there.

Frames, snapshots and incremental read buffers have size limits. Disconnects clear reliable running state, and profile removal invalidates callbacks from the previous helper. Probe subprocesses are reaped on cancellation. The helper does not retain transcript text or read login credentials.

## Validation

The current native observer runs through `camp-local codex-activity-watch`.
Go tests in `server/internal/localcli/codex_observer_test.go` cover partial and
oversized socket frames, read-only RPCs, private socket permissions, approval-ID
collisions, inode ownership, partial rollout records, repeated shutdown and
bounded unfinished-record buffers. The Python reference remains temporarily
for the conversation ownership check while that execution code is migrated.

A ten-second comparison on October 21 found the same 12 live Codex session IDs
and states in both implementations: eleven idle and one running, with no decode
or observer errors. Both observer processes exited cleanly on cancellation.
No model turn was started for this check.

`Tests/test_codex_activity.py` covers the socket protocol, bounded frames, fragmented messages, read-only calls, turn transitions, partial writes, changed inodes, ownership checks, subprocess cleanup and an end-to-end helper stream. `Tests/CodexActivityTests.swift` covers board transitions, independent Claude polling, removed profiles, helper replacement and unexpected EOF. These checks supplement the full Python and native regression suites.

Real-provider observation and native computer-use evidence are recorded separately from synthetic protocol tests. A short runtime check does not satisfy the V1 performance soak, two-Mac validation or release review gates.

On October 21, the full suite passed 96 Python tests and 12 native suites. Computer use on one Mac verified two active Codex chats in In progress after a 33,058-session import, the matching running state in open chat, and concurrent Claude activity. The disposable app then exited normally and its observer stopped. No provider prompt was submitted during that walkthrough.

A 12-second real observer sample reached the active desktop turn after 2.6 seconds of initial history indexing. Memory settled around 45 MiB; shutdown took 18 ms. These measurements are a startup/cleanup smoke test, not a latency benchmark or leak proof. Exact private-desktop approval waits and the remaining native terminal/completion/reconnect acceptance cases are still open in [issue #9](https://github.com/teslashibe/capybara-camp/issues/9).
