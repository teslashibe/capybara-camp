# First-run setup

V1 development adds a four-step native setup sheet. It opens for a new local
installation. Existing installations keep their board; anyone can reopen setup
from **Capybara Camp → Set up Capybara Camp**.

1. **Sessions** shows the profiles found by the normal background import, their
   paths and session counts. Add profile folder uses the existing directory
   picker and Go importer. Read failures stay visible; zero profiles does not
   block access to the app. Sign in through the provider CLI first. A profile
   folder is not a verified account identity.
2. **Camp** is optional. Enable Connect a shared Camp, enter the server and email,
   verify the email code, then create, join or select a Camp. These are the same
   controls and operations as Camp settings. A failed connection stays on the
   sheet with an error and can be corrected or skipped. No sessions are shared
   automatically. Connecting a Camp does not provision cloud execution.
3. **Project** starts in Inbox, selects an existing local project or creates one.
   It uses the existing project fields, validation and SQLite store. Repository
   links are optional and do not grant Git access. Creation saves before moving
   forward; an invalid name, unsafe URL or failed write leaves the inputs intact.
   Shared projects remain available in Camp settings.
4. **Ready** shows the current import, connection and project state, offers
   optional desktop notifications through the existing macOS permission flow,
   and opens the board. New projects appear in the sidebar; setup keeps imported
   sessions visible and does not assign historical sessions automatically.

Back preserves local project fields while the sheet is open. Skip this step advances past
optional Camp/project setup. Finish later closes the sheet and saves its current
step and chosen local project. It does not reappear on every launch; the menu resumes it. A completed flow
can be revisited from the beginning. Relaunch restores the step, not unsaved form
text, email codes or invitation tokens. Successfully added sources and projects
remain in their normal stores.

`Sources/CampOnboarding.swift` owns the small versioned `setup.json` record in the
existing application-support directory. It contains the step, completion/deferral
flags and chosen local project ID, and is written atomically with owner-only permissions. It stores
no provider credentials or Camp authentication material. Unknown or corrupt
setup records do not interrupt an existing board; reopening setup repairs them.
The sheet adds no polling loop, background task or second import cache. The
source list is lazy, and the existing source, connection and project components
are shared with their settings screens.

## Validation

`CampOnboardingTests` covers new installation, upgrade detection, step bounds,
back, deferral/resume, completion/revisit, corrupt-record recovery, owner-only
persistence, project validation, and the absence of implicit sharing or provider
execution. Native computer-use validation covers the visible first-run flow,
folder picker cancellation, optional connection failures, project creation,
notification choice, restart and the setup menu. A real second-user cloud test
and provider account provisioning remain part of the overall V1 release gates.

Cloud provider account sign-in, Local/Cloud execution defaults, Git grants and
skill installation will extend this flow when their underlying V1 services are
implemented. They must show verified outcomes from those services; importing a
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
