# Cloud process isolation

Cloud workers use a mandatory Linux Landlock domain around provider and Git
subprocesses. The parent worker retains its account grant and control journal.
A child can read the installed provider tools and system libraries, read and
write its private provider home, and access the assigned workspace. Chat and
planning provider processes receive read-only workspace access; explicitly
approved implementation receives write access. The provider's own permission
policy still applies inside that boundary.

A Git subprocess receives its one workspace, an empty private scratch home,
and only its current short-lived checkout credential file when required. It
cannot read the account grant, control journal, other workspaces or provider
homes. Git's existing restrictions on hooks, system configuration, redirects
and credential helpers remain in place.

The domain is applied on a locked OS thread immediately before executing the
provider or Git binary. Descendants inherit the restrictions. Filesystem
traversal through a symlink cannot grant additional access. The domain also
restricts signals and abstract Unix sockets, and Landlock restricts ptrace
across domain boundaries. TCP access is governed by the Pod network policy;
this filesystem boundary does not replace the container runtime or network
policy.

The worker requires Landlock ABI 6 or later. There is no production flag to
turn it off. A missing or disabled kernel feature stops the worker before it
reads account state or launches a provider. The selected Kubernetes runtime
must actually expose and enforce this ABI; a runtime-class name alone is not
proof. Run the tests below on the target node/runtime before enabling cloud
accounts. User-space kernels may expose different syscall support from the
host kernel.

Provider OAuth credentials remain available to that provider's private home,
as they are in the normal CLI. The process domain protects Camp's control
credentials and unrelated installations; it does not claim to hide a
provider's own credentials from code granted access to that same home.

## Checks

```sh
./tools/test-processguard.sh
# After building the runner image:
CAMP_RUNNER_IMAGE_TEST=YOUR_LOCAL_IMAGE go -C server test \
  ./internal/provisioner -run TestRunnerImageProcessGuard -v
# Inside the chosen production runtime:
/camp-runner process-guard-check
```

The first command must pass, without skipping. It uses an unprivileged user,
dropped capabilities, no network and a read-only root filesystem. It checks
allowed workspace operations, denied control-grant reads, denied symlink
escapes, denied journal writes, inherited restrictions in a shell child, and
denied parent-process signalling and reads of its environment, memory, file
descriptors and filesystem through `/proc`. Process metadata remains read-only
for the JavaScript runtimes. Ordinary Go tests may
skip the Linux-only enforcement test on a host without the required feature;
such a result does not pass this release gate.

The runner-image check exercises the installed Codex and Claude binaries in
the domain without signing in or sending a model request. Live sign-in,
provider turns, target-runtime enforcement and Mac-offline execution remain
separate acceptance requirements.

The kernel's [Landlock documentation](https://docs.kernel.org/userspace-api/landlock.html)
describes inherited restrictions, ptrace, signal scopes and the ABI contract.
