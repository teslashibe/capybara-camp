# Workspace transfer payload

`server/internal/transfer` captures selected files for review. The Go Camp API
stores resumable, encrypted uploads of that manifest. Neither component is
connected to the native transfer UI yet. Upload completion means the payload
passed validation and is ready for destination staging. It does not write a
destination, move a session, or change its assignment. Used cloud workspaces
remain protected by the existing transfer requirement.

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

## Resumable upload API

All routes below require a signed-in device and the source session's owner.
Camp membership, editor access and permission to read a shared transcript do
not grant transfer access. Responses use `Cache-Control: no-store`. A server
must have `CAMP_PROVIDER_ENCRYPTION_KEY` configured to encrypt payloads.

Use `/v1/camps/{camp}/sessions/{session}/transfers` as the route prefix.

| Method and suffix | Contract |
| --- | --- |
| `POST /` | Create an upload with an `Idempotency-Key` and the reviewed source/destination snapshot |
| `GET /` | Return the latest 25 transfers for this session, including uploaded chunk ordinals |
| `GET /{id}` | Read durable state and resume an interrupted upload |
| `PUT /{id}/chunks/{ordinal}` | Upload one base64-encoded `data` value and its lowercase hexadecimal `sha256` |
| `POST /{id}/complete` | Send `{}` to validate the complete payload and change its state to `ready` |
| `GET /{id}/chunks/{ordinal}` | Read one verified chunk from a `ready` transfer |
| `POST /{id}/cancel` | Send `{}` to discard the stored chunks without touching the source |

Creation accepts these fields:

```json
{
  "target_location": "cloud",
  "target_account_id": "OWNER_ACCOUNT_UUID",
  "expected_epoch": 1,
  "expected_revision": 1,
  "expected_transcript_revision": 0,
  "expected_permission_revision": 1,
  "payload_sha256": "SHA256_OF_EXACT_MANIFEST_JSON",
  "review_sha256": "MANIFEST_DIGEST",
  "payload_bytes": 12345
}
```

The placeholders above must be replaced with real IDs and 64-character hashes.
For a Local destination, omit `target_account_id`; the API binds the transfer
to the authenticated device. A Cloud destination must be an owner's connected,
unexpired account for the source provider. Creation records that account's
epoch. Completion rechecks it, so changing the destination login requires a new
transfer and review.

The source must have no external owner or unfinished prompt request. Its
execution epoch, display revision, transcript revision and permission revision
must match at creation and completion. Cloud waiting states with unresolved
requests also fail this check. This is snapshot validation; it does not stop
the source or reserve the execution assignment.

Chunks are 256 KiB, with only the final chunk allowed to be shorter. The API
returns `chunk_bytes`, `uploaded_chunks` and `uploaded_bytes`, so a restarted
client can upload only the missing chunks. Out-of-order uploads are supported.
Repeating an identical chunk leaves the revision and byte count unchanged,
including after completion. Different bytes at an occupied ordinal return a
conflict. Creation-key replay returns the current transfer; changed creation
input with that key returns a conflict.

Chunk storage uses the operator's existing AES-GCM key with fresh nonces and
authenticated owner, Camp, transfer and ordinal bindings. This is encryption at
rest under the server's key, not end-to-end encryption. The completion path
verifies chunk integrity/order, the exact payload hash, strict manifest decoding
and the reviewed manifest digest before allowing chunk downloads. Invalid
uploads remain unverified until cancelled or expired. Uploaded bytes may contain
sensitive material before validation; the native review and source exclusions
are still required.

Each owner may hold two `uploading` or `ready` transfers across all Camps.
They expire 24 hours after creation; reads, lists and new uploads remove expired
chunks when they encounter them. This cleanup is opportunistic, so an inactive
server may retain encrypted expired chunks until the next operation. Cancel
immediately deletes chunks. Metadata remains for status and replay. A server
process admits at most two concurrent completion validations and returns 429
for excess work. Chunk HTTP bodies are capped at 384 KiB to accommodate base64;
decoded chunks and the complete payload retain their smaller format limits.

Creation, completion and cancellation emit owner-only `transfer.updated`
events. Intermediate chunk progress is available from status responses.
Replaying completion of a ready transfer only reports its stored validation
result. The future handoff must recheck source and destination eligibility even
when this earlier validation succeeded.

Schema 20 adds `session_transfers` and `transfer_chunks`. Existing sessions,
assignments and queued work are retained. Upgrades use the normal migration
command; destructive down-migration is refused.

## Remaining integration

The agreed transfer flow still requires:

1. A source session lock and acknowledged terminal state, with unresolved
   provider outcomes and queued prompts held for explicit review
2. Resumable destination staging using the uploaded payload, including
   transcript, child-session and attachment provenance
3. Destination validation of the pinned base, file preimages and final hashes,
   including the base repository's credential exclusions, with conflicts
   returned for review and no overwrite of a diverged worktree
4. A durable destination receipt followed by one atomic assignment-epoch
   change, with replay-safe recovery and the source retained on failure
5. Native review, standalone-plan history migration, and local-to-cloud and
   cloud-to-local acceptance through both provider clients

The capture and upload layers do not complete any destination handoff. Its
lifecycle and permissions remain under the existing Camp, runner and
local-client ownership boundaries.

## Validation

```sh
cd server
go test -race ./internal/transfer
# With CAMP_TEST_DATABASE_URL pointing to a disposable PostgreSQL database:
go test -race -run 'TestTransfer|TestMigrationPreservesExistingIdentityData' \
  ./internal/camp ./internal/httpapi ./internal/database
go vet ./internal/transfer ./internal/camp ./internal/httpapi
```

The tests use temporary Git repositories. They cover additions, deletions,
mode changes, exact selection, excluded credentials and links, source drift,
detached commits, tampered payloads, input limits and disabled repository hooks.
PostgreSQL and HTTP tests cover restart/resume, encrypted chunk substitution,
owner-only access, source and account revisions, queued work, expiration,
cancellation, concurrent cross-Camp quotas, body limits and migration retention.
