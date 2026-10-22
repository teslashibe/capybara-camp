# Cloud runner provisioning

The optional Go controller creates one isolated worker per provider
installation. It is implemented and fixture-tested, but has not yet been
validated on a live Kubernetes cluster. The worker handles provider sign-in and
isolated Git checkout preparation; cloud chat execution and transfers remain
separate work.

## Control and worker permissions

`camp-controller` connects to PostgreSQL with the separate `camp_controller`
role. That role can inspect account metadata and manage installation grants. It
cannot read provider credential ciphertext, login challenge ciphertext, human
email addresses or device tokens. The controller receives the provider encryption
key so it can recover its own encrypted runner grants after restart. It does not
receive the Camp authentication secret or email-service key.

The Kubernetes role can get, list, create and delete Pods, Secrets and PVCs only
in `capybara-runners`. It can get and create the isolation NetworkPolicy there.
It has no exec, attach, role-binding, node, cluster-wide or application-namespace
Secret access. Workers receive no Kubernetes service-account token or database
credentials. The API authenticates each worker against its installation grant.

Schema 11 adds encrypted managed grants and workload status. Create the
controller database role before running that migration. The public PostgreSQL
bootstrap does this when `CAMP_CONTROLLER_DB_PASSWORD` is supplied. For an
existing database that already applied schema 11 without that role, create the
role through your normal database administration process and apply the GRANT
block from `00011_runner_provisioning.sql` as the migration owner. Do not rerun
the whole migration or grant access to the provider credential columns.

Schema 14 also grants the controller read access to only the workspace
installation ID, state and lease expiry. These columns let it defer grant
rotation during checkout. If the role is created after migration, apply the
GRANT block from `00014_workspace_claims.sql` as well. Workspace credentials,
repository names and other owners' user data remain unavailable to this role.

Schema 15 adds `runner_execution_leases`, which exposes only installation IDs
and lease times. The controller uses it to defer rotation during a cloud turn.
If creating the role after migration, apply that migration's GRANT block too.
Prompt bodies and request history remain inaccessible to the controller.

## Reconciliation and cleanup

Each pass reads at most 64 accounts. Account locks serialize managed-grant
creation and an authorization-slot cap limits active managed installations.
The controller reuses the encrypted grant across retries. Near expiry it
replaces an idle grant; it does not rotate one during an active login or a live
workspace-preparation lease. A manual
grant is never silently replaced by the controller.

Resource names derive from a grant UUID. Labels bind each resource to its
installation and epoch; account names and email addresses do not appear in
Kubernetes metadata. Retries inspect existing resources and refuse conflicting
security settings, images, sidecars, environment entries or mounts.

A terminal worker Pod is recreated with its existing grant and private volume,
preserving the journal that distinguishes a retriable result from an uncertain
provider operation. Initialization and crash-loop failures remain visible;
the controller does not continually reset Kubernetes restart backoff. A worker
is ready only when Kubernetes reports both a running phase and Ready condition.

Before a new grant's Pod starts, the controller removes older resources in this
order: Pod, PVC, Secret. Each delete binds the observed UID and resource version.
It observes a resource as gone before moving to the next layer. It never sends
a force-deletion request. An unavailable node or stuck finalizer therefore
leaves cleanup visibly pending. The distinction matters because [force-deleting
a Kubernetes Pod does not confirm that its processes stopped](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_delete/).

Disconnect immediately fences provider access in PostgreSQL. The controller
marks erasure complete only after observing the managed resources gone. Late
status or cleanup responses cannot change a newer installation epoch. A
reactivated account gets a new grant and private volume while retaining its
logical account identity and shared history.

## Runtime requirements

Every Pod specifies the configured sandbox RuntimeClass. Install and validate
that runtime first; the controller does not fall back to the default runtime.
See the [gVisor Kubernetes guide](https://gvisor.dev/docs/user_guide/quick_start/kubernetes/)
and [K3s runtime configuration](https://docs.k3s.io/advanced) for node setup.

Workers run as UID/GID 10001 with a read-only root filesystem, dropped
capabilities, no privilege escalation, and bounded CPU, memory and temporary
storage. Tini reaps child processes. A small init container copies only the
installation grant into a private memory-backed directory. The worker's state
directory is on its own PVC. A second, owner-private PVC holds workspaces and
survives routine grant rotation within the installation epoch. Disconnect and
reactivation clean up that volume with the old epoch. No host paths, host PID
namespace or shared writable provider home are mounted.

The controller creates the isolation NetworkPolicy before provisioning workers.
It denies inbound traffic, allows cluster DNS, and allows outbound IPv4 HTTPS
while excluding private, loopback and link-local ranges. The configured Camp
origin must be reachable over public HTTPS through the ingress. Private-only
origins and IPv6-only clusters need an explicitly reviewed policy adaptation.
NetworkPolicy requires an enforcing CNI; a rendered policy alone is not proof
of network isolation. Test cross-Pod and metadata-endpoint denial on the target
cluster before release.

The namespace quota separately bounds physical Pods, Secrets and volumes. Keep
it consistent with `CAMP_RUNNER_CAPACITY` and the server's available memory.
Use an encrypted storage backend with a `Delete` reclaim policy and validate
its cleanup behavior. Observing a deleted PVC does not prove physical media
erasure, and backup retention remains an operator policy.
Each installation uses two 1 GiB volumes: the replaceable auth journal and the
workspace volume. Grant rotation retains the encrypted provider state in
PostgreSQL and the workspace volume, but clears the old auth journal volume.
Provider OAuth renewal and credential restoration for cloud inference remain
part of the execution integration.

## Build and deploy

The runner image uses pinned official Node, Codex and Claude Code packages.
The lockfile records package integrity. Build from the repository root:

```sh
docker build --platform linux/amd64 -f server/Dockerfile.runner \
  -t YOUR_REGISTRY/capybara-camp-runner:YOUR_VERSION .
docker build --platform linux/amd64 -f server/Dockerfile \
  -t YOUR_REGISTRY/capybara-camp:YOUR_VERSION .
kustomize build deploy/kubernetes/runner > /tmp/camp-with-runners.yaml
```

Publish the images to your registry and use their immutable digests in your
operator overlay. Replace all example domains, image references, runtime and
storage settings. Supply `CAMP_CONTROLLER_DATABASE_URL` using the limited role,
and the same `CAMP_PROVIDER_ENCRYPTION_KEY` as the API. Keep these values outside
the public repository. The runner overlay includes the API/database base, a
separate runner namespace, controller RBAC and a quota. Add your existing
HTTPS ingress; it does not provision a server or buy infrastructure.

Schema, image and sandbox upgrades need an operator rollout plan. Do not change
the controller's desired image under an active worker and assume a hot upgrade:
conflicting existing resources are held for review. Drain or disconnect the
affected installation, confirm cleanup, then reactivate it against the new
configuration.

## Evidence still required

The local PostgreSQL and Kubernetes API fixtures cover grant concurrency,
capacity, erasure ordering, stale epochs, restart recovery and unsafe resource
changes. The optional database role bootstrap passes with and without a
controller, and the rendered manifests pass Kubernetes schema validation.
The generated Pod, both PVCs, Secret and isolation policy also pass schema validation
through `TestRunnerManifestSchema` when `CAMP_KUBE_SCHEMA_TEST` points to
`kubeconform`.
Image checks confirm private grant-file permissions, non-root execution and
both CLI versions on Linux. These do not
prove live CNI enforcement, sandbox compatibility, volume recovery or provider
sign-in. The live-cluster checks, real provider turns with the Mac disconnected,
backup restore and final two-Mac acceptance remain release gates.
