package config

import (
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type Controller struct {
	DatabaseURL, ProviderKey, APIOrigin, KubeOrigin string
	KubeTokenFile, KubeCAFile, Namespace            string
	RunnerImage, RuntimeClass, StorageClass         string
	Capacity                                        int
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
var digestImage = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9./:_-]*@sha256:[a-f0-9]{64}$`)

func LoadController() (Controller, error) {
	c := Controller{DatabaseURL: os.Getenv("CAMP_CONTROLLER_DATABASE_URL"), ProviderKey: os.Getenv("CAMP_PROVIDER_ENCRYPTION_KEY"), APIOrigin: os.Getenv("CAMP_PUBLIC_URL"),
		KubeOrigin: "https://kubernetes.default.svc", KubeTokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token", KubeCAFile: "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
		Namespace: "capybara-runners", RunnerImage: os.Getenv("CAMP_RUNNER_IMAGE"), RuntimeClass: os.Getenv("CAMP_RUNNER_RUNTIME_CLASS"), StorageClass: os.Getenv("CAMP_RUNNER_STORAGE_CLASS"), Capacity: 32}
	for key, target := range map[string]*string{"CAMP_KUBE_ORIGIN": &c.KubeOrigin, "CAMP_KUBE_TOKEN_FILE": &c.KubeTokenFile, "CAMP_KUBE_CA_FILE": &c.KubeCAFile, "CAMP_RUNNER_NAMESPACE": &c.Namespace} {
		if value := os.Getenv(key); value != "" {
			*target = value
		}
	}
	if value := os.Getenv("CAMP_RUNNER_CAPACITY"); value != "" {
		var err error
		c.Capacity, err = strconv.Atoi(value)
		if err != nil {
			return c, errors.New("CAMP_RUNNER_CAPACITY must be an integer")
		}
	}
	return c, c.Validate()
}

func (c Controller) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("CAMP_CONTROLLER_DATABASE_URL is required")
	}
	key, err := base64.StdEncoding.Strict().DecodeString(c.ProviderKey)
	if err != nil || len(key) != 32 {
		return errors.New("CAMP_PROVIDER_ENCRYPTION_KEY must be a base64-encoded 32-byte key")
	}
	for _, raw := range []string{c.APIOrigin, c.KubeOrigin} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return errors.New("Controller service URLs must be HTTPS origins")
		}
	}
	if !strings.HasPrefix(c.KubeTokenFile, "/") || !strings.HasPrefix(c.KubeCAFile, "/") || !dnsLabel.MatchString(c.Namespace) || !dnsLabel.MatchString(c.RuntimeClass) || !dnsLabel.MatchString(c.StorageClass) {
		return errors.New("Controller paths, namespace, runtime and storage class are required")
	}
	if c.Capacity < 1 || c.Capacity > 1024 || !digestImage.MatchString(c.RunnerImage) {
		return errors.New("Controller needs a digest-pinned runner image and capacity of 1–1024")
	}
	return nil
}
