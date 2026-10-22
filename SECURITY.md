# Security

Please use [private vulnerability reporting](https://github.com/teslashibe/capybara-camp/security/advisories/new) for suspected vulnerabilities. Do not post credentials or private transcripts in a public issue.

Never upload provider home directories, `auth.json`, `.credentials.json`, environment files, browser cookies, private keys, or the contents of `~/Library/Application Support/Capybara Camp`.

## Release contents

The published v0.0.1 app contains the native executable, generated icon and
mascot, Python session adapters and bundle metadata. Current V1 source builds
replace those adapters with a bundled Go helper and contain no Python runtime
files. Provider CLIs, credentials, session databases, private verification
screenshots and local logs are not bundled. ZIP checksums accompany downloadable
apps. An ad-hoc signature checks bundle integrity locally but does not establish
a verified publisher or Apple notarization. V1 release signing and notarization
remain open in [the release procedure](docs/releasing.md).

## Current boundaries

Camp imports readable local stores. It uses each source profile's installed CLI
login when sending. Chat preview disables tools, hooks and MCP servers. V1
development allows explicitly reviewed project permissions for local sessions
and implementation. It rejects concurrent sends into sessions known to be owned
by another provider process.

Imported content is untrusted. It is displayed as text, not rendered as active HTML. A cancelled or uncertain provider action is not silently retried. Queued prompts remain paused after restart.

V1 development builds support explicit session sharing and owner-only cloud
copies. Camp device tokens are
bound to the configured server origin, stored in the Keychain and never sent
through redirects. Local provider credentials stay with their CLIs. Separate
cloud sign-in creates owner-bound credentials encrypted at rest by the service;
other Camp members cannot read them. A private cloud copy does not grant shared
access. The local
cache separates users, Camps and origins; revocation removes the relevant cached
transcripts and fences delayed writes. A new login must explicitly adopt an
existing session before executing it on a different device. The owner resolves
active or uncertain work on the previous device first.

Cloud assignments bind a provider installation, Git workspace and execution
epoch. The Linux runner requires its process isolation boundary before provider
or Git children start. Target-cluster isolation, real provider execution and the
complete security audit remain release gates; local synthetic tests do not
establish production security. See [process isolation](docs/process-isolation.md)
and [V1 delivery status](docs/v1-status.md).

The loopback test API is compiled only into a Go test binary. Its mail sink
accepts only synthetic example.com addresses. The `--development` native build
permits loopback HTTP and optional in-memory test credentials; release builds do
not enable those options.

Only the latest published version is a candidate for fixes. This early release has not received an independent security audit.
