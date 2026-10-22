# Workspace transfer payload

`server/internal/transfer` is the read-only file-capture layer for the planned
reviewed transfer flow. It is not connected to the native app or a transfer
endpoint yet. Capturing a manifest does not upload files, write a destination,
move a session, or change its assignment. Used cloud workspaces remain protected
by the existing transfer requirement.

## Captured state

`Capture(ctx, directory, selectedPaths)` reads an explicitly selected set of
repository-relative paths. The directory must be the Git working root. It
records the current commit, branch name, regular-file additions, changes,
deletions and executable-bit changes. A rename is represented by selecting its
old and new paths. Staged and unstaged changes are captured as the working
tree's final contents; the index split is not transferred.

Each file carries its base-content hash and desired-content hash. Added files
have no base hash, and deleted files have no desired contents. Files outside
the selection never enter the payload. The manifest contains no local root
path, account credentials or timestamps.

Version 1 limits a capture to 256 paths, 4 MiB per file and 32 MiB of desired
file contents. JSON decoding is bounded to 46 MiB. Git tree output is also
bounded. The current format requires a 40-character Git commit ID.

## Review and validation

`Manifest.Digest()` binds the base, branch, paths, file hashes, executable bits
and exclusion list. Reordering entries or formatting the JSON differently
does not change that digest. `Decode` validates content hashes, rejects
duplicate or case-colliding paths, unknown fields and versions, trailing JSON,
traversal and ambiguous path forms.

`VerifySource` rereads the working root, commit, branch and selected file
contents. A changed source requires a new capture and review. Capture also
performs that check before returning. The eventual handoff must still stop
source execution and revalidate this snapshot before accepting a destination
receipt.

Git reads use fixed commands with hooks, filesystem monitors, credentials and
external configuration disabled. File reads stay within an `os.Root`, refuse
links and non-regular files, and verify file metadata around the read.

## Exclusions

The manifest lists exclusions without their contents. Credential paths include
provider homes, `.env` files, common credential stores and private-key file
types. Content checks reject recognizable private-key blocks and common API
token formats. These checks do not identify every possible secret; they do not
replace the owner's review or the release security audit.

Symlinks, hardlinks, directories and submodules are currently reported as
excluded or unsupported. Unchanged selected files are identified separately.
The future review UI must show these outcomes before accepting a transfer;
it must not claim that an excluded file moved. Missing selected untracked
files are errors, while a missing tracked file is a deletion.

## Remaining integration

The agreed transfer flow still requires:

1. A source session lock and acknowledged terminal state, with unresolved
   provider outcomes and queued prompts held for explicit review
2. Owner-bound transfer records, chunk storage and resumable destination
   staging, including transcript, child-session and attachment provenance
3. Destination validation of the pinned base, file preimages and final hashes,
   including the base repository's credential exclusions, with conflicts
   returned for review and no overwrite of a diverged worktree
4. A durable destination receipt followed by one atomic assignment-epoch
   change, with replay-safe recovery and the source retained on failure
5. Native review, standalone-plan history migration, and local-to-cloud and
   cloud-to-local acceptance through both provider clients

The capture layer alone satisfies none of those handoff steps. Their lifecycle
and permissions remain under the existing Camp, runner and local-client
ownership boundaries.

## Validation

```sh
cd server
go test -race ./internal/transfer
go vet ./internal/transfer
```

The tests use temporary Git repositories. They cover additions, deletions,
mode changes, exact selection, excluded credentials and links, source drift,
detached commits, tampered payloads, input limits and disabled repository hooks.
