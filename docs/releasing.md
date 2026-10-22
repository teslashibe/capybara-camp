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

The current `build.sh` defaults to ad-hoc signing; `CAMP_SIGN_IDENTITY` selects
a certificate. Its current signing path and `tools/package-release.sh` are
development tooling, not a completed Developer ID/notarization pipeline.
Finish that pipeline before the public V1 candidate: sign nested executables
and the app with the required release settings, submit to Apple, staple the
accepted ticket and validate the result. Keep certificate material and
notarization credentials outside the repository.

Create the final archive only after signing and stapling. Generate SHA-256
checksums from the exact files to be published. Test extraction and launch from
the release archive, including Gatekeeper assessment. Record the source commit,
toolchain, signing identity and validation results without exposing secrets.
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
  dist/Capybara-Camp-1.0.0-macos-universal.zip \
  dist/Capybara-Camp-Getting-Started.html \
  dist/SHA256SUMS
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
