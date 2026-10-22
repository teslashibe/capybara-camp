# First-run setup

V1 development includes a seven-step native setup sheet. It opens for a new local
installation. Existing installations keep their board; anyone can reopen setup
from **Capybara Camp → Set up Capybara Camp**.

1. **Sessions** automatically finds local profiles before reading their session
   histories. The Go discovery pass checks `.codex*` and `.claude*` folders in
   accessible Mac home directories, configured provider homes and previously
   saved custom locations. No folder selection is required for those profiles.
   A collapsed **Missing a profile?** section provides the directory picker for
   unusual locations. Read failures stay visible; zero profiles does not
   block access to the app. Sign in through the provider CLI first. A profile
   folder is not a verified account identity.
2. **Camp** is optional. Enable Connect a shared Camp, enter the server and email,
   verify the email code, then create, join or select a Camp. These are the same
   controls and operations as Camp settings. A failed connection stays on the
   sheet with an error and can be corrected or skipped. No sessions are shared
   automatically. Connecting a Camp does not provision cloud execution.
3. **Accounts** optionally adds separate cloud Codex and Claude accounts. It uses
   the same form as Manage sources, including provider sign-in, code entry,
   cancellation and reconnect. The server must enable cloud accounts and prepare
   an isolated runner. Until then, the form shows unavailable or waiting state.
   Local profiles stay independent; the form never copies their credentials.
4. **GitHub** optionally connects the person's GitHub App authorization. It uses
   the same screen as Manage sources, with cancel/reconnect, signed-in identity
   and installation/repository browsing. A missing server configuration is
   visible and can be skipped. Connecting does not clone or push a repository.
5. **Project** starts in Inbox, selects an existing local project or creates one.
   It uses the existing project fields, validation and SQLite store. Repository
   links are optional and do not grant Git access. Creation saves before moving
   forward; an invalid name, unsafe URL or failed write leaves the inputs intact.
   Shared projects remain available in Camp settings.
6. **Defaults** chooses Local or Cloud for new plans through the shared execution
   preferences form. Cloud requires a connected Camp; skipping leaves the
   existing preference in place. This step does not move imported sessions.
7. **Ready** shows the current import, connection and project state, offers
   optional desktop notifications through the existing macOS permission flow,
   and opens the board. New projects appear in the sidebar; setup keeps imported
   sessions visible and does not assign historical sessions automatically.

Back preserves local project fields while the sheet is open. Skip this step advances past
optional Camp/account/GitHub/project/defaults setup. Finish later closes the sheet and saves its current
step and chosen local project. It does not reappear on every launch; the menu resumes it. A completed flow
can be revisited from the beginning. Relaunch restores the step, not unsaved form
text, email codes or invitation tokens. Successfully added sources and projects
remain in their normal stores.

`Sources/CampOnboarding.swift` owns the small versioned `setup.json` record in the
existing application-support directory. It contains the step, completion/deferral
flags and chosen local project ID, and is written atomically with owner-only permissions. It stores
no provider credentials or Camp authentication material. Unknown or corrupt
setup records do not interrupt an existing board; reopening setup repairs them.
The source list is lazy, and the existing source, connection, account and project
components are shared with their settings screens. Cloud account status polls
only while its form is visible. Closing or skipping it stops that view task; a
server-side sign-in attempt remains cancellable and expires after ten minutes.
The account setup journal stores only request IDs, account names and selections,
scoped to the signed-in owner and server. Return codes and provider credentials
never enter it. Existing saved step values retain their meaning after insertion
of the account step.

The initial discovery pass reads directory/store markers rather than transcript
contents. The UI shows these profiles while the full import runs and preserves
previously cached session counts. A profile is a local store; several account
identities can have history in one store. The verified account connection work
must not equate the number of folders with the number of provider logins.

## Validation

`CampOnboardingTests` covers new installation, upgrade detection, step bounds,
back, deferral/resume, completion/revisit, corrupt-record recovery, owner-only
persistence, project validation, and the absence of implicit sharing or provider
execution. Native computer-use validation covers the visible first-run flow,
folder picker cancellation, optional connection failures, project creation,
notification choice, restart and the setup menu. A real second-user cloud test
and provider account provisioning remain part of the overall V1 release gates.

Repository execution grants and skill installation will extend
this flow when their underlying V1 services are implemented. They must show
verified outcomes from those services; importing a
folder or completing the sheet must never fabricate a connected account.

### October 21 local verification

The existing Go race checks, 102 adapter tests and all 15 native suites passed.
The final onboarding suite passed again after the walkthrough fixes, and the
arm64 development app and its bundled Go helper passed signature verification.

Computer use verified an empty first launch, folder-picker cancellation,
automatic profile appearance after import, invalid-server and wrong-code
recovery, email sign-in and Camp creation against the local Go/PostgreSQL test
server, project validation/creation, Back, Skip, Finish later and the menu's
resume action. Restarting before the first click kept the first-run sheet.
The final board kept the imported card visible after project creation. The
walkthrough used synthetic profiles and accounts; no provider prompt was sent.
Existing notification permission/denial behavior passed its native suite; the
walkthrough kept desktop notifications off.

The follow-up automatic-discovery fixture finds four Codex and two Claude
profiles across separate user homes without a saved folder list, deduplicates
symlinks and reads no transcript content. Native computer use verified that the
real build shows three discovered profile stores while their histories are
still importing. No folder picker was used; the custom-path control stayed
collapsed. These three stores are not a count of verified provider accounts.

On October 22, the automatic-discovery and separate-profile/read-only-store
regressions passed again with the Go race detector. Discovery covers standard
and alternate profile folders in readable home directories, provider-home
environment settings passed to the app, and saved custom roots. It does not
search arbitrary files across the whole disk or bypass macOS folder access.

### Cloud account regression checks

The account form uses the real owner-only API. Native tests cover a lost account
creation response, relaunch while waiting for a runner, single-attempt startup,
Claude code entry, reconnect cancellation, account isolation and stale replies
after sign-out. A delayed-start test checks that Cancel waits for an in-flight
start result and then cancels that attempt. Existing native suites passed before
that fix; the account and onboarding suites passed again after it.

For computer-use QA, `tools/native-fixture.sh` runs a disposable local API and
database. Its provider worker is synthetic: Claude accepts
`demo-authorization-code`, while Codex exposes a cancellable test challenge.
Do not use its provider links to authenticate a real account. A development
build can use `CAMP_DEV_PROFILE_ROOT` to keep profile discovery inside a private
fixture directory containing `home/` and `users/`. Production helpers ignore
that variable. This permits recording the native interface without reading real
session histories. `CAMP_DEV_EPHEMERAL_CREDENTIALS=1` keeps the fixture's Camp
login out of Keychain.

Computer use verified the five-step setup flow, Camp email sign-in, separate
Claude and Codex account creation, the waiting/device-code screens, synthetic
Claude completion, reconnect cancellation preserving the connected account,
account switching and confirmed disconnect. Relaunching and signing in again
restored the selected account. The same form worked in Manage sources. A
truncated provider picker was widened and rechecked with both provider names.
This used an arm64 development build signed with the existing Apple Development
identity, not a notarized V1 release. The normal app and its profiles were not
used for the synthetic sign-in checks.

### GitHub integration checks

The GitHub and onboarding suites cover the inserted sixth step, recovery of a
lost authorization-start response, cancellation, private setup persistence and
late replies after sign-out. A native computer-use walkthrough verified the
same GitHub screen in onboarding and Manage sources, repository browsing,
read-only labels, scroll overflow, cancelling a disconnect, and confirming a
disconnect. It used a seeded local fixture and did not authorize a real GitHub
account. The final design audit and live GitHub App validation remain pending.
