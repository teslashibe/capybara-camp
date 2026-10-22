package provisioner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/config"
)

// Validate the dynamically generated resources as well as the static overlay.
func TestRunnerManifestSchema(t *testing.T) {
	validator := os.Getenv("CAMP_KUBE_SCHEMA_TEST")
	if validator == "" {
		t.Skip("CAMP_KUBE_SCHEMA_TEST is required")
	}
	cfg := config.Controller{Namespace: "capybara-runners", RunnerImage: "registry.example/camp@sha256:" + string(bytes.Repeat([]byte{'a'}, 64)), APIOrigin: "https://camp.example", RuntimeClass: "runsc", StorageClass: "local-path"}
	grant := camp.ControllerGrant{ID: uuid.NewString(), Account: camp.ProviderAccount{InstallationID: uuid.NewString(), Epoch: 1}, Token: "fixture-grant-only"}
	secret, pvc, pod := resources(cfg, grant)
	var manifests bytes.Buffer
	for _, resource := range []object{secret, pvc, pod, workspaceVolume(cfg, grant), isolationPolicy(cfg.Namespace)} {
		manifests.WriteString("---\n")
		if err := json.NewEncoder(&manifests).Encode(resource); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, validator, "-strict", "-summary")
	command.Stdin = &manifests
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated workload schema failed: %v (%s)", err, output)
	}
}

// Opt in only after building the local image. This exercises the actual init
// command as the container's unprivileged user with a read-only root.
func TestRunnerImageBootstrap(t *testing.T) {
	image := os.Getenv("CAMP_RUNNER_IMAGE_TEST")
	if image == "" {
		t.Skip("CAMP_RUNNER_IMAGE_TEST is required")
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("fixture-grant-only"), 0444); err != nil {
		t.Fatal(err)
	}
	_, _, pod := resources(config.Controller{Namespace: "capybara-runners", RunnerImage: image, RuntimeClass: "runsc", StorageClass: "local-path"}, camp.ControllerGrant{ID: uuid.NewString(), Account: camp.ProviderAccount{InstallationID: uuid.NewString(), Epoch: 1}})
	init := pod.Spec["initContainers"].([]any)[0].(map[string]any)
	script := init["args"].([]any)[0].(string) + `; test "$(stat -c %a /auth/private/token)" = 600; test "$(stat -c %a /state/private)" = 700; test "$(stat -c %a /workspaces/private)" = 700; test "$(stat -c %u /auth/private/token)" = 10001; test "$(id -u)" = 10001`
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--tmpfs", "/auth:mode=1777", "--tmpfs", "/state:mode=1777", "--tmpfs", "/workspaces:mode=1777", "--tmpfs", "/tmp", "--mount", "type=bind,source="+file+",target=/grant/token,readonly", "--entrypoint", "/bin/sh", image, "-eu", "-c", script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("image bootstrap failed: %v (%s)", err, output)
	}
}
