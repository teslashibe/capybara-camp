# Bundled Go helper

The Mac app builds `server/cmd/camp-local` for each requested architecture and bundles it in `Contents/MacOS`. The helper uses stdin/stdout JSON and opens provider SQLite databases read-only. It needs no separate Go installation on the user's Mac. The native app and helper are signed as part of the same build.

The current development build routes profile discovery, session import, profile addition, transcript reads, shared transcript export, Claude live activity, model catalogues, sub-agent discovery, permission discovery, approval-context fingerprints and the Codex live observer to this helper. Profile roots stay part of every imported ID. Existing `sources.json`, `import-cache.json` and transcript indexes remain readable, including stable export ordinals and tombstones. A changed fingerprint rebuilds an index without replacing message identities.

Each invocation closes its files and databases before exit. Visible history keeps only the requested message tail in memory. Metadata discovery reads at most 100 records per Claude transcript and one record per legacy Codex transcript. Transcript export pages remain limited to 200 messages and 1 MiB. Incomplete Claude writes roll back index updates. A failed source stays visible for the native app's retention logic, and cancellation leaves the prior cache intact.

Sub-agent history does not prove that a process is running. Stored Codex descendants remain unknown until a live observation supplies state. Claude workflow activity requires a busy parent and its latest workflow to remain unfinished; ending that workflow does not make an older interrupted workflow appear active.

## Migration still in progress

Conversation execution, approval relay and the attached-context tool still use the existing Python adapter. The former Codex observer code also remains as the conversation ownership check and a regression reference until that execution path is ported. The Python regression suite and inline native fixtures remain. The app still requires Python until those paths have Go or Swift replacements and pass their existing behavior checks. V1 must remove the remaining Python source, runtime requirement, fixtures, CI setup and documentation requirements.

Claude status comes from the installed CLI's safe-mode `agents --json` response. Registry files alone never prove activity. Each probe uses its selected profile, discards inherited provider credentials and tuning, caps stdout at 2 MiB and stderr at 64 KiB, and has an eight-second timeout within a thirty-second activity request. Failures report status as unavailable without exposing raw provider errors. Cancellation and timeout stop only the process group created by that probe.

Go regression tests live in `server/internal/localcli` and run through `test.sh`. They cover separate profiles, read-only databases, legacy stores, interrupted writes, existing transcript index migration, bounded export, sub-agent workflow state and cancellation. The cloud HTTP service retains its existing package boundaries; the local helper does not require a running cloud service or PostgreSQL.

## October 21 validation

The Go reader and previous adapter each found 33,065 sessions across three real sources, with identical IDs and models and no read errors. One displayed title differed only by removed trailing whitespace. The comparison used separate local caches and did not write provider stores or submit prompts.

A signed development Mac build imported a synthetic profile, opened its complete transcript and sub-agent history, and displayed new transcript entries while the main chat remained open. Both arm64 and x86_64 helper builds passed. Go race tests, all 102 existing Python regression tests and all 14 native suites passed. This validates the migrated read paths; live provider execution and the remaining Python removal are still open.

The activity migration matched five live Claude sessions, their IDs/states and all model catalogues across three profiles. Computer use verified simultaneous Codex/Claude cards in In progress, the live Claude chat with 43 sub-agents, and Opus effort/Fast controls on a synthetic card. No provider prompt was submitted. The helper also builds for Linux amd64. Process tests cover timeout, oversized output, private error suppression and an orphaned descendant; Go race/vet and the retained Python/native suites passed again.

Permission discovery uses a Go JSON-RPC connection with a 16-event queue, a 32-event backlog and 1 MiB frames. Requests, blocked input writes, cancellation and child cleanup are bounded. Provider requests and client responses retain separate ID handling, including collisions. Invalid managed-policy responses fail closed. A repeated-connect test checks that readers and descriptors do not accumulate.

The Go and previous adapters returned identical Codex and Claude permission catalogues and matching approval fingerprints for four cases, including Unicode paths and explicit permission selections. Native computer use verified both dialogs without saving changes. Existing ASCII JSON fingerprint encoding is preserved so the migration itself does not invalidate saved approvals. Actual file or permission changes still require a new approval. This connection currently serves permission discovery; conversation execution moves separately.

The native Codex observer now also uses Go. Its socket client permits only
initialize, loaded-thread listing and thread reads. Process discovery verifies
writable rollout device/inode identities; buffers and retained metadata are
bounded. A ten-second live comparison matched all 12 observed sessions and
their states against the prior adapter. A final smoke run used about 27 MiB
peak RSS and exited successfully on cancellation. These short runs are not
the release soak test. Go race/vet checks cover socket fragments, ping/pong,
approval-ID collisions, private socket permissions, partial records, buffer
limits and repeated shutdown. The helper builds for Mac arm64/amd64 and Linux
amd64.
