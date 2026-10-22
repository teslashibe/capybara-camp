# V1 delivery status

October 21, 2026 · [Release epic](https://github.com/teslashibe/capybara-camp/issues/1)

V1 is not released. The public app and website still describe v0.0.1. Local
development builds contain more functionality, but implementation, deployment
and acceptance are separate milestones.

| Area | Implemented locally | Still required for V1 |
| --- | --- | --- |
| Local sessions | Automatic multi-profile import, board/chat, model/effort/speed controls, sub-agents, streaming, dictation, steering and queues | Finish live provider/recovery and performance validation after the Go migration; complete the final design comparison below |
| Planning and projects | Single-model plan, provider comparison, saved versions, same-ticket local implementation, project assignment across repositories | Shared implementation and owner-routed approval completion; cross-device validation |
| Shared Camps | Go/PostgreSQL auth, membership, sessions, events, requests, native cache/outbox and owner-Mac worker | Real independent users on two Macs, remaining permission/recovery cases, production deployment |
| Onboarding | Automatic profile discovery before full import, optional Camp sign-in, owner-only cloud account setup, GitHub connections, projects, notifications, skip/resume | Default execution target; final onboarding validation after remaining services land |
| Go migration | All native runtime actions use bundled Go; Python source, native fixtures and build/test/CI requirements removed | Final live-provider and release acceptance gates below |
| Cloud execution | Owner-only accounts/installations, native account forms, scoped grants, expiring login attempts, credential encryption, private Claude code delivery, disconnect/reactivation fencing, Go sign-in adapters, auth workers with recovery journals and a Kubernetes provisioning controller | Live sandbox/storage/network validation, isolated inference runtime, local/cloud defaults and transfers, usage states, real cloud sign-in and disconnected-Mac execution |
| Git | Project repository links, local CLI project execution, owner-bound GitHub App authorization, native repository browsing, encrypted tokens and signed revocation | Isolated cloud workspaces, execution grants, Git actor attribution, branch/push/PR recovery and live two-user GitHub validation |
| Memory and skills | Approved Cortex/shared Markdown design | Actual native and Go integrations, provider/model consolidation, memory inspection, skill editing/version rollout and permission tests |
| Hosting and release | Public Kubernetes/K3s base, optional runner overlay, pinned provider image and local development builds | Cortex deployment templates, Hetzner provisioning with the owner, backup restore, two clean reviews, soak tests, Developer ID/notarization, universal V1 release and recorded website demo |

Next: finish cloud account/runner and Git
contracts, then memory/skills and shared execution. Validate the full workflow
locally before the owner provisions the Hetzner box with us. Cloud sign-in and
the two-Mac acceptance run follow that deployment. Hardware has not been bought
or provisioned for this goal.

The native account form and synthetic provider walkthrough now pass locally.
The Go controller provisions per-installation workers, renews managed grants,
and removes old Pods before deleting their storage and Secrets. It fences stale
epochs and preserves the private journal when restarting a terminal worker.
Local tests cover concurrency, limited database permissions, cleanup ordering
and restart behavior. The runner image, database bootstrap and rendered
manifests pass local checks. Live sandbox, storage, network and OAuth behavior
still need validation on the target cluster. See [runner provisioning](runner-provisioning.md).

Automatic discovery finds local profile stores without asking for paths. It
does not prove that every historical account is still signed in, or that six
accounts necessarily use six folders. A custom location or an unreadable Mac
user directory remains visible as a limitation instead of being guessed.

The existing release exclusions remain: no automatic account rotation or quota
bypass, no guaranteed never-expiring login, and no silent execution fallback.
Normal provider sign-in and renewal remain provider-controlled.

## Final design acceptance

After implementation, compare the running native app with the approved
interactive design at `http://127.0.0.1:8770/capybara-camp-interactive.html`.
Review every screen and its empty, active, waiting, error and completed states,
including the board, chat, planning, account setup, projects, memory, skills
and notifications. Check wording, icons, shared controls, card dimensions,
click areas, keyboard navigation, scrolling and streaming behavior. Record
differences, fix them, and repeat the affected computer-use flows before release.
Passing unit tests alone does not satisfy this gate.

Known difference reported by the owner: an in-progress chat currently places
sub-agents beneath Recent sessions. That layout is wrong. Match the approved
design's separation of the main conversation and its sub-agents, and verify
switching between them without mixing their transcripts or losing position.

The GitHub QA walkthrough also found that Command-Q took effect after closing
the Manage sources sheet, but did not close the app from inside that sheet.
Check quit behavior for every modal during the final keyboard-access audit.

This review belongs at the end of implementation, alongside the two clean
code-review cycles, native acceptance tests and performance checks. Preserve
the owner's existing wording and clean, uniform card layout throughout.
