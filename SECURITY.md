# Security

Please use [private vulnerability reporting](https://github.com/teslashibe/capybara-camp/security/advisories/new) for suspected vulnerabilities. Do not post credentials or private transcripts in a public issue.

Never upload provider home directories, `auth.json`, `.credentials.json`, environment files, browser cookies, private keys, or the contents of `~/Library/Application Support/Capybara Camp`.

## Release contents

Public releases contain the native executable, generated icon and mascot, three Python adapters and bundle metadata. Provider CLIs, credentials, session databases, private verification screenshots and local logs are not bundled. ZIP checksums accompany downloadable apps. An ad-hoc signature checks bundle integrity locally but does not establish a verified publisher or Apple notarization.

## Current boundaries

Camp imports readable local stores. It uses each source profile's installed CLI login when sending. Camp's chat preview disables tools, hooks and MCP servers. It rejects concurrent sends into sessions known to be owned by another provider process.

Imported content is untrusted. It is displayed as text, not rendered as active HTML. A cancelled or uncertain provider action is not silently retried. Queued prompts remain paused after restart.

Only the latest published version is a candidate for fixes. This early release has not received an independent security audit.
