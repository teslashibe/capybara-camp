package provisioner

import (
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/config"
)

func resourceName(grant string) string { return "camp-" + strings.ReplaceAll(grant, "-", "") }
func labels(grant camp.ControllerGrant) map[string]string {
	return map[string]string{"capybara.camp/managed-by": "controller", "capybara.camp/installation": grant.Account.InstallationID,
		"capybara.camp/grant": grant.ID, "capybara.camp/epoch": strconv.FormatInt(grant.Account.Epoch, 10)}
}
func owned(obj object, installation string) bool {
	id := obj.Metadata.Labels["capybara.camp/grant"]
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id && obj.Metadata.Name == resourceName(id) && obj.Metadata.Labels["capybara.camp/managed-by"] == "controller" && obj.Metadata.Labels["capybara.camp/installation"] == installation
}
func resources(c config.Controller, g camp.ControllerGrant) (object, object, object) {
	m := metadata{Name: resourceName(g.ID), Namespace: c.Namespace, Labels: labels(g)}
	immutable := true
	secret := object{APIVersion: "v1", Kind: "Secret", Metadata: m, Type: "Opaque", Immutable: &immutable, Data: map[string]string{"token": base64.StdEncoding.EncodeToString([]byte(g.Token))}}
	pvc := object{APIVersion: "v1", Kind: "PersistentVolumeClaim", Metadata: m, Spec: map[string]any{"accessModes": []any{"ReadWriteOnce"}, "storageClassName": c.StorageClass, "volumeMode": "Filesystem", "resources": map[string]any{"requests": map[string]any{"storage": "1Gi"}}}}
	security := map[string]any{"allowPrivilegeEscalation": false, "privileged": false, "readOnlyRootFilesystem": true, "runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001,
		"capabilities": map[string]any{"drop": []any{"ALL"}}, "seccompProfile": map[string]any{"type": "RuntimeDefault"}, "procMount": "Default"}
	limits := map[string]any{"requests": map[string]any{"cpu": "100m", "memory": "256Mi"}, "limits": map[string]any{"cpu": "1500m", "memory": "1Gi"}}
	init := map[string]any{"name": "private-grant", "image": c.RunnerImage, "imagePullPolicy": "IfNotPresent", "command": []any{"/bin/sh", "-eu", "-c"},
		"args":            []any{"mkdir -p /auth/private /state/private; chmod 700 /auth/private /state/private; cp /grant/token /auth/private/token; chmod 600 /auth/private/token"},
		"securityContext": security, "resources": limits, "env": []any{}, "volumeMounts": []any{
			map[string]any{"name": "grant-source", "mountPath": "/grant", "readOnly": true}, map[string]any{"name": "auth", "mountPath": "/auth"}, map[string]any{"name": "state", "mountPath": "/state"}}}
	runner := map[string]any{"name": "runner", "image": c.RunnerImage, "imagePullPolicy": "IfNotPresent", "command": []any{"/usr/bin/tini", "--", "/camp-runner"}, "args": []any{}, "securityContext": security, "resources": limits,
		"env":          []any{map[string]any{"name": "CAMP_RUNNER_ORIGIN", "value": c.APIOrigin}, map[string]any{"name": "CAMP_RUNNER_GRANT_FILE", "value": "/auth/private/token"}, map[string]any{"name": "CAMP_RUNNER_STATE_DIR", "value": "/state/private"}},
		"volumeMounts": []any{map[string]any{"name": "auth", "mountPath": "/auth", "readOnly": true}, map[string]any{"name": "state", "mountPath": "/state"}, map[string]any{"name": "tmp", "mountPath": "/tmp"}}}
	pod := object{APIVersion: "v1", Kind: "Pod", Metadata: m, Spec: map[string]any{
		"runtimeClassName": c.RuntimeClass, "automountServiceAccountToken": false, "serviceAccountName": "default", "hostNetwork": false, "hostPID": false, "hostIPC": false, "shareProcessNamespace": false,
		"restartPolicy": "OnFailure", "terminationGracePeriodSeconds": 30,
		"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001, "fsGroupChangePolicy": "OnRootMismatch", "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
		"initContainers":  []any{init}, "containers": []any{runner}, "volumes": []any{
			map[string]any{"name": "grant-source", "secret": map[string]any{"secretName": m.Name, "defaultMode": 288}},
			map[string]any{"name": "auth", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "16Mi"}},
			map[string]any{"name": "tmp", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "256Mi"}},
			map[string]any{"name": "state", "persistentVolumeClaim": map[string]any{"claimName": m.Name}},
		}}}
	return secret, pvc, pod
}

func isolationPolicy(namespace string) object {
	return object{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy", Metadata: metadata{Name: "camp-runner-isolation", Namespace: namespace, Labels: map[string]string{"capybara.camp/managed-by": "controller"}}, Spec: map[string]any{
		"podSelector": map[string]any{"matchLabels": map[string]any{"capybara.camp/managed-by": "controller"}}, "policyTypes": []any{"Ingress", "Egress"}, "ingress": []any{},
		"egress": []any{
			map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "kube-system"}}, "podSelector": map[string]any{"matchLabels": map[string]any{"k8s-app": "kube-dns"}}}},
				"ports": []any{map[string]any{"protocol": "UDP", "port": 53}, map[string]any{"protocol": "TCP", "port": 53}}},
			map[string]any{"to": []any{map[string]any{"ipBlock": map[string]any{"cidr": "0.0.0.0/0", "except": []any{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "127.0.0.0/8"}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 443}}},
		},
	}}
}
