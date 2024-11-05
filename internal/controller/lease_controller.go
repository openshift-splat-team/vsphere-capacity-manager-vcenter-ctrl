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
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/vsphere"
	"github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"
)

// LeaseReconciler reconciles a Lease object
type LeaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	*vsphere.Metadata

	lastCheck time.Time
}

var (
	leases  map[string]*v1.Lease
	leaseMu sync.Mutex

	leakMu sync.Mutex
)

//+kubebuilder:rbac:groups=vspherecapacitymanager.splat.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=vspherecapacitymanager.splat.io,resources=leases/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=vspherecapacitymanager.splat.io,resources=leases/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Lease object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.16.3/pkg/reconcile
func (r *LeaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	/*
		opts := zap.Options{
			Development: true,
		}

		log.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	*/
	logger := log.FromContext(ctx)

	lease := &v1.Lease{}

	logger.WithName("leases").Info("", "number", len(leases))

	if err := r.Get(ctx, client.ObjectKey{Name: req.Name, Namespace: req.Namespace}, lease); err != nil {
		// error is Not NotFound, return
		if err = client.IgnoreNotFound(err); err != nil {
			return ctrl.Result{}, err
		}

		logger.WithName("leases").Info("not found, assuming it was deleted", "name", req.Name)

		// If lease was not found it was deleted, start clean up process
		// make a copy of the lease, lock, delete the lease from cache, unlock, delete virtual machines if they exist
		if lease, ok := leases[req.Name]; ok {
			logger.WithName("leases").Info("available for leak check", "name", lease.Name)
			leaseCopy := lease.DeepCopy()

			leaseMu.Lock()
			delete(leases, req.Name)
			leaseMu.Unlock()

			if err := checkLeasedNetworkForLeakedVirtualMachines(ctx, leaseCopy, r.Metadata, true, logger); err != nil {
				return ctrl.Result{}, err
			}

			if err := checkLeaseForLeakedFolders(ctx, leaseCopy, r.Metadata, true, logger); err != nil {
				return ctrl.Result{}, err
			}

			if err := checkLeaseForLeakedTags(ctx, leaseCopy, r.Metadata, true, logger); err != nil {
				return ctrl.Result{}, err
			}

			leaseCopy = nil

		} else {
			logger.WithName("leases").Info("potentially did not clean up deleted lease", "name", req.NamespacedName)
		}

		return ctrl.Result{}, nil
	}

	if lease.DeletionTimestamp != nil {
		logger.WithName("leases").Info("was deleted", "at", lease.DeletionTimestamp.String())
	}

	// cache lease once its been fulfilled
	if lease.Status.Phase == v1.PHASE_FULFILLED {
		// only add cache and check for leaking once
		if _, ok := leases[lease.Name]; !ok {
			leaseMu.Lock()
			leases[lease.Name] = lease
			logger.WithName("leases").Info("cached was created", "name", lease.Name, "at", lease.CreationTimestamp.String())
			leaseMu.Unlock()
			if err := checkLeasedNetworkForLeakedVirtualMachines(ctx, lease, r.Metadata, false, logger); err != nil {
				return ctrl.Result{}, err
			}
		}
	}
	//r.checkLeakage()

	return ctrl.Result{}, nil
}

/*
	func (r *LeaseReconciler) checkLeakage() {
		pastTime := time.Now().Add(time.Hour * -8)

		leakMu.Lock()
		if pastTime.After(r.lastCheck) {
			r.lastCheck = time.Now()
			 * cns volumes
			 * folders
			 * tags
			 * rp
			 * storage policies
		}
		leakMu.Unlock()
	}
*/
func checkLeaseForLeakedTags(ctx context.Context, lease *v1.Lease, metadata *vsphere.Metadata, leaseDeleted bool, logger logr.Logger) error {
	for server, _ := range metadata.VCenterCredentials {
		logger.WithName("vcenter").Info("for leaked tags", "vcenter", server)
		s, err := metadata.Session(ctx, server)
		if err != nil {
			return err
		}

		categories, err := s.TagManager.GetCategories(ctx)
		if err != nil {
			return err
		}
		logger.WithName("vcenter").Info("for leaked tags categories", "vcenter", server, "number", len(categories))

		if clusterId, ok := lease.ObjectMeta.Labels["cluster-id"]; ok && leaseDeleted {
			for _, c := range categories {
				if strings.Contains(c.Name, clusterId) {
					logger.WithName("vcenter").Info("deleting tag category ", "vcenter", server, "name", c.Name)
					if err = s.TagManager.DeleteCategory(ctx, &c); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
func checkLeaseForLeakedFolders(ctx context.Context, lease *v1.Lease, metadata *vsphere.Metadata, leaseDeleted bool, logger logr.Logger) error {
	for server, _ := range metadata.VCenterCredentials {
		s, err := metadata.Session(ctx, server)
		if err != nil {
			return err
		}

		folders, err := s.Finder.FolderList(ctx, "*")
		if err != nil {
			return err
		}
		logger.WithName("vcenter").Info("checking for leaked folders", "vcenter", server, "number", len(folders))

		if clusterId, ok := lease.ObjectMeta.Labels["cluster-id"]; ok && leaseDeleted {
			for _, f := range folders {
				if strings.Contains(f.Name(), clusterId) {
					logger.Info(fmt.Sprintf("\tdeleting folder %s", f.Name()))
					logger.WithName("vcenter").Info("deleting", "vcenter", server, "folder", f.Name())
					var task *object.Task
					if task, err = f.Destroy(ctx); err != nil {
						return err
					}

					if err = task.Wait(ctx); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func checkLeasedNetworkForLeakedVirtualMachines(ctx context.Context, lease *v1.Lease, metadata *vsphere.Metadata, leaseDeleted bool, logger logr.Logger) error {
	for _, network := range lease.Status.Topology.Networks {
		logger.WithName("vcenter").Info("checking lease", "network", network)
		for server, _ := range metadata.VCenterCredentials {
			logger.WithName("vcenter").Info("checking for leaked virtual machines", "vcenter", server)
			if virtualMachineManagedObjectRefs, err := getVirtualMachinesFromDistributedPortGroupManagedObject(ctx, server, network, metadata, logger); virtualMachineManagedObjectRefs != nil && err == nil {
				vmsToDelete, err := getVirtualMachineManagedObjects(ctx, server, lease, virtualMachineManagedObjectRefs, metadata, logger, leaseDeleted)
				if err != nil {
					return err
				}
				for _, vm := range vmsToDelete {
					logger.WithName("virtual machine").Info("deleting", "name", vm.Name, "uptime", vm.Summary.QuickStats.UptimeSeconds)

					if err := deleteVirtualMachine(ctx, server, metadata, vm.Reference()); err != nil {
						return err
					}
				}
			} else if err != nil {
				return err
			}
		}
	}
	return nil
}

func getVirtualMachineManagedObjects(ctx context.Context, server string, lease *v1.Lease, vmMoRefs []types.ManagedObjectReference, metadata *vsphere.Metadata, logger logr.Logger, noTimeCheck bool) ([]mo.VirtualMachine, error) {
	s, err := metadata.Session(ctx, server)
	if err != nil {
		return nil, err
	}
	virtualMachinesMo := make([]mo.VirtualMachine, 0, len(vmMoRefs))
	virtualMachinesToDelete := make([]mo.VirtualMachine, 0, len(vmMoRefs))

	logger.WithName("virtual machine").Info("retrieve", "managed objects", vmMoRefs)
	if err := s.PropertyCollector().Retrieve(ctx, vmMoRefs, []string{"name", "parent", "summary", "config"}, &virtualMachinesMo); err != nil {
		return nil, err
	}

	logger.WithName("virtual machines").Info("managed objects", "length", len(virtualMachinesMo))

	// there should be no virtual machines left on a ci-vlan- port group
	for _, vm := range virtualMachinesMo {
		// avoids deleting upi imported rhcos ovas so they can be re-used
		if strings.HasPrefix(vm.Name, "rhcos-") {
			continue
		}

		if vm.Config == nil {
			logger.WithName("virtual machine").Info("config", "name", vm.Name, "at", "WARN: is nil")
			continue
		}

		// don't delete templates
		if vm.Config.Template {
			logger.WithName("template").Info("ignoring", "name", vm.Name)
			continue
		}
		if vm.Config.CreateDate == nil {
			logger.WithName("virtual machine").Info("createdate", "name", vm.Name, "at", "WARN: is nil")
			continue
		}

		before := vm.Config.CreateDate.Before(lease.CreationTimestamp.Time)

		logger.WithName("virtual machine").Info("created", "name", vm.Name, "at", vm.Config.CreateDate.String(), "before lease", before)
		logger.WithName("lease").Info("created", "name", lease.Name, "at", lease.CreationTimestamp.String(), "before lease", before)

		if before || noTimeCheck {
			virtualMachinesToDelete = append(virtualMachinesToDelete, vm)
		}
	}
	return virtualMachinesToDelete, nil
}

func getVirtualMachinesFromDistributedPortGroupManagedObject(ctx context.Context, server, network string, metadata *vsphere.Metadata, logger logr.Logger) ([]types.ManagedObjectReference, error) {
	s, err := metadata.Session(ctx, server)
	if err != nil {
		return nil, err
	}

	datacenters, err := s.Finder.DatacenterList(ctx, "*")
	if err != nil {
		return nil, err
	}

	var vmMoRefs []types.ManagedObjectReference

	logger.WithName("vcenter").Info("checking", "datacenters", datacenters)

	for _, dc := range datacenters {
		s.Finder.SetDatacenter(dc)
		networkBasename := path.Base(network)

		networkList, err := s.Finder.NetworkList(ctx, networkBasename)
		if err != nil {
			return nil, err
		}
		logger.WithName("vcenter").Info("network", "length", len(networkList))

		for _, networkRef := range networkList {
			var dvpg mo.DistributedVirtualPortgroup

			if err := s.PropertyCollector().RetrieveOne(ctx, networkRef.Reference(), []string{"vm", "name", "key"}, &dvpg); err != nil {
				return nil, err
			}
			logger.WithName("vcenter").Info("checking", "network", dvpg.Name, "vm length", len(dvpg.Vm))

			vmMoRefs = append(vmMoRefs, dvpg.Vm...)
		}
	}
	return vmMoRefs, nil
}

func deleteVirtualMachine(ctx context.Context, server string, metadata *vsphere.Metadata, ref types.ManagedObjectReference) error {
	s, err := metadata.Session(ctx, server)
	if err != nil {
		return err
	}
	vmObj := object.NewVirtualMachine(s.Client.Client, ref)

	powerState, err := vmObj.PowerState(ctx)
	if powerState == types.VirtualMachinePowerStatePoweredOn {
		powerOffTask, err := vmObj.PowerOff(ctx)
		if err != nil {
			return err
		}
		if err := powerOffTask.Wait(ctx); err != nil {
			return err
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

// SetupWithManager sets up the controller with the Manager.
func (r *LeaseReconciler) SetupWithManager(mgr ctrl.Manager) error {

	leases = make(map[string]*v1.Lease)

	return ctrl.NewControllerManagedBy(mgr).For(&v1.Lease{}).WithEventFilter(predicate.Funcs{
		CreateFunc:  func(createEvent event.CreateEvent) bool { return true },
		DeleteFunc:  func(deleteEvent event.DeleteEvent) bool { return true },
		UpdateFunc:  func(updateEvent event.UpdateEvent) bool { return true },
		GenericFunc: func(genericEvent event.GenericEvent) bool { return false },
	}).Complete(r)
}
