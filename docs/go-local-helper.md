# Bundled Go helper

The Mac app builds `server/cmd/camp-local` for each requested architecture and bundles it in `Contents/MacOS`. The helper uses stdin/stdout JSON and opens provider SQLite databases read-only. It needs no separate Go installation on the user's Mac. The native app and helper are signed as part of the same build.

The current development build routes session discovery, profile addition, transcript reads, shared transcript export and sub-agent discovery to this helper. Profile roots stay part of every imported ID. Existing `sources.json`, `import-cache.json` and transcript indexes remain readable, including stable export ordinals and tombstones. A changed fingerprint rebuilds an index without replacing message identities.

Each invocation closes its files and databases before exit. Visible history keeps only the requested message tail in memory. Metadata discovery reads at most 100 records per Claude transcript and one record per legacy Codex transcript. Transcript export pages remain limited to 200 messages and 1 MiB. Incomplete Claude writes roll back index updates. A failed source stays visible for the native app's retention logic, and cancellation leaves the prior cache intact.

Sub-agent history does not prove that a process is running. Stored Codex descendants remain unknown until a live observation supplies state. Claude workflow activity requires a busy parent and its latest workflow to remain unfinished; ending that workflow does not make an older interrupted workflow appear active.

## Migration still in progress

The provider process adapters, live activity, permissions and execution context still use the existing Python adapter. The Python regression suite and inline native fixtures also remain. The app still requires Python until those paths have Go or Swift replacements and pass their existing behavior checks. V1 must remove the remaining Python source, runtime requirement, fixtures, CI setup and documentation requirements.

Go regression tests live in `server/internal/localcli` and run through `test.sh`. They cover separate profiles, read-only databases, legacy stores, interrupted writes, existing transcript index migration, bounded export, sub-agent workflow state and cancellation. The cloud HTTP service retains its existing package boundaries; the local helper does not require a running cloud service or PostgreSQL.

## October 21 validation

The Go reader and previous adapter each found 33,065 sessions across three real sources, with identical IDs and models and no read errors. One displayed title differed only by removed trailing whitespace. The comparison used separate local caches and did not write provider stores or submit prompts.

A signed development Mac build imported a synthetic profile, opened its complete transcript and sub-agent history, and displayed new transcript entries while the main chat remained open. Both arm64 and x86_64 helper builds passed. Go race tests, all 102 existing Python regression tests and all 14 native suites passed. This validates the migrated read paths; live provider execution and the remaining Python removal are still open.
