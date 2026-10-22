package provisioner

import (
	"context"
	"errors"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/camp"
	"github.com/teslashibe/capybara-camp/server/internal/config"
)

type Store interface {
	ControllerAccounts(context.Context, string, int) ([]camp.ProviderAccount, error)
	EnsureControllerGrant(context.Context, camp.ProviderAccount, int) (camp.ControllerGrant, error)
	ControllerGrantCurrent(context.Context, camp.ControllerGrant) (bool, error)
	ControllerCleanupCurrent(context.Context, camp.ProviderAccount) (bool, error)
	ControllerWorkloadStatus(context.Context, camp.ProviderAccount, string, string) error
	ControllerErased(context.Context, camp.ProviderAccount) error
}
type Controller struct {
	store  Store
	kube   *kubeClient
	config config.Controller
	cursor string
}

func New(store Store, kube *kubeClient, c config.Controller) *Controller {
	return &Controller{store: store, kube: kube, config: c}
}

func (c *Controller) Run(ctx context.Context, report func(error)) error {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		if err := c.Pass(ctx); err != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func (c *Controller) Pass(ctx context.Context) error {
	accounts, err := c.store.ControllerAccounts(ctx, c.cursor, 64)
	if err != nil {
		return err
	}
	if len(accounts) == 64 {
		c.cursor = accounts[len(accounts)-1].ID
	} else {
		c.cursor = ""
	}
	_, infrastructure := c.kube.ensure(ctx, "networkpolicies", isolationPolicy(c.config.Namespace))
	for _, a := range accounts {
		if err := ctx.Err(); err != nil {
			return err
		}
		var work error
		if infrastructure != nil && a.State != "disconnecting" && a.State != "disconnected" {
			work = infrastructure
		} else {
			work = c.reconcile(ctx, a)
		}
		state, code := "ready", ""
		switch {
		case work == nil:
			if a.State == "disconnecting" || a.State == "disconnected" {
				state = "erased"
			}
		case errors.Is(work, camp.ErrTransition):
			continue
		case errors.Is(work, ErrPending):
			state, code = "starting", "workload_pending"
			if a.State == "disconnecting" || a.State == "disconnected" {
				state, code = "erasing", ""
			}
		case errors.Is(work, ErrWorkloadFailed):
			state, code = "failed", "workload_failed"
		case errors.Is(work, camp.ErrRunnerCapacity):
			state, code = "pending", "capacity"
		case errors.Is(work, camp.ErrManualRunner):
			state, code = "failed", "manual_grant"
		case errors.Is(work, ErrConflict):
			state, code = "failed", "resource_conflict"
		default:
			state, code = "failed", "cluster_unavailable"
		}
		if err := c.store.ControllerWorkloadStatus(ctx, a, state, code); err != nil {
			return err
		}
	}
	return infrastructure
}

func (c *Controller) reconcile(ctx context.Context, a camp.ProviderAccount) error {
	if a.State == "disconnecting" || a.State == "disconnected" {
		if err := c.clean(ctx, a.InstallationID, "", "", func(ctx context.Context) (bool, error) { return c.store.ControllerCleanupCurrent(ctx, a) }); err != nil {
			return err
		}
		return c.store.ControllerErased(ctx, a)
	}
	g, err := c.store.EnsureControllerGrant(ctx, a, c.config.Capacity)
	if err != nil {
		return err
	}
	name := resourceName(g.ID)
	if err = c.clean(ctx, a.InstallationID, name, workspaceName(a.InstallationID, a.Epoch), func(ctx context.Context) (bool, error) { return c.store.ControllerGrantCurrent(ctx, g) }); err != nil {
		return err
	}
	secret, pvc, pod := resources(c.config, g)
	for _, item := range []struct {
		resource string
		object   object
	}{{"secrets", secret}, {"persistentvolumeclaims", pvc}, {"persistentvolumeclaims", workspaceVolume(c.config, g)}, {"pods", pod}} {
		current, err := c.store.ControllerGrantCurrent(ctx, g)
		if err != nil {
			return err
		}
		if !current {
			return camp.ErrTransition
		}
		actual, err := c.kube.ensure(ctx, item.resource, item.object)
		if err != nil {
			return err
		}
		if item.resource == "pods" {
			if actual.Status == nil {
				return ErrPending
			}
			if actual.Status.Phase == "Failed" || actual.Status.Phase == "Succeeded" {
				// Recreate a terminal Pod using its existing grant and PVC. Keeping
				// the journal preserves the runner's recovery fence across restarts.
				current, err := c.store.ControllerGrantCurrent(ctx, g)
				if err != nil {
					return err
				}
				if !current {
					return camp.ErrTransition
				}
				if err = c.kube.remove(ctx, "pods", actual); err != nil {
					return err
				}
				return ErrPending
			}
			for _, container := range actual.Status.ContainerStatuses {
				if container.failed() {
					return ErrWorkloadFailed
				}
			}
			for _, container := range actual.Status.InitContainerStatuses {
				if container.failed() {
					return ErrWorkloadFailed
				}
			}
			ready := false
			for _, condition := range actual.Status.Conditions {
				if condition.Type == "Ready" && condition.Status == "True" {
					ready = true
				}
			}
			if actual.Status.Phase != "Running" || !ready {
				return ErrPending
			}
		}
	}
	return nil
}

func (c *Controller) clean(ctx context.Context, installation, keep, keepWorkspace string, current func(context.Context) (bool, error)) error {
	for _, resource := range []string{"pods", "persistentvolumeclaims", "secrets"} {
		objects, err := c.kube.list(ctx, resource, installation)
		if err != nil {
			return err
		}
		pending := false
		for _, obj := range objects {
			if !owned(obj, installation) {
				return ErrConflict
			}
			if obj.Metadata.Name == keep || obj.Metadata.Name == keepWorkspace {
				continue
			}
			pending = true
			valid, err := current(ctx)
			if err != nil {
				return err
			}
			if !valid {
				return camp.ErrTransition
			}
			if obj.Metadata.DeletionTimestamp == nil {
				if err := c.kube.remove(ctx, resource, obj); err != nil {
					return err
				}
			}
		}
		// Observe each layer gone before removing the next one. In particular,
		// never remove credentials/storage while a Pod may still be running.
		if pending {
			return ErrPending
		}
	}
	return nil
}
