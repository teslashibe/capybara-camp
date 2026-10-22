package provisioner

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/config"
	"github.com/teslashibe/capybara-camp/server/internal/testdb"
)

type clusterFixture struct {
	mu         sync.Mutex
	objects    map[string]object
	operations []string
	holdPods   bool
	onPod      func()
	t          *testing.T
}

func (f *clusterFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer fixture-cluster-token" {
		w.WriteHeader(401)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	at := -1
	for i, p := range parts {
		if p == "namespaces" {
			at = i
			break
		}
	}
	if at < 0 || len(parts) < at+3 || parts[at+1] != "capybara-runners" {
		w.WriteHeader(404)
		return
	}
	resource := parts[at+2]
	name := ""
	if len(parts) > at+3 {
		name = parts[at+3]
	}
	key := resource + "/" + name
	f.mu.Lock()
	defer f.mu.Unlock()
	f.operations = append(f.operations, r.Method+" "+key)
	write := func(value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	switch r.Method {
	case "GET":
		if name != "" {
			value, ok := f.objects[key]
			if !ok {
				w.WriteHeader(404)
				return
			}
			write(value)
			return
		}
		var items []object
		for id, value := range f.objects {
			if !strings.HasPrefix(id, resource+"/") {
				continue
			}
			keep := true
			for _, pair := range strings.Split(r.URL.Query().Get("labelSelector"), ",") {
				label, want, ok := strings.Cut(pair, "=")
				if !ok || value.Metadata.Labels[label] != want {
					keep = false
				}
			}
			if keep {
				items = append(items, value)
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Metadata.Name < items[j].Metadata.Name })
		write(map[string]any{"items": items, "metadata": map[string]any{}})
	case "POST":
		var value object
		if json.NewDecoder(r.Body).Decode(&value) != nil {
			w.WriteHeader(400)
			return
		}
		key = resource + "/" + value.Metadata.Name
		if _, ok := f.objects[key]; ok {
			w.WriteHeader(409)
			return
		}
		if resource == "pods" && f.onPod != nil {
			hook := f.onPod
			f.onPod = nil
			f.mu.Unlock()
			hook()
			f.mu.Lock()
		}
		value.Metadata.UID = uuid.NewString()
		value.Metadata.ResourceVersion = "1"
		if resource == "pods" {
			for _, field := range []string{"hostNetwork", "hostPID", "hostIPC"} {
				if value.Spec[field] == false {
					delete(value.Spec, field)
				}
			}
			value.Status = &podStatus{Phase: "Running"}
			value.Status.Conditions = []podCondition{{Type: "Ready", Status: "True"}}
		}
		f.objects[key] = value
		w.WriteHeader(201)
		write(value)
	case "DELETE":
		value, ok := f.objects[key]
		if !ok {
			w.WriteHeader(404)
			return
		}
		var options struct {
			Preconditions      struct{ UID, ResourceVersion string }
			GracePeriodSeconds *int
		}
		if json.NewDecoder(r.Body).Decode(&options) != nil || options.Preconditions.UID != value.Metadata.UID || options.Preconditions.ResourceVersion != value.Metadata.ResourceVersion || options.GracePeriodSeconds != nil {
			f.t.Error("unsafe delete options")
			w.WriteHeader(409)
			return
		}
		if resource == "pods" && f.holdPods {
			now := time.Now()
			value.Metadata.DeletionTimestamp = &now
			f.objects[key] = value
		} else {
			delete(f.objects, key)
		}
		write(map[string]any{"kind": "Status", "status": "Success"})
	default:
		w.WriteHeader(405)
	}
}
func fixture(t *testing.T) (*camp.Store, auth.Actor, camp.ProviderAccount, *Controller, *clusterFixture) {
	t.Helper()
	pool := testdb.Open(t)
	s, err := camp.NewWithProviderKey(pool, "test-receipts", bytes.Repeat([]byte{22}, 32))
	if err != nil {
		t.Fatal(err)
	}
	owner := auth.Actor{UserID: uuid.NewString(), Email: "owner@example.com", Name: "Owner"}
	ctx := context.Background()
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,$2,$3)`, owner.UserID, owner.Email, owner.Name); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateProviderAccount(ctx, owner, uuid.NewString(), camp.ProviderAccountInput{Provider: "Claude", Label: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	f := &clusterFixture{objects: map[string]object{}, t: t}
	k, cfg := testKubeClient(t, f)
	return s, owner, a, New(s, k, cfg), f
}

func testKubeClient(t *testing.T, handler http.Handler) (*kubeClient, config.Controller) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, "ca"), ca, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("fixture-cluster-token"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Controller{DatabaseURL: "unused", ProviderKey: "unused", APIOrigin: "https://camp.example", KubeOrigin: server.URL, KubeTokenFile: filepath.Join(dir, "token"), KubeCAFile: filepath.Join(dir, "ca"), Namespace: "capybara-runners", RunnerImage: "example.invalid/camp@sha256:" + strings.Repeat("a", 64), RuntimeClass: "runsc", StorageClass: "local-path", Capacity: 4}
	k, err := NewKubeClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return k, cfg
}

func TestKubeClientDoesNotForwardCredentialsToRedirect(t *testing.T) {
	var destinationCalls int
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls++ }))
	defer destination.Close()
	k, _ := testKubeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	_, err := k.call(t.Context(), "GET", "pods", "", nil, nil, &object{})
	if !errors.Is(err, ErrCluster) || destinationCalls != 0 {
		t.Fatal("cluster redirect followed or accepted")
	}
}

func TestKubeClientReloadsProjectedTokenAndBoundsResponses(t *testing.T) {
	var expected = "fixture-cluster-token"
	var oversize bool
	k, cfg := testKubeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expected {
			t.Error("client cached a projected token")
		}
		if oversize {
			_, _ = w.Write([]byte(strings.Repeat(" ", (2<<20)+1)))
			return
		}
		_, _ = w.Write([]byte(`{"kind":"Pod"}`))
	}))
	if _, err := k.call(t.Context(), "GET", "pods", "", nil, nil, &object{}); err != nil {
		t.Fatal(err)
	}
	expected = "rotated-fixture-token"
	if err := os.WriteFile(cfg.KubeTokenFile, []byte(expected), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := k.call(t.Context(), "GET", "pods", "", nil, nil, &object{}); err != nil {
		t.Fatal(err)
	}
	oversize = true
	if _, err := k.call(t.Context(), "GET", "pods", "", nil, nil, &object{}); !errors.Is(err, ErrCluster) {
		t.Fatal("unbounded cluster response")
	}
}
func TestControllerCreatesOnceAndCleansInOrder(t *testing.T) {
	s, owner, a, c, f := fixture(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := c.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	if len(f.objects) != 4 {
		t.Fatal("duplicate resources", len(f.objects))
	}
	f.mu.Unlock()
	current, err := s.ProviderAccountStatus(ctx, owner, a.ID)
	if err != nil || current.Account.WorkloadState != "ready" {
		t.Fatal("runner not ready", err)
	}
	g, err := s.EnsureControllerGrant(ctx, current.Account, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DisconnectProviderAccount(ctx, owner, a.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.holdPods = true
	f.mu.Unlock()
	for i := 0; i < 3; i++ {
		if err = c.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	erasing, err := s.ProviderAccountStatus(ctx, owner, a.ID)
	if err != nil || erasing.Account.WorkloadState != "erasing" {
		t.Fatal("deletion showed startup state", err)
	}
	f.mu.Lock()
	if _, ok := f.objects["persistentvolumeclaims/"+resourceName(g.ID)]; !ok {
		t.Error("removed volume before Pod termination")
	}
	delete(f.objects, "pods/"+resourceName(g.ID))
	f.holdPods = false
	f.mu.Unlock()
	for i := 0; i < 4; i++ {
		if err = c.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	current, err = s.ProviderAccountStatus(ctx, owner, a.ID)
	if err != nil || current.Account.State != "disconnected" || current.Account.WorkloadState != "erased" {
		t.Fatal("cleanup not acknowledged", err)
	}
	f.mu.Lock()
	if len(f.objects) != 1 {
		t.Error("private resources survived cleanup")
	}
	f.mu.Unlock()
	if _, err = s.ReactivateProviderAccount(ctx, owner, a.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if err = c.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	current, err = s.ProviderAccountStatus(ctx, owner, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.EnsureControllerGrant(ctx, current.Account, 4)
	if err != nil || fresh.ID == g.ID {
		t.Fatal("reactivation reused old grant", err)
	}
}
func TestControllerDoesNotPublishStaleReadyStatus(t *testing.T) {
	s, owner, a, c, f := fixture(t)
	ctx := context.Background()
	f.onPod = func() {
		if _, err := s.DisconnectProviderAccount(ctx, owner, a.ID, uuid.NewString()); err != nil {
			t.Error(err)
		}
	}
	if err := c.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := s.ProviderAccountStatus(ctx, owner, a.ID)
	if err != nil || current.Account.State != "disconnecting" || current.Account.WorkloadState == "ready" {
		t.Fatal("stale creation changed new epoch", err)
	}
	for i := 0; i < 4; i++ {
		if err = c.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	current, err = s.ProviderAccountStatus(ctx, owner, a.ID)
	if err != nil || current.Account.State != "disconnected" {
		t.Fatal("orphaned stale creation", err)
	}
}

func TestCleanupCannotDeleteNewAccountEpoch(t *testing.T) {
	s, owner, a, c, f := fixture(t)
	ctx := context.Background()
	if err := c.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	oldGrant, err := s.EnsureControllerGrant(ctx, a, 4)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.DisconnectProviderAccount(ctx, owner, a.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgeRunnerErased(ctx, oldGrant.Token); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.ReactivateProviderAccount(ctx, owner, a.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.EnsureControllerGrant(ctx, fresh, 4)
	if err != nil {
		t.Fatal(err)
	}
	_, _, pod := resources(c.config, g)
	pod.Metadata.UID = uuid.NewString()
	pod.Metadata.ResourceVersion = "1"
	f.mu.Lock()
	f.objects["pods/"+pod.Metadata.Name] = pod
	before := len(f.operations)
	f.mu.Unlock()
	err = c.clean(ctx, old.InstallationID, "", func(ctx context.Context) (bool, error) { return s.ControllerCleanupCurrent(ctx, old) })
	if !errors.Is(err, camp.ErrTransition) {
		t.Fatal("stale cleanup did not stop", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.objects["pods/"+pod.Metadata.Name]; !ok {
		t.Fatal("deleted new epoch")
	}
	for _, op := range f.operations[before:] {
		if strings.HasPrefix(op, "DELETE ") {
			t.Fatal("stale cleanup deleted a resource")
		}
	}
}

func TestCleanupStopsWhenGrantWasReplaced(t *testing.T) {
	s, owner, a, c, f := fixture(t)
	ctx := context.Background()
	if err := c.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := s.EnsureControllerGrant(ctx, a, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateRunnerGrant(ctx, owner, a.ID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	newGrant := camp.ControllerGrant{ID: uuid.NewString(), Account: a}
	_, _, pod := resources(c.config, newGrant)
	pod.Metadata.UID = uuid.NewString()
	pod.Metadata.ResourceVersion = "1"
	f.mu.Lock()
	f.objects["pods/"+pod.Metadata.Name] = pod
	before := len(f.operations)
	f.mu.Unlock()
	err = c.clean(ctx, a.InstallationID, resourceName(old.ID), func(ctx context.Context) (bool, error) { return s.ControllerGrantCurrent(ctx, old) })
	if !errors.Is(err, camp.ErrTransition) {
		t.Fatal("replaced grant kept deleting", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, op := range f.operations[before:] {
		if strings.HasPrefix(op, "DELETE ") {
			t.Fatal("stale grant deleted a resource")
		}
	}
}

func TestControllerDoesNotTreatRunningPhaseAsReady(t *testing.T) {
	s, owner, a, c, f := fixture(t)
	ctx := context.Background()
	if err := c.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	var key string
	for name, value := range f.objects {
		if strings.HasPrefix(name, "pods/") {
			key = name
			value.Status.Conditions = nil
			f.objects[name] = value
		}
	}
	f.mu.Unlock()
	if err := c.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := s.ProviderAccountStatus(ctx, owner, a.ID)
	if err != nil || status.Account.WorkloadState != "starting" {
		t.Fatal("running phase fabricated readiness", err)
	}
	f.mu.Lock()
	value := f.objects[key]
	value.Status.ContainerStatuses = []containerStatus{{}}
	value.Status.ContainerStatuses[0].State.Waiting = &struct {
		Reason string `json:"reason"`
	}{Reason: "CrashLoopBackOff"}
	f.objects[key] = value
	f.mu.Unlock()
	if err = c.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = s.ProviderAccountStatus(ctx, owner, a.ID)
	if err != nil || status.Account.WorkloadError != "workload_failed" {
		t.Fatal("crashed worker reported ready", err)
	}
}

func TestControllerHandlesInitStartupAndFailure(t *testing.T) {
	s, owner, a, c, f := fixture(t)
	ctx := context.Background()
	if err := c.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	g, err := s.EnsureControllerGrant(ctx, a, 4)
	if err != nil {
		t.Fatal(err)
	}
	key := "pods/" + resourceName(g.ID)
	for _, tc := range []struct {
		status, state, code string
	}{
		{`{"phase":"Pending","initContainerStatuses":[{"state":{"waiting":{"reason":"PodInitializing"}}}]}`, "starting", "workload_pending"},
		{`{"phase":"Pending","initContainerStatuses":[{"state":{"terminated":{"exitCode":1}}}]}`, "failed", "workload_failed"},
		{`{"phase":"Pending","initContainerStatuses":[{"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}`, "failed", "workload_failed"},
	} {
		var status podStatus
		if err = json.Unmarshal([]byte(tc.status), &status); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		value := f.objects[key]
		value.Status = &status
		f.objects[key] = value
		before := len(f.operations)
		f.mu.Unlock()
		if err = c.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		current, err := s.ProviderAccountStatus(ctx, owner, a.ID)
		if err != nil || current.Account.WorkloadState != tc.state || current.Account.WorkloadError != tc.code {
			t.Fatalf("wrong init status for %s: %s / %s (%v)", tc.status, current.Account.WorkloadState, current.Account.WorkloadError, err)
		}
		f.mu.Lock()
		for _, op := range f.operations[before:] {
			if strings.HasPrefix(op, "DELETE ") {
				t.Error("startup reset Kubernetes backoff")
			}
		}
		f.mu.Unlock()
	}
}

func TestControllerRecreatesTerminalPodPreservingJournalVolume(t *testing.T) {
	for _, phase := range []string{"Succeeded", "Failed"} {
		t.Run(phase, func(t *testing.T) {
			s, _, a, c, f := fixture(t)
			ctx := context.Background()
			if err := c.Pass(ctx); err != nil {
				t.Fatal(err)
			}
			g, err := s.EnsureControllerGrant(ctx, a, 4)
			if err != nil {
				t.Fatal(err)
			}
			name := resourceName(g.ID)
			f.mu.Lock()
			old := f.objects["pods/"+name]
			old.Status = &podStatus{Phase: phase}
			f.objects["pods/"+name] = old
			pvcID := f.objects["persistentvolumeclaims/"+name].Metadata.UID
			secretID := f.objects["secrets/"+name].Metadata.UID
			before := len(f.operations)
			f.holdPods = true
			f.mu.Unlock()
			for i := 0; i < 2; i++ {
				if err = c.Pass(ctx); err != nil {
					t.Fatal(err)
				}
			}
			f.mu.Lock()
			delete(f.objects, "pods/"+name)
			f.holdPods = false
			f.mu.Unlock()
			if err = c.Pass(ctx); err != nil {
				t.Fatal(err)
			}
			current, err := s.EnsureControllerGrant(ctx, a, 4)
			if err != nil || current.ID != g.ID {
				t.Fatal("terminal Pod replaced its grant", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.objects["pods/"+name].Metadata.UID == old.Metadata.UID || f.objects["persistentvolumeclaims/"+name].Metadata.UID != pvcID || f.objects["secrets/"+name].Metadata.UID != secretID {
				t.Fatal("worker did not restart with its original journal and grant")
			}
			for _, op := range f.operations[before:] {
				if strings.HasPrefix(op, "DELETE ") && op != "DELETE pods/"+name {
					t.Error("worker recovery removed private state")
				}
			}
		})
	}
}
func TestObjectValidationRefusesUnsafeChanges(t *testing.T) {
	_, _, a, c, _ := fixture(t)
	g := camp.ControllerGrant{ID: uuid.NewString(), Account: a, Token: "fixture"}
	_, _, desired := resources(c.config, g)
	clone := func() object {
		raw, _ := json.Marshal(desired)
		var out object
		_ = json.Unmarshal(raw, &out)
		return out
	}
	for _, field := range []string{"hostNetwork", "hostPID", "hostIPC", "automountServiceAccountToken"} {
		t.Run(field, func(t *testing.T) {
			got := clone()
			got.Spec[field] = true
			if matches(desired, got) {
				t.Fatal("unsafe Pod accepted")
			}
		})
	}
	got := clone()
	got.Spec["runtimeClassName"] = "runc"
	if matches(desired, got) {
		t.Fatal("sandbox downgrade accepted")
	}
	got = clone()
	container := got.Spec["containers"].([]any)[0].(map[string]any)
	container["securityContext"].(map[string]any)["privileged"] = true
	if matches(desired, got) {
		t.Fatal("privilege change accepted")
	}
	got = clone()
	got.Spec["containers"] = append(got.Spec["containers"].([]any), got.Spec["containers"].([]any)[0])
	if matches(desired, got) {
		t.Fatal("sidecar injected")
	}
	for _, key := range []string{"envFrom", "lifecycle", "readinessProbe"} {
		got = clone()
		got.Spec["containers"].([]any)[0].(map[string]any)[key] = map[string]any{"unexpected": true}
		if matches(desired, got) {
			t.Errorf("injected %s accepted", key)
		}
	}
	got = clone()
	for _, field := range []string{"hostNetwork", "hostPID", "hostIPC", "shareProcessNamespace"} {
		delete(got.Spec, field)
	}
	if !matches(desired, got) {
		t.Fatal("Kubernetes omitted safe false defaults")
	}
	got = clone()
	if !matches(desired, got) {
		t.Fatal("valid Pod rejected")
	}
	if fmt.Sprint(desired.Spec["runtimeClassName"]) != "runsc" {
		t.Fatal("missing sandbox")
	}
}
