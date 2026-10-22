# Bundled Go helper

The Mac app builds `server/cmd/camp-local` for each requested architecture and bundles it in `Contents/MacOS`. The helper uses stdin/stdout JSON and opens provider SQLite databases read-only. It needs no separate Go installation on the user's Mac. The native app and helper are signed as part of the same build.

The current development build routes every local action through this helper: profile discovery, session import, profile addition, transcript reads, shared transcript export, live activity, model catalogues, sub-agent discovery, permissions, approval fingerprints, conversations and attached context. Profile roots stay part of every imported ID. Existing `sources.json`, `import-cache.json` and transcript indexes remain readable, including stable export ordinals and tombstones. A changed fingerprint rebuilds an index without replacing message identities.

Each invocation closes its files and databases before exit. Visible history keeps only the requested message tail in memory. Metadata discovery reads at most 100 records per Claude transcript and one record per legacy Codex transcript. Transcript export pages remain limited to 200 messages and 1 MiB. Incomplete Claude writes roll back index updates. A failed source stays visible for the native app's retention logic, and cancellation leaves the prior cache intact.

Sub-agent history does not prove that a process is running. Stored Codex descendants remain unknown until a live observation supplies state. Claude workflow activity requires a busy parent and its latest workflow to remain unfinished; ending that workflow does not make an older interrupted workflow appear active.

## Runtime and test dependencies

The app no longer invokes or bundles Python. Both provider conversations, approval relay, temporary context lifecycle and external-session ownership checks run in Go. The remaining Python modules, regression files and inline native fixtures have been removed. Build, test and CI use Swift and Go. The separate `camp-test-provider` executable supplies controlled native test responses and is excluded from app bundles. The [migration record](python-test-migration.json) identifies replacements for all 103 former tests.

The full Go race/vet checks and all fifteen native suites pass without a Python
interpreter. A fresh universal development build contains arm64 and x86_64 app
and helper binaries, retains `com.capybaracamp.app`, and passes signature
verification with the existing development identity. Source and bundle checks
find no Python files; the test provider is absent from the app bundle. This is
an unreleased development build, not a notarized V1 release.

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

## Attached context and approval migration

The Go context server exposes only `read_session` for the exact messages attached
to the current turn. It cannot discover other sessions, read arbitrary paths or
send prompts. It preserves existing source links and sorted UTF-8 message
digests, rejects unknown fields and invalid UTF-8, caps manifests at 1 MiB,
selected text at 256 KiB and one read at 64 KiB. Stdio requests are capped at
128 KiB. The manifest must be an owner-only regular file inside an owner-only
directory; symlinks and FIFOs are rejected. Cancellation closes the server's
stdin so an idle process can exit.

The Go approval relay retains at most sixteen requests and sixteen file
previews. It copies the provider's offered choices, diffs and permission scope
before displaying them. Approval replies cannot add permissions, mutate a saved
rule or approve another thread's request. Duplicate and cancelled requests are
handled once. Questions must fit the native renderer's bounds, and a failed
provider write does not report success. Both provider turn loops now use this
relay. Codex confirms selected permissions before submitting a prompt; Claude
initializes its approval channel before the first prompt and confirms its mode
before exposing tool approvals.

Validation passed with 103 retained Python tests, fifteen native suites, Go
race tests and vet. A signed arm64 development app served synthetic context
through its actual bundled helper, preserved the source digest and links, and
rejected unshared sessions, arbitrary paths, sending and a publicly readable
manifest. This process check found an idle-stdin cancellation defect that the
initial unit tests missed. The helper now uses a pollable input descriptor;
the repeated regression passes and the packaged process exited in 8 ms after
SIGTERM. Mac amd64 and Linux amd64 builds also pass. These checks do not replace
the final live-provider, two-Mac or soak acceptance runs.

## Conversation runtime

The Go `conversation` action preserves the existing native event protocol. It
holds a lock derived from the canonical provider profile and native session ID,
including a newly created implementation ID before publishing it or submitting
its first prompt. External Codex writers and loaded daemon sessions remain
owned by their original client, even when idle. Claude uses its safe live-session
probe. Neither check resumes another client's session.

Chat and planning disable project tools and configured MCP servers before the
CLI parses desktop-only URLs. Selected context is the only additional read
tool. Planning creates an ephemeral Codex thread or non-persistent Claude
session and keeps its 8 KiB response limit. Implementation uses the approved
permission fingerprint and installed CLI rules. A Codex steering timeout pauses
the queue; a Claude steer waits for the interrupt acknowledgment, result boundary
and echoed new prompt before reporting acceptance.

Input queues, provider frames, output, file reads and process groups are bounded.
Cancellation joins the control reader and reaps the owned provider process.
Go process fixtures exercise both providers in chat, planning and implementation,
including a provider request that collides with a client RPC ID. A live isolated
Codex planning turn read a new synthetic validation word through the Go context
tool and returned its pinned source link. This validates Codex context use; it
does not prove successful live Claude execution, cloud execution or the release
soak gate.

The signed native walkthrough used one synthetic profile, with personal profile
files inaccessible to that test process. It verified automatic discovery,
transcript append updates, persisted model/effort/Ultra Fast choices and a safe
ownership-check failure. That failure exposed a composer recovery bug: an
unaccepted prompt disappeared after the error. The native completion handler
now restores it to the original chat without overwriting another chat's draft;
both the new regression and the repeated computer-use check passed. All fifteen
native suites passed against the updated module. The test app then quit normally
and its helper stopped.
