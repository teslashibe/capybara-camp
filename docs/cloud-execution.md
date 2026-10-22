# Local and cloud execution

[Iteration 2](iteration-2.md) defines client projects, separate GitHub identities and per-ticket repository workspaces. A client project is a collection of tickets, not a filesystem directory or repository.

Approved V1 scope, October 21, 2026. This is an implementation contract, not a
claim that cloud runners or provider sign-in are shipped. The current Go service
stores shared sessions and prompt requests; the native app can publish selected
local transcripts. Cloud execution is the next runtime extension.

## What people see

- Manage sources keeps local profile import and adds Cloud accounts and Defaults
- Add each Codex or Claude account separately, name it, and complete the provider's
  sign-in in a browser. Show the verified identity, sign-in state, available models
  and usage when the provider exposes them. Unknown usage stays unknown
- Choose Local or Cloud as the default for new work. Save this per person and Camp;
  Local means the current Mac. Pick default accounts separately for each provider
  and location. Existing sessions keep their assigned runner
- Each card shows one small computer or cloud icon. Its menu and Chat offer
  Run on with the selected account. Keep the shared card dimensions and model
  controls; no new row of settings on every card
- Move to cloud previews the destination, project files, context and permissions.
  The same ticket retains its full history, plan, children, model settings and
  migration history. Moving execution does not grant team access
- Cloud work continues when the Mac sleeps. Queued work on an unavailable local
  Mac waits visibly. A failed cloud connection never silently starts work locally

## Identities and authentication

The first-run flow is Connect Camp → Add accounts → Choose defaults. Connecting
Camp uses the person's verified email and saved server address. Add accounts
offers Codex or Claude, an optional name, and the official provider sign-in.
After one account connects, Add another returns to the same flow. People may
skip cloud account setup and keep working locally. The app cannot authorize all
six provider accounts with a single Camp login: each distinct provider account
needs its own consent once per installation.

Defaults and per-task choices use the same location/account control. A planning
ticket stores two independent destinations; it must never represent both
planners as one Codex account. A mixed plan shows Local and Cloud on its board
location action. Opening that action returns to the two planner controls.
Starting freezes both destinations alongside their model settings. Changing a
global default only affects newly created work. Implementation chooses its own
destination and records it in the same ticket's history.

Selecting an unavailable account shows the specific reconnect action and prevents
Start; no invisible account replacement is allowed. Returning from account setup
restores the pending task and selection. Provider account identifiers, not
editable labels, bind defaults and work. The account's model and usage views
refresh after verified sign-in, without assuming either API is available.

A Camp login identifies a person. A provider account identifies whose Codex or
Claude allowance is used. A profile installation is that account's login on one
specific Mac or cloud runner. A directory name or account label proves neither
identity nor entitlement. Link installations to one provider account only after
matching provider identity evidence; otherwise keep them separate and unverified.
Four Codex and two Claude accounts must stay independently selectable. K signs in
to Camp independently and adds their own accounts. Membership never grants access
to another person's credentials or permission to spend their allowance.

The Go control plane requests an owner-bound login attempt from an isolated cloud
runner. The official CLI owns OAuth, callbacks and token renewal. Camp displays
only the validated provider URL and one-time code when needed. Start, status,
cancel and reconnect use authenticated owner-only endpoints. Attempts expire;
late completion after cancellation cannot publish a connected profile. Never
forward arbitrary CLI output to an auth screen or logs. Browser login needs an
exact provider allowlist, attempt binding and an authenticated callback/code relay.

| Provider | Cloud sign-in path | Account isolation |
| --- | --- | --- |
| Codex | Official `codex login --device-auth`; if unavailable, a supported browser callback flow. Explicit API-key sign-in is a separately labeled billing option | Separate `CODEX_HOME` for every installation |
| Claude | Official `claude auth login`, browser authorization and code fallback; `claude setup-token` is a supported headless fallback, with its expiry and reduced capabilities shown. Explicit API-key sign-in is separate | Separate `CLAUDE_CONFIG_DIR` and OS/container identity. Console profiles need their own supported isolation as well |

Do not bulk-copy live Mac login caches to cloud. Fresh cloud sign-in avoids two
machines racing to rotate one refresh token. Cloud credentials are encrypted at
rest, owner/profile-bound and available only to that profile's runner. Kubernetes
Secret references and encryption keys stay out of product records and logs.
The runner needs plaintext credentials while executing; self-hosting operators
are trusted administrators, and this is not end-to-end encryption. Disconnect
fences new work, terminates the profile's sessions and removes its credentials.

Normal provider renewal is automatic where supported. A daily health check may
report expiry or reconnect needs; it cannot guarantee a never-expiring login.
Usage belongs to the provider account across locations. Moving an account between
Mac and cloud does not reset its five-hour or weekly allowance. Automatic quota
rotation remains outside this release.

## Runtime and persistence

Reuse the Go API, PostgreSQL, event stream and native cache. Add one Go
`camp-runner` binary with Codex and Claude adapters; use official CLI stdio
protocols, not a second model API implementation. Local execution retains the
native adapter until the common execution contract is proven. No Python cloud
service or additional queue/database is needed.

PostgreSQL owns provider-account metadata, installations, runtime targets,
assignment revisions, migration manifests and durable attempts. Keep secrets out
of snapshots, SSE, transcripts and backup exports intended for users. A worker
authenticates with a narrowly scoped, revocable installation credential; it may
claim only its own assigned work. It never receives a human's broad Camp token.

Start with one isolated Linux pod and private persistent volume per owner/provider
installation, one active project workspace per pod, and one provider process at
a time. Serialize work within that installation; different installations may run
independently under a configured global cap. Use unprivileged users, dropped Linux
capabilities, resource and disk limits, restricted egress, no hostPath, no cluster
service-account token in the workload, and no shared writable provider homes.
Use a proven workload sandbox for untrusted project code; a container alone is
not a sufficient multi-user code-execution boundary. Keep orchestration access
outside the workload. Retained project files and provider state need encrypted,
off-host backups alongside PostgreSQL, with quotas and explicit removal.

Bound subprocess output, transcript batches, queues and reconnect buffers. Write
the durable attempt before provider submission. Reuse request IDs, assignment
epochs, heartbeats and the existing unknown-outcome recovery. A database retry
does not authorize repeating provider work.

Proposed additions, following existing ownership boundaries:

```text
server/cmd/camp-runner/
server/internal/runner/       # bounded CLI process and provider adapters
server/internal/camp/         # account, target, transfer and assignment SQL
server/internal/httpapi/      # owner-only login and transfer endpoints
server/internal/database/migrations/
Sources/CampSourcesView.swift
Sources/CampExecutionView.swift
deploy/kubernetes/runner/
```

## Moving a session

1. Prepare a destination for the same provider and owner. Confirm the signed-in
   account, CLI version, model, permissions and project are available. Any account
   change is explicit. Cross-provider context stays the existing planning flow
2. Finish or interrupt the source turn and obtain its acknowledged terminal state.
   Require release of external CLI ownership. Unknown provider outcomes block
   transfer. Freeze queued requests for an explicit cancel or reapproval
3. Inventory the chosen project root. Transfer a pinned Git revision plus approved
   dirty changes and explicitly selected untracked files. Exclude credentials,
   `.env` files, private keys and provider homes, even if tracked. Reject traversal
   and escaping symlinks, scan the manifest, bound file/total size, and preview
   exclusions. Capture submodules and private Git access separately. Never upload
   an entire home directory or install project hooks during transfer
4. Stage resumable chunks by digest into a destination workspace. Transfer only
   the selected session and approved child/context artifacts. Unsupported or
   missing attachments are reported before commit; failures leave the source
   available. Resuming a native session is enabled only for provider/version pairs
   proven by cross-OS tests. Otherwise offer Continue with imported context, keep
   a linked provider session ID and display the boundary in history
5. Atomically commit one new assignment epoch after preparation. Stale source
   workers can no longer claim, start, append authoritative output or finish work.
   Keep the logical ticket ID and pinned history. No process memory is migrated
6. Stream destination events into the same chat. Moving back uses the same
   contract and downloads the approved file changes. Conflicts require review;
   never overwrite a diverged local worktree automatically

Planning and memory consolidation use the same target/account selector. Each
planner can have its own target; implementation selects its target independently.
Skills install into the chosen profile with the existing revision rules. Cloud
execution does not depend on a Mac remaining online.

## Release checks

The operator handoff for the Hetzner server happens after a locally tested Go
runner and deployable container images are ready, with account isolation,
streamed output/approvals and restart recovery exercised. At that point the
owner and agent select and provision the box together, configure its domain
and TLS, and complete real cloud provider sign-in and disconnected-Mac tests.
The current shared-history service alone is not that handoff point. The owner
wants an initial cloud trial as soon as these prerequisites pass; that trial
does not waive the final V1 release checks below.

- Four Codex and two Claude accounts coexist; reconnect, cancel and restart never
  cross-bind identities. K cannot read or execute with another owner's account
- Local/Cloud defaults persist, apply only to new work, and fail visibly when the
  destination is unavailable. Cards stay the same size; icon targets have labels
- Both providers run a real cloud turn with the Mac disconnected, then replay the
  result on reconnect. Cancel and steer reach only the assigned process
- Move local to cloud and back with Git changes, queued requests and child history.
  Inject source death, destination death, lost replies and cancelled login. No
  silent duplicate provider start, lost accepted prompt or stale-worker write
- Test native resume compatibility separately from imported-context continuity
- Test archive traversal, symlinks, oversized data, secret exclusions, per-owner
  storage isolation, quota exhaustion and restoration of runner state
- Keep the existing performance/soak, two-Mac, signing and release gates. Provision
  the Hetzner host jointly at the final milestone; this scope buys no hardware

## Provider references

- [Codex authentication](https://learn.chatgpt.com/docs/auth): headless device login,
  per-home credential storage and automatic renewal during use
- [Codex CLI reference](https://learn.chatgpt.com/docs/developer-commands?surface=cli):
  saved-session resume; this alone does not prove cross-machine portability
- [Claude authentication](https://code.claude.com/docs/en/authentication): multiple
  profiles, browser/code login and the separately expiring setup-token option
- [Claude CLI reference](https://code.claude.com/docs/en/cli-reference): login status,
  saved-session resume and non-interactive invocation
