// Package provisioner reconciles only Camp-owned provider workloads.
package provisioner

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/config"
)

var ErrCluster = errors.New("cluster unavailable")
var ErrConflict = errors.New("managed workload conflicts with existing resources")
var ErrPending = errors.New("workload is pending")
var ErrWorkloadFailed = errors.New("workload failed to start")

type metadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	UID               string            `json:"uid,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp,omitempty"`
	Labels            map[string]string `json:"labels"`
}
type object struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   metadata          `json:"metadata"`
	Spec       map[string]any    `json:"spec,omitempty"`
	Data       map[string]string `json:"data,omitempty"`
	Immutable  *bool             `json:"immutable,omitempty"`
	Type       string            `json:"type,omitempty"`
	Status     *podStatus        `json:"status,omitempty"`
}
type podStatus struct {
	Phase                 string            `json:"phase"`
	Conditions            []podCondition    `json:"conditions"`
	ContainerStatuses     []containerStatus `json:"containerStatuses"`
	InitContainerStatuses []containerStatus `json:"initContainerStatuses"`
}
type podCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}
type containerStatus struct {
	State struct {
		Waiting *struct {
			Reason string `json:"reason"`
		} `json:"waiting"`
		Terminated *struct {
			ExitCode int `json:"exitCode"`
		} `json:"terminated"`
	} `json:"state"`
}

func (s containerStatus) failed() bool {
	if s.State.Terminated != nil && s.State.Terminated.ExitCode != 0 {
		return true
	}
	if s.State.Waiting != nil {
		switch s.State.Waiting.Reason {
		case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "CreateContainerError", "RunContainerError", "InvalidImageName", "ErrImageNeverPull":
			return true
		}
	}
	return false
}

type kubeClient struct {
	origin, namespace, tokenFile string
	http                         *http.Client
}

func NewKubeClient(c config.Controller) (*kubeClient, error) {
	ca, err := readFile(c.KubeCAFile, 1<<20)
	if err != nil {
		return nil, ErrCluster
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, ErrCluster
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &kubeClient{origin: c.KubeOrigin, namespace: c.Namespace, tokenFile: c.KubeTokenFile,
		http: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (k *kubeClient) call(ctx context.Context, method, resource, name string, query url.Values, input, output any) (int, error) {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return 0, ErrCluster
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	prefix := "/api/v1/namespaces/"
	if resource == "networkpolicies" {
		prefix = "/apis/networking.k8s.io/v1/namespaces/"
	}
	path := k.origin + prefix + k.namespace + "/" + resource
	if name != "" {
		path += "/" + name
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return 0, ErrCluster
	}
	token, err := readFile(k.tokenFile, 16<<10)
	if err != nil || len(token) > 16<<10 || strings.ContainsAny(strings.TrimSpace(string(token)), "\r\n") {
		return 0, ErrCluster
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := k.http.Do(req)
	if err != nil {
		return 0, ErrCluster
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return 404, nil
	}
	if res.StatusCode == 409 {
		return 409, nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return res.StatusCode, ErrCluster
	}
	if output != nil {
		raw, e := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
		if e != nil || len(raw) > 2<<20 || json.Unmarshal(raw, output) != nil {
			return res.StatusCode, ErrCluster
		}
	}
	return res.StatusCode, nil
}

func (k *kubeClient) list(ctx context.Context, resource, installation string) ([]object, error) {
	var out []object
	cursor := ""
	seen := map[string]bool{}
	for {
		var page struct {
			Items    []object `json:"items"`
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
		}
		query := url.Values{"labelSelector": {"capybara.camp/managed-by=controller,capybara.camp/installation=" + installation}, "limit": {"64"}}
		if cursor != "" {
			query.Set("continue", cursor)
		}
		status, err := k.call(ctx, "GET", resource, "", query, nil, &page)
		if err != nil || status != 200 {
			return nil, ErrCluster
		}
		out = append(out, page.Items...)
		cursor = page.Metadata.Continue
		if len(out) > 256 || len(cursor) > 4096 || seen[cursor] {
			return nil, ErrCluster
		}
		if cursor == "" {
			return out, nil
		}
		seen[cursor] = true
	}
}

func (k *kubeClient) remove(ctx context.Context, resource string, obj object) error {
	if obj.Metadata.UID == "" || obj.Metadata.ResourceVersion == "" {
		return ErrConflict
	}
	status, err := k.call(ctx, "DELETE", resource, obj.Metadata.Name, nil, map[string]any{
		"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground",
		"preconditions": map[string]string{"uid": obj.Metadata.UID, "resourceVersion": obj.Metadata.ResourceVersion},
	}, nil)
	if status == 409 {
		return ErrConflict
	}
	return err
}

func (k *kubeClient) ensure(ctx context.Context, resource string, desired object) (object, error) {
	var current object
	status, err := k.call(ctx, "GET", resource, desired.Metadata.Name, nil, nil, &current)
	if err != nil {
		return current, err
	}
	if status == 404 {
		status, err = k.call(ctx, "POST", resource, "", nil, desired, &current)
		if err != nil {
			return current, err
		}
		if status == 409 {
			status, err = k.call(ctx, "GET", resource, desired.Metadata.Name, nil, nil, &current)
			if err != nil || status != 200 {
				return current, ErrCluster
			}
		}
		if status != 200 && status != 201 && status != 202 {
			return current, ErrCluster
		}
	}
	if current.Metadata.UID == "" || current.Metadata.ResourceVersion == "" {
		return current, ErrCluster
	}
	if current.Metadata.DeletionTimestamp != nil {
		return current, ErrPending
	}
	if !matches(desired, current) {
		return current, ErrConflict
	}
	return current, nil
}

// Check the fields we own. Kubernetes may default scheduler and node fields,
// but may not change security, mount another volume, or inject a sidecar/env.
func matches(want, got object) bool {
	if want.Metadata.Name != got.Metadata.Name || want.Metadata.Namespace != got.Metadata.Namespace || want.Kind != got.Kind {
		return false
	}
	for key, value := range want.Metadata.Labels {
		if got.Metadata.Labels[key] != value {
			return false
		}
	}
	if want.Kind == "Secret" {
		return got.Immutable != nil && *got.Immutable && got.Type == want.Type && equalJSON(want.Data, got.Data)
	}
	if want.Kind == "PersistentVolumeClaim" {
		return contains(want.Spec, got.Spec)
	}
	if want.Kind == "NetworkPolicy" {
		ingress, _ := got.Spec["ingress"].([]any)
		return len(ingress) == 0 && equalJSON(want.Spec["podSelector"], got.Spec["podSelector"]) && equalJSON(want.Spec["policyTypes"], got.Spec["policyTypes"]) && equalJSON(want.Spec["egress"], got.Spec["egress"])
	}
	if want.Kind != "Pod" {
		return false
	}
	// Kubernetes omits false host-namespace flags when serializing a Pod.
	for _, key := range []string{"hostNetwork", "hostPID", "hostIPC", "shareProcessNamespace"} {
		if value, present := got.Spec[key]; present && value != false {
			return false
		}
	}
	for _, key := range []string{"runtimeClassName", "automountServiceAccountToken", "serviceAccountName", "restartPolicy", "terminationGracePeriodSeconds", "securityContext", "volumes"} {
		if !equalJSON(want.Spec[key], got.Spec[key]) {
			return false
		}
	}
	for _, key := range []string{"containers", "initContainers"} {
		a, ok := want.Spec[key].([]any)
		if !ok {
			return false
		}
		b, ok := got.Spec[key].([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i, raw := range a {
			wm := raw.(map[string]any)
			gm, ok := b[i].(map[string]any)
			if !ok {
				return false
			}
			for _, field := range []string{"name", "image", "command", "args", "env", "volumeMounts", "securityContext", "resources"} {
				if !equalJSON(wm[field], gm[field]) {
					return false
				}
			}
			for _, field := range []string{"envFrom", "volumeDevices", "lifecycle", "livenessProbe", "readinessProbe", "startupProbe"} {
				if !equalJSON(wm[field], gm[field]) {
					return false
				}
			}
		}
	}
	return true
}
func equalJSON(a, b any) bool {
	if emptyJSON(a) && emptyJSON(b) {
		return true
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func emptyJSON(value any) bool {
	if value == nil {
		return true
	}
	switch v := value.(type) {
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}
func readFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrCluster
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, ErrCluster
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrCluster
	}
	return raw, nil
}
func contains(want, got map[string]any) bool {
	for key, value := range want {
		if !equalJSON(value, got[key]) {
			return false
		}
	}
	return true
}
