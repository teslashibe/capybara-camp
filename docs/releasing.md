# Release procedure

The next planned release is v1.0.0. It is not ready to publish. Keep the complete
scope and acceptance evidence in [V1 delivery status](v1-status.md) and the
[release epic](https://github.com/teslashibe/capybara-camp/issues/1). A successful
build alone does not satisfy those gates.

## Before the release candidate

- Complete the scoped features and resolve the final native design differences.
- Finish live provider, two-user/two-Mac and cloud acceptance, including
  disconnected-Mac execution, isolation, recovery and backup restore.
- Complete performance and leak/soak validation, then two consecutive full code
  reviews with no unresolved in-scope defects.
- Audit tracked source, history and candidate artifacts for secrets. Exclude
  local profiles, credentials, diagnostic output and synthetic test helpers
  from the distributed app and website.
- Complete the dependency/license audit and resolve the source license before
  presenting the repository as licensed open source.

## Documentation and version

Update `VERSION`, the README, operator guides and website from the behavior of
the final candidate. The README must cover installation, automatic profile
discovery, onboarding, everyday use, account/Git connections, collaboration,
cloud setup, data storage, troubleshooting and development commands. Recheck
every documented command and local link.

Write `releases/v1.0.0.md` with the shipped features, supported requirements,
upgrade/deployment steps, validation evidence and any actual limitations. The
GitHub release must use those committed notes. Keep credentials and personal
transcripts out of examples and recordings.

Commit the final documentation with the candidate. A change after acceptance
requires the relevant checks again and an updated candidate commit.

## Build and signing

Keep the stable app identifier `com.capybaracamp.app` and the chosen signing
identity. Build universal arm64/x86_64 binaries for both the app and bundled Go
helper. Verify version metadata, required architectures and the absence of
test-only binaries or Python runtime files.

Normal `build.sh` invocations default to ad-hoc signing. `./build.sh --release`
builds both architectures, requires the expected signing team, and signs the
helper and app with hardened runtime and secure timestamps. It verifies the
Developer ID Application certificate chain and both architecture signatures.
It rejects another bundle identifier and unexpected files inside the bundle.

Use an installed Developer ID Application certificate. Keep its private key
outside the repository. Store Apple's notarization credentials with the
interactive command below; never put a password or API key in a command file,
environment variable, release log or GitHub artifact.
Install full Xcode for `notarytool` and `stapler` before running this release path.

```sh
xcrun notarytool store-credentials CapybaraCamp
export CAMP_SIGN_IDENTITY='Developer ID Application: your installed certificate'
export CAMP_SIGN_TEAM_ID='YOURTEAMID'
export CAMP_NOTARY_PROFILE='CapybaraCamp'
./tools/package-release.sh --release
```

Replace the certificate label and ten-character team ID with the release
identity. Keep that team and `com.capybaracamp.app` stable across releases.
This command requires a clean Git checkout and committed notes for `VERSION`.
It builds the candidate, retains a hashed upload under `build/release-*`, and
submits it once to Apple. It does not wait in a polling loop. If Apple is still
processing, it prints a resume command:

```sh
./tools/package-release.sh --resume SUBMISSION_ID
```

Resume uses the retained archive and source commit. It checks the submission
ID, upload name, source state, expected team and original archive hash. If the
upload result is uncertain, inspect the history and the private staging
directory to find its submission ID before resuming:

```sh
xcrun notarytool history --keychain-profile CapybaraCamp
xcrun notarytool log SUBMISSION_ID --keychain-profile CapybaraCamp
```

Do not repeat `--release` blindly. A rejected submission produces no public
archive. Inspect its log before preparing a corrected candidate. If preparation
failed before any upload was attempted, review and remove only that incomplete
`build/release-*` directory before retrying the build.

Once Apple accepts, the script extracts the original upload, staples and
validates the ticket, and checks Gatekeeper. It then creates the final ZIP,
extracts that ZIP into a fresh directory and repeats verification. Only after
those checks pass does it publish the local artifact directory `dist/vVERSION/`:

- Universal app ZIP
- `Capybara-Camp-Getting-Started.html`, from the committed website source
- `RELEASE-NOTES.md`, from the committed versioned notes
- `BUILD-INFO.txt`, with source commit, signing team, toolchain versions and
  notarization submission ID
- `SHA256SUMS`, computed from those exact four files

Existing artifact directories are never overwritten. The private staging
directory and Apple's response files remain under ignored `build/`; they are
not release assets. Packaging does not push Git, create a tag, publish a GitHub
release or deploy the website. Complete the steps below after testing the
extracted app with computer use. Automated signature checks cannot verify its
actual UI, provider or cloud behavior.
Concurrent packaging of the same version is blocked by a local lock. After a
hard crash, check the PID in `build/release-VERSION.lock/pid` and ensure that
process has ended before removing only that lock directory and resuming.

To recheck an extracted candidate on another Mac with the source checkout:

```sh
CAMP_SIGN_TEAM_ID='YOURTEAMID' ./tools/verify-macos-release.sh \
  '/path/to/Capybara Camp.app' --notarized
```

For a local package without notarization, use
`./tools/package-release.sh --development`. It writes a separate
`dist/development-VERSION/` directory and puts `DEVELOPMENT` in the archive
name. Never upload that file as the public release.

The workflow follows Apple's [notarization guidance](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)
and [Developer ID certificate requirements](https://developer.apple.com/documentation/technotes/tn3127-inside-code-signing-requirements).
Stable signing identifies the publisher; it does not guarantee identical bytes
or hashes across builds.

## Push, tag and publish

Publish only the final tested commit. Do not move an existing release tag to a
different commit or overwrite a published artifact.

1. Push the finished branch and verify that its remote head is the accepted
   commit. Check the GitHub workflows for that commit.
2. Create the `v1.0.0` tag on that commit, push the tag and verify its remote
   target. Confirm that `VERSION` and the app metadata both say `1.0.0`.
3. Publish a GitHub release from that tag, using the committed notes and the
   final universal archive, setup documentation and `SHA256SUMS`.
4. Read the release back from GitHub. Download the published files, verify their
   checksums, inspect the extracted app's signature and notarization, and smoke
   test the downloaded app on the supported architectures.

Use `gh` for release creation and inspection. The final creation command has
this form once the tag and artifacts exist:

```sh
gh release create v1.0.0 --verify-tag \
  --repo teslashibe/capybara-camp \
  --title "Capybara Camp v1.0.0" \
  --notes-file releases/v1.0.0.md \
  dist/v1.0.0/Capybara-Camp-1.0.0-macos-universal.zip \
  dist/v1.0.0/Capybara-Camp-Getting-Started.html \
  dist/v1.0.0/RELEASE-NOTES.md \
  dist/v1.0.0/BUILD-INFO.txt \
  dist/v1.0.0/SHA256SUMS
```

If a publish request has an uncertain result, inspect the existing release and
assets before retrying. A local tag or successful upload command is not proof
that users can download the published release.

## Website and recorded demo

Record the actual native release candidate through computer use, with synthetic
accounts, repositories and transcripts. Show onboarding, board/chat, planning,
implementation, collaboration and the other shipped features. Verify that no
personal data, tokens, account codes or credentials appear in any frame.

Update `docs/index.html` with the shipped features, accurate setup instructions
and links to the published release. Include the recorded demo with playback
controls, optional looping and reduced-motion behavior. Package only the page
and explicitly required public media assets for Cloudflare Pages. Do not upload
a working directory or build output wholesale.

Publish the website through its established Cloudflare Direct Upload project.
A GitHub push does not deploy that project. Check the live domain, desktop and
narrow layouts, demo playback, reduced-motion behavior and every download link.
Verify that the public release, README, notes and website describe the same
version. Record the release URL and deployed website result in the delivery
status before marking the goal complete.
