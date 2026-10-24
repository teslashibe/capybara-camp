# Client projects

V1 development builds group work in client projects. A project contains tickets and optional GitHub repository links. It is separate from a session's working directory, provider profile and Git checkout. Linking a repository here does not grant Git access or change its remote.

Use Projects in the sidebar to create, rename or archive a project. The card menu and chat header assign a ticket to a project or return it to Inbox. A project can contain imported sessions and planning tickets from different repositories, including work without a repository. Assignment preserves ticket IDs, history and running processes. Existing directory names remain visible as location hints; they do not create client projects automatically.

Private projects and assignments live in the app's SQLite cache. Signing out of a shared Camp retains them. Shared projects use the Go service and PostgreSQL, with the existing Camp membership, command receipts, revisions and event stream. Private sessions are not uploaded merely because they have a project.

Every shared Camp has an Inbox. Migration 8 adds one to existing Camps and preserves all existing project and ticket IDs. Projects can be archived without deleting their tickets or repository links; archived projects remain available in Show archived. New assignments cannot target an archived project. Existing runs and history remain usable.

Shared project edits and assignments use the existing durable outbox. Pending changes are shown in Projects. A stale revision becomes a visible conflict; review the latest project or ticket and choose the change again. The client does not silently overwrite another person's edit. The owner publisher adopts confirmed remote project assignments. Project reassignment does not change session permission revisions or cancel an active request.

## Service contract

- `POST /v1/camps/{camp}/projects` creates a project with a name and optional repository URLs
- `PUT /v1/camps/{camp}/projects/{project}` replaces its name, archive state and repository links at an expected revision
- `PATCH /v1/camps/{camp}/sessions/{session}/project` assigns an accessible session at an expected revision
- `PATCH /v1/camps/{camp}/planning/{plan}/project` assigns an accessible planning ticket at an expected revision
- An empty `project_id` on an assignment selects the Camp's Inbox

Owners and editors can edit accessible work. Viewer writes, cross-Camp targets, malformed repository URLs and stale revisions are rejected. Repository links are canonical GitHub repository URLs, capped at 50 per project. They contain no credentials or branch paths.

Project storage was introduced in schema 8. The current required schema is
defined by `SchemaVersion` in [the database package](../server/internal/database/database.go).
Run the matching migrations before starting the server. No destructive project
rollback is supplied; retain a compatible binary and database backup.

Repository authentication, isolated Git workspaces and Git actor selection belong to [the Git integration ticket](https://github.com/teslashibe/capybara-camp/issues/11). They are not implemented by storing a project link. Two-Mac and final release validation remain part of V1 acceptance.

## Validation

The PostgreSQL 17 race-test suite verifies a teammate assigning a session while its owner's leased request continues, preserved planning versions, Inbox, archive behavior, authorization and stale-revision rejection. Native tests verify a client project containing two repository-backed sessions and an unbound planning ticket, private persistence through restart and Camp sign-out, and protection against the owner publisher reverting another member's assignment.

The October 21 native walkthrough created a project with two repository links, assigned two synthetic sessions from different folders, renamed and archived the project, and reopened its retained tickets after an app restart. All 96 Python tests and 13 native suites passed. This is one-Mac validation; the two-Mac shared-project walkthrough remains part of the release gate.
