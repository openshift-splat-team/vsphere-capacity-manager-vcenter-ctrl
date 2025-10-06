/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/vsphere"
	v1 "github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"
)

// LeaseReconciler reconciles a Lease object
type LeaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	*vsphere.Metadata

	leases  map[string]*v1.Lease
	leaseMu sync.Mutex

	logger logr.Logger
}

//+kubebuilder:rbac:groups=vspherecapacitymanager.splat.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=vspherecapacitymanager.splat.io,resources=leases/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=vspherecapacitymanager.splat.io,resources=leases/finalizers,verbs=update

// Reconcile manages lease lifecycle and resource cleanup.
// Handles fulfilled lease caching and leak detection for vSphere resources.
// Performs cleanup when leases are deleted from the cluster.
func (r *LeaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var reconciledLease v1.Lease
	r.logger.WithName("reconcile").WithValues("lease_name", req.Name, "lease_namespace", req.Namespace)

	r.logger.WithName("leases").Info("starting reconciliation", "cached_leases_count", len(r.leases))

	if err := r.Get(ctx, client.ObjectKey{Name: req.Name, Namespace: req.Namespace}, &reconciledLease); err != nil {
		if err = client.IgnoreNotFound(err); err != nil {
			r.logger.Error(err, "failed to get lease from API server")
			return ctrl.Result{}, err
		}

		r.logger.WithName("leases").Info("lease not found, assuming it was deleted", "name", req.Name)

		// If lease was not found it was deleted, start clean up process
		// make a copy of the lease, lock, delete the lease from cache, unlock, delete virtual machines if they exist
		if l, err := r.getLeaseByName(req.Name); err != nil {
			r.logger.WithName("leases").Error(err, "lease not found in cache", "name", req.Name)
		} else {
			r.logger.WithName("leases").Info("lease found in cache, starting cleanup process", "name", l.Name, "creation_timestamp", l.CreationTimestamp.Format(time.RFC3339))

			r.logger.WithName("leases").Info("getting managed entities by cluster id")
			managedEntities, err := r.getManagedEntitiesByClusterId(ctx, l.Name)
			if err != nil {
				r.logger.Error(err, "failed to get ManagedEntitiesByClusterId", "lease_name", l.Name)
			}

			if len(managedEntities) != 0 {
				r.logger.WithName("leases").Info("getting children of managed entities")
				tempChildMe, err := r.childrenOfFolder(ctx, managedEntities, l.Name)
				if err != nil {
					return ctrl.Result{}, err
				}

				managedEntities = append(managedEntities, tempChildMe...)

				r.logger.WithName("leases").Info("deleting managed entities")
				if err := r.deleteByManagedEntity(ctx, managedEntities, l.Name); err != nil {
					r.logger.Error(err, "failed to delete ManagedEntitiesByClusterId", "lease_name", l.Name)
				}
			}

			r.logger.WithName("leases").Info("deleting managed entities")
			if err := r.deleteTagsByClusterId(ctx, l.Name); err != nil {
				r.logger.Error(err, "failed to cleanup leaked tags")
				return ctrl.Result{}, err
			}

			r.leaseMu.Lock()
			delete(r.leases, req.Name)
			r.leaseMu.Unlock()
			r.logger.WithName("cleanup").Info("completed cleanup for deleted lease", "lease_name", l.Name)
		}

		return ctrl.Result{}, nil
	}

	// cache lease once its been fulfilled
	if reconciledLease.Status.Phase == v1.PHASE_FULFILLED {
		var ok bool
		r.logger.WithName("leases").Info("processing fulfilled lease", "phase", reconciledLease.Status.Phase)

		r.leaseMu.Lock()
		if _, ok = r.leases[reconciledLease.Name]; !ok {
			r.logger.WithName("leases").Info("caching fulfilled lease for monitoring", "name", reconciledLease.Name, "creation_timestamp", reconciledLease.CreationTimestamp.Format(time.RFC3339))
			r.leases[reconciledLease.Name] = &reconciledLease
		}
		r.leaseMu.Unlock()

		// only add cache and check for leaking once
		if !ok {
			r.logger.WithName("monitoring").Info("starting leak detection for fulfilled lease", "lease_name", reconciledLease.Name)

			// Is there _any_ *soap* based objects that exist in any vCenter based on the cluster id
			managedEntities, err := r.getManagedEntitiesByClusterId(ctx, reconciledLease.Name)
			if err != nil {
				return ctrl.Result{}, err
			}

			switch {
			case len(managedEntities) == 0:
				// there are no cluster objects with that name, if there are any virtual machines running
				// on the port group destroy them
				if err := r.deleteVirtualMachinesByPortGroup(ctx, reconciledLease.Name); err != nil {
					return ctrl.Result{}, err
				}
				// I know below is not necessary, thinking about future changes
				// where we know the types of managedentities with the cluster id
				// and how we could better approach removing left over objects
			case len(managedEntities) == 1:
				for _, managedEntity := range managedEntities {
					r.logger.Info("processing managed object", "name", managedEntity.Name, "type", managedEntity.Reference().Type)
				}

			case len(managedEntities) > 1:
				for _, managedEntity := range managedEntities {
					r.logger.Info("processing managed object", "name", managedEntity.Name, "type", managedEntity.Reference().Type)
				}
			}
		} else {
			r.logger.WithName("leases").Info("lease already cached, skipping leak check", "name", reconciledLease.Name)
		}
	}

	return ctrl.Result{}, nil
}

// getLeaseByName retrieves a cached lease by name.
// Returns error if lease is not found in the local cache.
func (r *LeaseReconciler) getLeaseByName(name string) (*v1.Lease, error) {
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()

	if lease, ok := r.leases[name]; ok {
		return lease, nil
	}
	return nil, fmt.Errorf("lease %s not found", name)
}

// getFilteredVirtualMachines retrieves VMs excluding those belonging to the cluster.
// Filters out VMs with cluster ID in name/parent or RHCOS VMs.
func (r *LeaseReconciler) getFilteredVirtualMachines(ctx context.Context, moRefs []types.ManagedObjectReference, server, clusterId string) ([]mo.VirtualMachine, error) {
	var virtualMachines []mo.VirtualMachine
	s, err := r.Metadata.Session(ctx, server)
	if err != nil {
		return nil, err
	}

	pc := property.DefaultCollector(s.Client.Client)
	if err := pc.Retrieve(ctx, moRefs, nil, &virtualMachines); err != nil {
		return nil, err
	}

	var toReturn []mo.VirtualMachine

	for _, vm := range virtualMachines {
		var parentMe mo.ManagedEntity

		// Parent managed entity of the virtual machine, this _should_ be a folder that was created
		// if its "vm" then the virtual machine should be deleted
		if vm.Parent != nil {
			if err := pc.RetrieveOne(ctx, *vm.Parent, []string{"name"}, &parentMe); err != nil {
				return nil, err
			}
			if strings.Contains(parentMe.Name, clusterId) {
				continue
			}
		}

		switch {
		case strings.Contains(vm.Name, clusterId):
			continue
		case strings.HasPrefix(vm.Name, "rhcos-"):
			continue
		}
		toReturn = append(toReturn, vm)
	}
	return toReturn, nil
}

// deleteVirtualMachinesByPortGroup removes VMs from lease port groups.
// Only deletes VMs created before the lease to avoid removing cluster VMs.
func (r *LeaseReconciler) deleteVirtualMachinesByPortGroup(ctx context.Context, leaseName string) error {
	var clusterId string
	var ok bool

	l, err := r.getLeaseByName(leaseName)
	if err != nil {
		return err
	}

	if clusterId, ok = l.Labels["cluster-id"]; !ok {
		return fmt.Errorf("cluster id not found in lease")
	}

	for server := range r.Metadata.VCenterCredentials {
		v, err := r.Metadata.ContainerView(ctx, server)
		if err != nil {
			return err
		}

		for _, network := range l.Status.Topology.Networks {
			portGroupName := path.Base(network)
			var networks []mo.Network
			if err := v.RetrieveWithFilter(ctx, []string{"Network"}, []string{}, &networks, property.Match{"name": portGroupName}); err != nil {
				return err
			}

			for _, n := range networks {
				if len(n.Vm) > 0 {
					vms, err := r.getFilteredVirtualMachines(ctx, n.Vm, server, clusterId)
					if err != nil {
						return err
					}
					for _, vm := range vms {
						if vm.Config.CreateDate.Before(l.CreationTimestamp.Time) {
							if err := r.deleteVirtualMachine(ctx, server, vm.Reference()); err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}

	return nil
}

// deleteVirtualMachine powers off and destroys a VM.
// Ensures VM is powered off before destruction.
func (r *LeaseReconciler) deleteVirtualMachine(ctx context.Context, server string, ref types.ManagedObjectReference) error {
	s, err := r.Metadata.Session(ctx, server)
	if err != nil {
		return err
	}
	vmObj := object.NewVirtualMachine(s.Client.Client, ref)

	objName, err := vmObj.ObjectName(ctx)
	if err != nil {
		return err
	}
	r.logger.Info("deleting virtual machine", "name", objName)

	if powerState, err := vmObj.PowerState(ctx); err == nil {
		r.logger.Info("virtual machine power state", "name", objName, "power_state", powerState)
		if powerState == types.VirtualMachinePowerStatePoweredOn {
			if task, err := vmObj.PowerOff(ctx); err == nil {
				if err := task.Wait(ctx); err != nil {
					return err
				}
			}
		}
	}

	destroyTask, err := vmObj.Destroy(ctx)
	if err != nil {
		return err
	}
	if err := destroyTask.Wait(ctx); err != nil {
		return err
	}

	return nil
}

// getManagedEntitiesByClusterId finds vSphere objects by cluster ID.
// Searches across all vCenter servers for entities matching cluster ID pattern.
func (r *LeaseReconciler) getManagedEntitiesByClusterId(ctx context.Context, leaseName string) ([]mo.ManagedEntity, error) {
	var clusterId string
	var ok bool
	var managedEntities []mo.ManagedEntity

	l, err := r.getLeaseByName(leaseName)
	if err != nil {
		return nil, err
	}

	if clusterId, ok = l.Labels["cluster-id"]; !ok {
		return nil, fmt.Errorf("cluster id not found in lease")
	}

	v, err := r.Metadata.ContainerView(ctx, l.Status.Server)
	if err != nil {
		return nil, err
	}

	if err := v.RetrieveWithFilter(ctx, []string{"ManagedEntity"}, nil, &managedEntities, property.Match{"name": fmt.Sprintf("%s*", clusterId)}); err != nil {
		if strings.Contains(err.Error(), "object references is empty") {
			return nil, nil
		}
		return nil, err
	}

	r.logger.Info("managed entities", "cluster-id", clusterId, "count", len(managedEntities))

	return managedEntities, nil
}

// toManagedObjectRefs converts object references to managed object references.
func toManagedObjectRefs(objs []object.Reference) []types.ManagedObjectReference {
	refs := make([]types.ManagedObjectReference, len(objs))
	for i, obj := range objs {
		refs[i] = obj.Reference()
	}
	return refs
}

// childrenOfFolder retrieves child objects from folder entities.
// Recursively finds all objects within cluster folders.
func (r *LeaseReconciler) childrenOfFolder(ctx context.Context, managedEntities []mo.ManagedEntity, leaseName string) ([]mo.ManagedEntity, error) {
	l, err := r.getLeaseByName(leaseName)
	if err != nil {
		return nil, err
	}

	var toReturnMe []mo.ManagedEntity
	s, err := r.Metadata.Session(ctx, l.Status.Server)
	if err != nil {
		return nil, err
	}
	pc := property.DefaultCollector(s.Client.Client)

	for _, managedEntity := range managedEntities {
		if managedEntity.Reference().Type == "Folder" {
			var tempChildMe []mo.ManagedEntity

			folder := object.NewFolder(s.Client.Client, managedEntity.Reference())
			children, err := folder.Children(ctx)
			if err != nil {
				return nil, err
			}
			childRefs := toManagedObjectRefs(children)

			if err := pc.Retrieve(ctx, childRefs, nil, &tempChildMe); err != nil {
				return nil, err
			}
			toReturnMe = append(toReturnMe, tempChildMe...)
		}
	}
	return toReturnMe, nil
}

// deleteByManagedEntity destroys vSphere managed entities.
// Removes folders, VMs, and other cluster-related objects.
func (r *LeaseReconciler) deleteByManagedEntity(ctx context.Context, managedEntities []mo.ManagedEntity, leaseName string) error {
	l, err := r.getLeaseByName(leaseName)
	if err != nil {
		return err
	}

	s, err := r.Metadata.Session(ctx, l.Status.Server)
	if err != nil {
		return err
	}

	for _, managedEntity := range managedEntities {
		if managedEntity.Reference().Type == "VirtualMachine" {
			if err := r.deleteVirtualMachine(ctx, l.Status.Server, managedEntity.Reference()); err != nil {
				return err
			}
		} else {
			common := object.NewCommon(s.Client.Client, managedEntity.Reference())
			task, err := common.Destroy(ctx)
			if err != nil {
				r.logger.Error(err, "failed to destroy managed entity", "entity", managedEntity.Reference())
				continue
			}

			if err := task.Wait(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteTagsByClusterId removes vSphere tags associated with cluster.
// Cleans up tag categories containing cluster ID across all vCenters.
func (r *LeaseReconciler) deleteTagsByClusterId(ctx context.Context, leaseName string) error {
	var clusterId string
	var ok bool
	lease, err := r.getLeaseByName(leaseName)
	if err != nil {
		return err
	}
	if clusterId, ok = lease.ObjectMeta.Labels["cluster-id"]; !ok {
		return fmt.Errorf("cluster id not found in lease")
	}

	r.logger.WithName("tags").Info("starting tag cleanup check", "lease_name", lease.Name)

	for server := range r.Metadata.VCenterCredentials {
		if err := r.cleanupTagsOnServer(ctx, server, clusterId); err != nil {
			return err
		}
	}

	r.logger.WithName("tags").Info("completed tag cleanup check", "lease_name", lease.Name)
	return nil
}

// cleanupTagsOnServer removes vSphere tags for a specific server.
func (r *LeaseReconciler) cleanupTagsOnServer(ctx context.Context, server, clusterId string) error {
	r.logger.WithName("vcenter").Info("checking vCenter for leaked tags", "vcenter", server)

	s, err := r.Metadata.Session(ctx, server)
	if err != nil {
		r.logger.Error(err, "failed to establish vCenter session for tag cleanup", "vcenter", server)
		return err
	}

	categories, err := s.TagManager.GetCategories(ctx)
	if err != nil {
		r.logger.Error(err, "failed to retrieve tag categories", "vcenter", server)
		return err
	}

	r.logger.WithName("vcenter").Info("retrieved tag categories", "vcenter", server, "categories_count", len(categories))

	deletedCount := 0
	for _, c := range categories {
		if strings.Contains(c.Name, clusterId) {
			r.logger.WithName("vcenter").Info("deleting tag category", "vcenter", server, "category_name", c.Name)

			if err = s.TagManager.DeleteCategory(ctx, &c); err != nil {
				r.logger.Error(err, "failed to delete tag category", "vcenter", server, "category_name", c.Name)
				return err
			}
			deletedCount++
		}
	}
	r.logger.WithName("tags").Info("completed tag category cleanup", "vcenter", server, "deleted_count", deletedCount)
	return nil
}

// SetupWithManager initializes the lease reconciler with controller manager.
// Configures logging and event filtering for lease resources.
func (r *LeaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.leases = make(map[string]*v1.Lease)
	zapLog, err := zap.NewProduction(zap.AddCaller())
	if err != nil {
		return err
	}

	r.logger = zapr.NewLogger(zapLog)

	return ctrl.NewControllerManagedBy(mgr).For(&v1.Lease{}).WithEventFilter(predicate.Funcs{
		CreateFunc:  func(createEvent event.CreateEvent) bool { return true },
		DeleteFunc:  func(deleteEvent event.DeleteEvent) bool { return true },
		UpdateFunc:  func(updateEvent event.UpdateEvent) bool { return true },
		GenericFunc: func(genericEvent event.GenericEvent) bool { return false },
	}).Complete(r)
}
