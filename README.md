<p align="center"><img src="Resources/capybara.png" width="180" alt="A capybara sitting beside a green tent"></p>

# Capybara Camp

Your Camp for big ideas

A small native Mac app for your local Codex and Claude Code sessions. Find a conversation, put it on a Kanban board, and pick it up in chat.

[Get started](https://capybaracamp.com/) · [Download v0.0.1](https://github.com/teslashibe/capybara-camp/releases/tag/v0.0.1) · [Release notes](releases/v0.0.1.md)

## What works

- Import accessible local Codex and Claude Code profiles, with their sessions kept separate
- Switch between Board and Chat; search by session, project or profile
- Track live Claude Code activity and inspect saved subagent transcripts
- Resume conversations through the installed provider CLI and its existing login
- Stream Camp-started replies, steer a running turn, and queue prompts
- Choose the model, effort and supported speed for the next reply
- Discover new sessions automatically and follow open transcript changes as the CLI writes them

This is a **chat-only preview**. Camp-started sessions have tools, hooks and MCP servers disabled. A session open in another app must be closed there before Camp can send into it. External terminal conversations update when their CLI saves text; they do not expose an independent live token stream to Camp.

## Requirements

- macOS 14 or newer, on Apple silicon or Intel
- Python 3.11 or newer, available as `python3`
- [Codex CLI](https://learn.chatgpt.com/docs/cli) and/or [Claude Code](https://code.claude.com/docs/en/setup), installed and signed in
- Xcode Command Line Tools for building from source (`xcode-select --install`)

Provider subscriptions, login and model availability remain with the provider. You only need the CLI for the provider you want to use.

## Build and run

```sh
git clone https://github.com/teslashibe/capybara-camp.git
cd capybara-camp
./build.sh
open "build/Capybara Camp.app"
```

The first launch imports accessible local profiles automatically. Use **Manage sources** to add a separate `CODEX_HOME` or `CLAUDE_CONFIG_DIR` folder. A folder is a source, not proof of a particular signed-in account: its current login can differ from a historical conversation's author.

Click a card to open chat. Send with **Command–Return**. During a Camp reply, **Command–Return** steers it and **Command–Shift–Return** queues the next prompt. Queues stay paused after a restart until you resume them.

New sessions are checked every 30 seconds; large libraries can take longer to read. Open transcripts use filesystem notifications, with no polling interval. When you scroll up, new messages leave your reading position in place.

## Downloaded app

The release includes a universal macOS ZIP and a SHA-256 checksum. Extract it and move **Capybara Camp.app** to Applications. The Python and provider CLI requirements still apply.

**v0.0.1 is ad-hoc signed and is not notarized by Apple.** macOS may block a downloaded copy. Review Apple's [instructions for opening an app from an unidentified developer](https://support.apple.com/en-us/102445), or use the source build above. Camp does not change Gatekeeper settings.

## Development

```sh
./test.sh
./build.sh --universal
./tools/package-release.sh
```

Tests use local fixtures and fake CLI processes; they do not need provider accounts or API keys. The suite covers import isolation, native state, transcript notifications, streaming, cancellation, steering and queue recovery. Live compatibility has been checked on one Mac with Codex CLI 0.163.0 and Claude Code 2.1.306; provider interfaces can change.

By default, builds go into `build/` and use ad-hoc signing. Set `CAMP_SIGN_IDENTITY` for your own certificate, `CAMP_APP_PATH` for a different output bundle, or `CAMP_BUNDLE_ID` for a different identifier. The default identifier is `com.capybaracamp.app`. Repeated builds keep that identifier; a matching binary hash across different builds is not promised.

## V1 development

The `codex/capybara-v1` work adds a Go shared-Camp service with PostgreSQL,
email sign-in, membership, versioned transcripts, permission checks, a durable
prompt queue and live events. The Swift client includes a scoped SQLite cache,
an offline outbox, Camp connection controls and shared transcript viewing.
The owner-Mac worker can execute shared chat requests and isolated shared
planning steps. Local five-step plans can
continue into implementation with a separate executor choice on the same ticket.
Completion notifications include a native inbox, foreground toasts, optional
macOS alerts and server-owned read receipts across a person's devices.
See [notifications](docs/notifications.md) for the current delivery
and cross-device boundaries.
Shared planning creation and editing in the native UI, cloud runners, memory and skills are still in
development. These features are not included in v0.0.1. See [planning](docs/planning.md),
[cloud execution scope](docs/cloud-execution.md), and [self-hosting](docs/self-hosting.md)
for the current implementation boundary and [the V1 epic](https://github.com/teslashibe/capybara-camp/issues/1)
for release acceptance.

In a V1 development build, connect your Camp from the header, then choose
**Share with Camp** on a session card. Select its project and whether teammates
may request work. Requests from other people require the session owner's approval.
Stop sharing removes the other members' access and cancels queued requests.

For isolated native/Go integration tests, run `tools/native-fixture.sh` with an
absolute temporary directory, then build with `./build.sh --development`.
The fixture starts disposable PostgreSQL and a loopback API; it writes synthetic
email codes to that directory. Development builds allow a loopback HTTP origin.
`CAMP_DEV_EPHEMERAL_CREDENTIALS=1` uses in-memory Camp credentials in those builds.
Normal builds require HTTPS and use the Keychain.

## Website

The public website is hosted on Cloudflare Pages at [capybaracamp.com](https://capybaracamp.com), using the `capybara-camp` Direct Upload project. Its source is `docs/index.html`; a Cloudflare Redirect Rule sends the `www` address to the main domain.

To publish a website update, upload a ZIP containing only `index.html` at the archive root. Do not upload the repository or a build directory: those can include local diagnostics. Direct Upload is separate from GitHub pushes. The GitHub Pages build remains a preview mirror.

## Local data and privacy

Camp stores source paths, cached metadata, board positions, model settings and queued prompts under `~/Library/Application Support/Capybara Camp`. `CAPY_SUPPORT` can select a separate absolute directory for local testing. Provider credentials stay in the provider's own storage. Import reads are local; sending a prompt uses the selected CLI and updates that provider's session store.

Cloud-only conversations and unreadable files belonging to other macOS users are not imported. v0.0.1 does not upload transcripts to a Camp service. V1 development builds upload only sessions explicitly shared to the configured Camp server. Do not attach your profile directories, application-support folder or unredacted transcripts to an issue. See [SECURITY.md](SECURITY.md).

## Scope of the first release

Verified multi-account login management, a usage dashboard, automatic credential rotation, limit-triggered account handoff, provider-native dictation and external Codex runtime monitoring are **not implemented**. Saved Codex subagent state is not proof that a subagent is currently running. This release is a local MVP, not a full coding-agent host.

## Source layout

- `Sources/CapybaraCamp.swift` — SwiftUI app and native state
- `Sources/CampConnection.swift`, `CampHTTP.swift`, `CampCache.swift` — native Camp connection, event stream and durable local cache
- `Sources/CampCredentials.swift` — origin-bound native Camp device credentials
- `server/` — Go API, PostgreSQL migrations and permission/lifecycle tests
- `deploy/` — public Kubernetes and Hetzner K3s templates
- `Resources/bridge.py` — local imports and transcript readers
- `Resources/activity.py` — activity, subagents and model capabilities
- `Resources/conversation.py` — native CLI connections, streaming and steering
- `Tests/` — fixture-only tests
- `docs/index.html` — self-contained setup page with the generated mascot
- `releases/` — versioned release notes

The capybara mascot was generated with an image-generation tool; its brief is in `Resources/logo-prompt.txt`. Capybara Camp is independent of OpenAI and Anthropic.

## License

A source license has not been selected yet. Publishing this repository does not grant a general license to redistribute it.
