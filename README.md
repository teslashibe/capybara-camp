<p align="center"><img src="Resources/capybara.png" width="180" alt="A capybara sitting beside a green tent"></p>

# Capybara Camp

Your Camp for big ideas

A native Mac app for your Codex and Claude Code sessions. Find a conversation,
put it on a Kanban board, and pick it up in chat.

[Get started](https://capybaracamp.com/) · [Download v0.0.1](https://github.com/teslashibe/capybara-camp/releases/tag/v0.0.1) · [Release notes](releases/v0.0.1.md)

## Release status

**v0.0.1 is the published chat-only preview. V1 is in development and is not
released.** The source branch includes features that the v0.0.1 download does
not have. The [V1 delivery status](docs/v1-status.md) records what is implemented,
tested and still required; the [release epic](https://github.com/teslashibe/capybara-camp/issues/1)
defines the full release scope.

## Local sessions

- Import accessible local Codex and Claude Code profiles, with their sessions kept separate
- Switch between Board and Chat; search by session, project or profile
- Track live Claude Code activity and inspect saved subagent transcripts
- Resume conversations through the installed provider CLI and its existing login
- Stream Camp-started replies, steer a running turn, and queue prompts
- Choose the model, effort and supported speed for the next reply
- Discover new sessions automatically and follow open transcript changes as the CLI writes them

The public preview disables tools, hooks and MCP servers in Camp-started
sessions. V1 development adds explicit [session permissions](docs/session-permissions.md)
and approved plan implementation. A session owned by another app must be
released there before Camp can send into it. External terminal conversations
update when their CLI saves text; Camp cannot display tokens that the CLI has
not written or exposed through its activity interface.

## Requirements

- macOS 14 or newer, on Apple silicon or Intel
- [Codex CLI](https://learn.chatgpt.com/docs/cli) and/or [Claude Code](https://code.claude.com/docs/en/setup), installed and signed in
- Xcode Command Line Tools for building from source (`xcode-select --install`)
- Go 1.25.7 or newer for building from source

Provider subscriptions, login and model availability remain with the provider. You only need the CLI for the provider you want to use.

## Download the public preview

The [v0.0.1 release](https://github.com/teslashibe/capybara-camp/releases/tag/v0.0.1)
includes a universal macOS ZIP and a SHA-256 checksum. Extract it and move
**Capybara Camp.app** to Applications. That older release still requires Python
3.11 and the provider CLI. Current source builds bundle the Go helper and do
not require Python to build, test or run.

v0.0.1 is ad-hoc signed and is not notarized by Apple. macOS may block a
downloaded copy. Review Apple's [instructions for opening an app from an unidentified developer](https://support.apple.com/en-us/102445),
or build from source below. Camp does not change Gatekeeper settings.

## Build from source

Install the requirements above, then:

```sh
git clone https://github.com/teslashibe/capybara-camp.git
cd capybara-camp
./build.sh
open "build/Capybara Camp.app"
```

This builds the checked-out source for your Mac's architecture. Go is needed
on the build machine only; the app includes its local helper. For both Apple
silicon and Intel, use `./build.sh --universal`.

The current unreleased V1 work is on `codex/capybara-v1`. To try that work,
switch to that branch before building. The default branch and published
download may be behind the development branch.

The [six-step onboarding flow](docs/onboarding.md) discovers local profiles,
then offers optional Camp sign-in, cloud provider accounts, GitHub, a project
and notifications. Each optional step can be skipped. **Finish later** saves
your place; **Capybara Camp → Set up Capybara Camp** reopens setup.

Profile discovery does not require choosing folders. Use **Manage sources**
to add an unusual `CODEX_HOME` or `CLAUDE_CONFIG_DIR` location if needed.
A profile folder can contain several historical account identities; it does
not prove that each of those accounts is currently signed in.

## Working in the app

### Board and chat

Click a card to open chat. Send with **Command–Return**. During a local Camp
reply, **Command–Return** steers it and **Command–Shift–Return** queues the next
prompt. Local queues stay paused after a restart until you resume them.

New sessions are checked every 30 seconds; large libraries can take longer to
read. Open transcripts use filesystem notifications. Chats open at the bottom
and follow incoming output. The main chat and its sub-agents appear above the
ten most recent sessions; use `@` to find older context. A saved sub-agent
transcript alone is not evidence that it is currently running.

Model, effort and supported speed controls apply to the next Camp turn.
Running activity uses settings reported by the provider when available. An
external session remains under its original client's control. The native chat
editor accepts macOS Dictation as live text.

### Plans and projects

In V1 development, **Plan** uses one model. **Plan with comparison** uses two
independent drafts, one review from each provider and a combined plan. Choose
profiles and model settings for the planners, attach session context with `@`,
and keep the planning card running in the background. Start over preserves the
previous version. Local plans continue into implementation on the same ticket
after you select an executor and approve its permissions.

[Projects](docs/projects.md) group tickets for a client or body of work across
repositories. Assigning a project does not move a working directory or grant
Git access. [Planning](docs/planning.md) documents budgets, cancellation,
history and the remaining shared implementation limits.

Shared planning records each planner's Mac or Cloud assignment. A moved session
requires review before Start. Once queued, a plan keeps those assignments; a
changed or unavailable destination stops the next phase and preserves completed
drafts. Start over creates a new revision with reviewed locations.
In a shared draft, **Run on** below each planner's model settings selects its
Local or Cloud source separately. Choose a prepared cloud workspace or a
session on this Mac, then apply the selection. Other planner settings and
attached context stay unchanged. Existing standalone local plans still need
the native import flow to move into the shared coordinator. The development
Go API can now preserve their saved planning versions as private history,
without queuing provider work. The native reader displays imported sources,
saved errors and unconfirmed outcomes. The native review/upload flow and preservation
of existing implementation conversations in the native flow remain unfinished; see
[local-plan import](docs/planning.md#local-plan-import-api).
Start over retains quoted context in the new draft. Removing a source there
does not alter the saved planning history.
The development API can attach copied implementation transcripts and saved
attempt records to their original plan versions. Its paged reads preserve the
copied revision and current permissions; the native controls are still pending.

### Default execution settings

Open **Manage sources → Defaults**, or use the Defaults step during setup.
Choose Local or Cloud, an optional provider for single-model plans, and a
profile or account for each provider. Automatic provider selection uses the
selected session when available, then an available provider. A Cloud default
also needs a shared Camp project. These preferences are saved on this Mac,
separately for each person, Camp and server.

Prepare at least one session workspace in that project through **Run on**.
New cloud plans use the most recently updated eligible source for the selected
account and open a private draft for review. They do not start provider work
until you choose Start. If the account, profile or workspace is unavailable,
Camp asks you to fix that selection. Existing tickets keep their assignments.
**Plan on this Mac** explicitly overrides the default location for a new plan.

### Permissions and notifications

Open **Permissions** from a card's menu or the shield in chat. Imported sessions
start in chat preview mode. Enabling project tools requires an explicit choice;
the installed CLI enforces its available policies. A change applies to the next
turn, and a queued prompt with outdated permissions needs review. Approval
requests appear in chat and move the card to **Needs you**.

The notification bell opens saved completions and unread items. Foreground
completions show a toast. Desktop notifications are opt-in; title previews and
sounds are separately optional. See [permissions](docs/session-permissions.md)
and [notifications](docs/notifications.md) for the exact behavior.

## Shared Camps and cloud execution

These features are in the V1 source and still require the live acceptance work
in [delivery status](docs/v1-status.md). No live cloud provider turn or production
cloud deployment has been accepted for this release.

Every collaborating Mac connects to the same HTTPS Camp service. Each person
signs in with their own email, then creates or joins a Camp. On a session card,
**Share with Camp** selects its project and whether teammates may request work.
Another person's request requires the session owner's approval. Stopping
sharing removes teammates' access and cancels queued requests. Private local
imports are not automatically uploaded.

The cloud service is Go with PostgreSQL. Native clients keep a scoped SQLite
cache and a durable offline outbox. The owner-Mac worker runs approved local
requests; a separately provisioned cloud installation runs assigned cloud
requests. The service retains transcripts and ordered events so clients can
reconnect. Independent Camp deployments do not synchronize with each other.

### Connect provider accounts and GitHub

1. Connect the app to your Camp service.
2. In **Manage sources → Cloud accounts**, add each Codex or Claude account
   separately. Complete that account's official provider sign-in after its
   isolated runner becomes ready. Local profile credentials are not copied.
3. Connect your own GitHub identity and select an available installation.
4. Link a repository to the Camp project. A project link alone does not grant
   repository access.

Cloud accounts belong to their owner. Other Camp members do not receive their
credentials. Account identity comes from successful provider verification,
not its label. Missing server configuration or an unavailable runner appears
as a visible setup state. Reconnect uses the same verified identity; provider
login expiry and usage limits remain under the provider's control.

### Move a ticket to Cloud

Open **Run on** from the card or chat. Review the private transcript copy,
provider account, GitHub identity, project repository and pinned revision.
For a local session, open **Run on → Cloud**, choose the destination provider
account and GitHub repository, then choose **Review move**. Select the working
files to include, inspect the captured contents and exclusions, and choose
**Prepare Cloud**. The review pins the current local Git commit. That commit
must already be available in the selected remote repository.

Review waits for the current main and subagent histories to finish syncing.
The capture binds their content revisions to the imported cloud revision and
shows the subagent count. The source checks that same history before the final
move, so an edit after review requires a fresh copy and review.

Once the destination is ready, choose **Verify source** on the original Mac.
Camp checks that the provider has stopped and the selected files still match.
Review the destination permissions, then choose **Move to cloud**. The ticket
keeps its main conversation and saved child transcripts as imported context.
The cloud agent can read both through the Camp context tool. File attachments,
unpushed commits and full migration acceptance still need the work listed in
the delivery status.
Copying a transcript privately does not share it with teammates.

Cloud assignment stops the local publisher from overwriting the cloud session.
Returning an unused checkout to Local preserves the local draft and project.
A used checkout requires the reviewed transfer feature, which is still pending;
the current app blocks that move. The local runner implementation retains a
separate checkout and provider history for each ticket and schedules work
serially per account. [Cloud controls](docs/cloud-controls.md) use the same
steer, queue and stop actions as local chat, with durable delivery tracking.
The runner can read its complete stored conversation through the context tool
in bounded pages, including long messages. History remains pinned to the queued
prompt and each page checks the current request and permissions. The agent uses
`list_subagents` to find attached child histories, then `read_session` to read
only the ones it needs. The child catalog and history use the same pinned parent
revision. Saved child histories are also readable separately in the native
sidebar; file attachments still need migration support.
Default settings, the prepared-workspace requirement and unfinished transfer
behavior are documented in [execution locations](docs/execution-locations.md)
and the delivery status.

The Go API includes encrypted, resumable uploads for reviewed file-transfer
manifests. An owner-authorized Cloud destination job connects the upload to the
runner, stages files in a private checkout and validates its receipt. These are
parts of the transfer flow: an upload alone does not move
the session or apply files. The source cloud runner can now verify an idle
provider and the reviewed files under a temporary session hold. It retries lost
acknowledgements without restarting the provider. The Mac worker also holds the
local conversation lock while the bundled Go helper checks its source files.
The owner can request the final move with explicit destination permissions.
The source then checks the files again before the server changes the execution
epoch once. During the source hold, the Mac waits for saved, visible routing
before releasing its lock. The cloud worker reconciles a lost completion
response from its journal.
The server retains the committed receipt and removes the temporary upload chunks.
The native publisher includes saved subagent histories with a shared or private
Camp copy. Child transcripts stay under the parent's permissions and have their
own message IDs and paged history. The sidebar refreshes after child updates,
and a child can be opened without mixing its messages into the main chat.
Model citations open the specific child message in the native snapshot
inspector, with a link back to that child conversation. Previously opened child
catalogs and pages are saved in a bounded offline cache. An **Offline copy**
label identifies a saved transcript; access changes and removals invalidate
it when observed. Uncached history and exact citation inspection require a
connection. See [offline child history](docs/offline-child-history.md).

The native review stores its selected payload separately from its small recovery
journal. Interrupted uploads reuse their saved command keys and missing chunks.
A rejected operation pauses for explicit retry or cancellation. Closing the
dialog keeps an active transfer running; cancellation is saved before the app
sends it to the server. These controls still require the final live-provider and
two-Mac acceptance tests, alongside child-session, attachment and cloud-to-local
migration.
The bundled helper now blocks the original local session across a hold timeout
or app restart. Confirmed cancellation restores local execution; a completed
move keeps the old source blocked. After sign-out or cache erasure, signing in
to the same Camp recovers saved blockers from the local helper and checks them
against the owner's server records. Confirmed cloud assignments return to the
app; confirmed cancellations release the original local session. Complete
migration acceptance remains unfinished.
See the [transfer contract](docs/transfer-payload.md) for ownership, limits,
recovery and the remaining handoff work.

For operators, start with [self-hosting](docs/self-hosting.md), then the
[runner provisioning guide](docs/runner-provisioning.md). The repository includes
public Kubernetes and Hetzner K3s templates. Deploy the base Camp service and
enable the isolated runner overlay only with its required storage, credentials
and Linux isolation support. Hardware purchase and live deployment remain
separate acceptance steps.

## Data and privacy

Camp stores source paths, cached metadata, board positions, model settings and
queued prompts under `~/Library/Application Support/Capybara Camp`.
`CAPY_SUPPORT` selects a separate absolute directory for testing. Provider
credentials used locally stay in the provider's own storage. Import reads are
local; sending a prompt uses the selected CLI and updates that provider's store.

Cloud-only conversations and unreadable files belonging to other macOS users
are not imported. v0.0.1 does not upload transcripts. V1 uploads a session only
after explicit sharing or an explicit private cloud copy. Private copies remain
owner-only. Cloud provider sign-in creates separate encrypted credentials on
the configured service; it does not upload a local credential directory.

Normal builds use HTTPS and store Camp device credentials in the macOS
Keychain. Never attach profile directories, the application-support folder,
provider credentials or unredacted transcripts to an issue. Report sensitive
findings through the process in [SECURITY.md](SECURITY.md).

## Development

```sh
./test.sh
./tools/test-server.sh
./tools/test-processguard.sh
./tools/test-release.sh
./build.sh --universal
```

`test.sh` runs local Go protocol tests and the native state suites with synthetic
provider processes. Its bridge integration suite packages a disposable Mac app
with the real Go helper and exercises recovery through the production command
dispatcher. Its support directory is isolated from the user's sessions.
`tools/test-server.sh` requires Docker and starts disposable
PostgreSQL for Go race tests and vet; it removes the container on exit. An explicit
`CAMP_TEST_DATABASE_URL` uses isolated schemas in that test database instead.
Never point it at production. `tools/test-processguard.sh` exercises the required
Linux process boundary; on macOS it uses Docker.
`tools/test-release.sh` checks notarization recovery and packaging with synthetic
Apple tools. It needs no certificate, Apple login or provider account.

These tests do not require real provider accounts or API keys. They cover import
isolation, transcript notifications, streaming, cancellation, steering, queue
recovery, auth and durable server state. They do not replace live provider,
two-Mac, deployment or UI acceptance. Current evidence and CLI versions are
recorded in the relevant guides and [UI audit](docs/ui-audit.md).

The build and test suite use Swift and Go. `test.sh` builds a separate Go fixture executable for native protocol tests; that executable is never included in the app. The [migration record](docs/python-test-migration.json) maps the 103 former Python tests to their replacements.

By default, builds go into `build/` and use ad-hoc signing. Set `CAMP_SIGN_IDENTITY` for your own certificate, `CAMP_APP_PATH` for a different output bundle, or `CAMP_BUNDLE_ID` for a different identifier. The default identifier is `com.capybaracamp.app`. Repeated builds keep that identifier; a matching binary hash across different builds is not promised.

### Isolated native QA

For isolated native/Go integration tests, run `tools/native-fixture.sh` with an
absolute temporary directory, then build with `./build.sh --development`.
The fixture starts disposable PostgreSQL and a loopback API; it writes synthetic
email codes to that directory. Development builds allow a loopback HTTP origin.
`CAMP_DEV_EPHEMERAL_CREDENTIALS=1` uses in-memory Camp credentials in those builds.
Normal builds require HTTPS and use the Keychain.
An explicit `CAMP_TEST_DATABASE_URL` also works with the native fixture. Use a
dedicated disposable PostgreSQL 17 database; the fixture creates an isolated
schema and removes it on exit. It must never point at production.

### Troubleshooting

- A profile is missing: finish provider CLI sign-in, check file access, then use
  **Manage sources** for a custom location. Do not copy another user's credentials.
- A session cannot send: release it from its original client. Camp does not take
  over another client's active session.
- A response is delayed: Camp-started output streams directly; external output
  depends on what the original CLI writes. Check its activity and login state.
- Cloud setup is unavailable or waiting: check service configuration, account
  provisioning and runner readiness in the operator guides.
- A request has an unknown outcome: review it before starting again. A lost
  connection is not proof that the provider did no work.
- Desktop alerts are missing: enable them in Camp and macOS settings. The saved
  notification inbox remains available even when the system suppresses an alert.

## Documentation

| Guide | Covers |
| --- | --- |
| [Onboarding](docs/onboarding.md) | Automatic discovery, optional connections and resume |
| [Planning](docs/planning.md) | Single/comparison plans, budgets and implementation history |
| [Projects](docs/projects.md) | Private/shared projects and repository links |
| [Session context](docs/session-context.md) | `@` references, message ranges and source permissions |
| [Session permissions](docs/session-permissions.md) | Provider policies, queues and approval changes |
| [Notifications](docs/notifications.md) | Toasts, inbox, desktop alerts and cross-device state |
| [Cloud accounts](docs/cloud-accounts.md) | Account ownership, login and credential lifecycle |
| [GitHub](docs/github.md) | GitHub App setup, repository authorization and checkouts |
| [Execution locations](docs/execution-locations.md) | Run on Local/Cloud and transfer limits |
| [Transfer payload](docs/transfer-payload.md) | Capture format, resumable upload API, encryption, limits and remaining handoff work |
| [Cloud provider turns](docs/runner-turns.md) | Streaming, recovery and pending live acceptance |
| [Cloud chat controls](docs/cloud-controls.md) | Steering, queues, approvals and delivery recovery |
| [Self-hosting](docs/self-hosting.md) | Kubernetes/K3s configuration, upgrades and backups |
| [Runner provisioning](docs/runner-provisioning.md) | Isolated workers, grants and lifecycle |
| [Process isolation](docs/process-isolation.md) | Linux security boundary and runtime checks |
| [Server API](server/README.md) | Go service, authentication and durable request contract |
| [Go local helper](docs/go-local-helper.md) | Local runtime, bounded imports and migration evidence |
| [UI audit](docs/ui-audit.md) | Design comparison and native test evidence |
| [V1 delivery status](docs/v1-status.md) | Implemented work and open release gates |
| [Release procedure](docs/releasing.md) | Signing, packaging, GitHub and website verification |

## Website

The public website is hosted on Cloudflare Pages at [capybaracamp.com](https://capybaracamp.com), using the `capybara-camp` Direct Upload project. Its source is `docs/index.html`; a Cloudflare Redirect Rule sends the `www` address to the main domain.

The current page is self-contained. Its upload ZIP contains only `index.html`
at the archive root. Do not upload the repository or a build directory, which
can include local diagnostics. The planned native demo will need explicitly
listed media assets in the website package. Cloudflare Direct Upload is separate
from GitHub pushes; GitHub Pages remains a preview mirror.

## Releases

The [release procedure](docs/releasing.md) requires the full acceptance gates,
updated README and versioned release notes, a final code push, a tag pointing to
the tested commit, and a published GitHub release with verified artifacts and
checksums. The website must show matching downloads and a recorded native demo.

`./tools/package-release.sh --development` creates an explicitly named development
archive. Public candidates use `--release` with a Developer ID Application
identity, an expected team ID and a saved notarytool credential profile. That
path checks both architectures, submits to Apple, resumes processing by
submission ID, staples the accepted ticket and verifies the extracted archive.
It writes the app, setup HTML, notes, build information and checksums under
`dist/vVERSION/`. It refuses to overwrite an existing artifact directory.

Packaging does not push, tag, publish to GitHub or deploy the website. Follow the
[release procedure](docs/releasing.md) after the full V1 acceptance work passes.
The tooling's synthetic tests are not evidence of a notarized public V1 build.

## Scope and remaining work

V1 still needs the unfinished cloud, shared implementation, Cortex memory,
shared skills, Git publication and validation work listed in the delivery
status. Automatic account rotation to evade limits, guaranteed perpetual login
and silent execution fallback are excluded. macOS Dictation is the chosen input
path; provider-native voice transcription is not included. v0.0.1 remains the
smaller local preview described in its [release notes](releases/v0.0.1.md).

## Source layout

- `Sources/CapybaraCamp.swift` — SwiftUI app and native state
- `Sources/CampConnection.swift`, `CampHTTP.swift`, `CampCache.swift` — native Camp connection, event stream and durable local cache
- `Sources/CampCredentials.swift` — origin-bound native Camp device credentials
- `server/` — Go API, PostgreSQL migrations and permission/lifecycle tests
- `server/cmd/camp-local/`, `server/internal/localcli/` — bundled Go imports, live observation, conversations, approvals and attached context; [migration status](docs/go-local-helper.md)
- `deploy/` — public Kubernetes and Hetzner K3s templates
- `server/cmd/camp-test-provider/`, `Tests/FixtureProcess.swift` — test-only native protocol fixtures
- `Tests/` — fixture-only tests
- `docs/index.html` — self-contained setup page with the generated mascot
- `releases/` — versioned release notes

The capybara mascot was generated with an image-generation tool; its brief is in `Resources/logo-prompt.txt`. Capybara Camp is independent of OpenAI and Anthropic.

## License

A source license has not been selected yet. Publishing this repository does not grant a general license to redistribute it.
