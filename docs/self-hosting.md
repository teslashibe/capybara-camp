# Self-hosting Capybara Camp

These are V1 development templates. The current server implements Camp sign-in,
devices, membership, projects, selected session sharing, versioned transcripts,
durable prompt requests and ordered events. The development desktop also runs
approved shared requests through the owning Mac's local CLI, with leased
execution and explicit review of uncertain outcomes. The Go server also persists
versioned planning tickets and their bounded five-step queue. Isolated cloud
provider turns are implemented locally and still await live acceptance. Memory,
skills and the remaining cloud workflow are unfinished. The base manifests serve shared state;
an optional [runner overlay and controller](runner-provisioning.md) supplies
installation-worker provisioning. Live-cluster validation is still pending. Do not treat
these templates as the completed V1 release or a production deployment sign-off.

Every collaborating Mac connects to the same HTTPS service origin and Camp.
People keep separate identities and choose what to share. Separate installations
have separate users and databases; V1 does not federate them or discover peers
on a local network. Local private chats remain available during a cloud outage.

## Prerequisites

- A Kubernetes cluster with a network-policy-capable CNI and persistent storage
- A DNS name, HTTPS ingress and certificate issuer
- Resend credentials and a verified sender for sign-in codes
- Docker, kubectl and Kustomize on the operator's machine
- An off-host backup destination and a tested recovery plan before production

The Hetzner example targets one K3s node with Traefik, cert-manager and the
`local-path` storage class. Install and configure those prerequisites first.
The example assumes the ingress controller runs in `kube-system`; change the
API NetworkPolicy selector if your controller uses another namespace.

The public templates require no access to the private `k8s-control` repository.
For our installation, hardware selection and live deployment will be done with
the owner after the local app and release candidate have been validated.

## Build and configure

From the repository root:

```sh
docker build -f server/Dockerfile -t YOUR_REGISTRY/capybara-camp:YOUR_VERSION .
docker push YOUR_REGISTRY/capybara-camp:YOUR_VERSION
kustomize build deploy/kubernetes/overlays/hetzner-k3s > /tmp/camp.yaml
```

Create an operator-owned overlay outside the repository. Replace the image with
your registry and an immutable digest, the example domain with your HTTPS origin,
the sender address, and the certificate issuer. Preserve the shared base. Use
the same image digest for the API and migration job.

Copy `deploy/examples/secrets.env.example` to a protected file outside the
repository. Replace every placeholder with independently generated secrets.
The passwords in the two database URLs must match their corresponding password
fields. URL-encode password characters in those URLs. Keep the authentication
secret stable and include it in encrypted disaster-recovery backups: changing it
invalidates outstanding email codes and encrypted command receipts.

Create the namespace and Secret with your own kubeconfig context:

```sh
kubectl --context YOUR_CONTEXT create namespace capybara-camp
kubectl --context YOUR_CONTEXT -n capybara-camp create secret generic camp-secrets \
  --from-env-file=/YOUR/PROTECTED/PATH/camp-secrets.env
```

Use an encrypted secret workflow such as SOPS for GitOps installations. Never
commit the populated env file, generated kubeconfig or plaintext Secret YAML.
The API receives only its runtime database credentials, auth key and mail key.
Migration credentials and the PostgreSQL administrator password are separate.

## Apply and verify

Apply your rendered overlay explicitly to the chosen context. The API refuses
traffic until the exact schema is available. Check the PostgreSQL rollout,
`camp-migrate` Job completion, API readiness and HTTPS certificate before allowing
users to connect. `/health` checks the process; `/ready` checks both database
schemas.

Schema 30 introduces memory observation metadata. The migration command also
applies the pinned Cortex migrations in a private schema, `camp_memory` for the
default public Camp schema. Custom Camp schemas use a separate derived schema
name. The API opens this store without running migrations and refuses startup
when it is missing or incomplete. Retrying the migration command completes a
partially finished setup. Keep the complete database in backups so Cortex and
Camp metadata are restored together. The embedded library needs no separate
listener, deployment or provider credentials. Its license is included at
`licenses/cortex.txt` and in the container images.

The database bootstrap SQL runs only on a new data volume. Changing a password
in a Kubernetes Secret does not change an existing PostgreSQL role password.
Perform a coordinated credential rotation through PostgreSQL and the affected
Secret, then restart the affected client. Do not delete the volume to rotate it.

The migration job is explicit. Before upgrading, back up the database, review
schema compatibility, and re-create the completed migration job using the new
image. Do not blindly downgrade a migrated database. Restore or use the documented
compatible application image if rollback is needed.

Schema 17 changes workspace scheduling to retain multiple ready tickets per
account. Stop older runner workloads before enabling the matching API and
controller image: older workers assume one retained checkout and may clean up
another unused ticket. Deploy the matching runner image before admitting work.

Schema 19 adds immutable execution bindings to planning participants. Back up,
finish or explicitly cancel active work, and stop older API/runner writers
before migrating; deploy matching images before accepting requests. Old plan
history is preserved with an unknown location. Already queued requests retain
their existing assignments, but the next planning phase cannot infer an old
destination and requires a new reviewed revision. Do not roll back to a writer
that creates unbound planning participants.

## Storage and recovery

The example uses one 20 GiB persistent volume. Size it for your retained shared
history, monitor free space, and configure encrypted base backups plus WAL
archiving to an off-host destination. A local volume or mirrored disks do not
protect against loss of the host. K3s is not a backup strategy.

Before production, demonstrate a clean restore including the database and auth
secret, verify member access and event cursors, and reconcile any request whose
provider execution outcome is unknown. Target recovery point: 15 minutes;
target recovery time: four hours. These are release targets, not guarantees
provided by the templates.

## Connect desktop clients

The V1 development desktop connection screen accepts the configured HTTPS origin.
Each person signs in independently, joins the same Camp through an invitation,
and explicitly shares selected sessions. Local provider logins stay on their
owning Macs. The cloud account flow creates separate, owner-bound
installations and runs the official CLIs in isolated Linux workloads. Choosing
Cloud does not share account credentials with other members. The connection
screen is not in v0.0.1. Follow [execution locations](execution-locations.md)
for the native assignment flow and its remaining transfer limits.
