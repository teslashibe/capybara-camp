package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestControllerRequiresSandboxDigestAndSeparateCredentials(t *testing.T) {
	base := Controller{DatabaseURL: "postgres://controller@example/camp", ProviderKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), APIOrigin: "https://camp.example", KubeOrigin: "https://kubernetes.default.svc", KubeTokenFile: "/run/token", KubeCAFile: "/run/ca", Namespace: "capybara-runners", RunnerImage: "registry.example/camp@sha256:" + strings.Repeat("a", 64), RuntimeClass: "runsc", StorageClass: "local-path", Capacity: 32}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*Controller){
		func(c *Controller) { c.RuntimeClass = "" }, func(c *Controller) { c.RunnerImage = "registry.example/camp:latest" }, func(c *Controller) { c.APIOrigin = "http://camp.example" },
		func(c *Controller) { c.KubeOrigin = "https://user:password@cluster.example" }, func(c *Controller) { c.Namespace = "../other" }, func(c *Controller) { c.Capacity = 0 },
		func(c *Controller) { c.ProviderKey = "" }, func(c *Controller) { c.DatabaseURL = "" },
	}
	for _, change := range mutations {
		value := base
		change(&value)
		if value.Validate() == nil {
			t.Fatal("unsafe controller configuration accepted")
		}
	}
}
