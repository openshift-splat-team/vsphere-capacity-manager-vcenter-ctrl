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
	"sync"

	"github.com/go-logr/logr"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"
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
}

var (
	leases  map[string]*v1.Lease
	leaseMu sync.Mutex
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
	logger := log.FromContext(ctx)
	lease := &v1.Lease{}

	if err := r.Get(ctx, client.ObjectKey{Name: req.Name, Namespace: req.Namespace}, lease); err != nil {
		// error is Not NotFound, return
		if err = client.IgnoreNotFound(err); err != nil {
			return ctrl.Result{}, err
		}

		// If lease was not found it was deleted, start clean up process
		// make a copy of the lease, lock, delete the lease from cache, unlock, delete virtual machines if they exist
		if lease, ok := leases[req.Name]; ok {
			logger.Info(fmt.Sprintf("cached lease %s available for leak check", lease.Name))
			leaseCopy := lease.DeepCopy()

			leaseMu.Lock()
			delete(leases, req.Name)
			leaseMu.Unlock()

			if err := checkLeasedNetworkForLeakedVirtualMachines(ctx, leaseCopy, r.Metadata, true, logger); err != nil {
				return ctrl.Result{}, err
			}
			leaseCopy = nil

		} else {
			logger.Info(fmt.Sprintf("potentially did not clean up deleted lease %s", req.NamespacedName))
		}

		return ctrl.Result{}, nil
	}

	if lease.DeletionTimestamp != nil {
		logger.Info(fmt.Sprintf("lease was deleted at %s", lease.DeletionTimestamp.String()))
	}

	if _, ok := leases[lease.Name]; !ok {
		leaseMu.Lock()
		leases[req.Name] = lease
		leaseMu.Unlock()
		logger.Info(fmt.Sprintf("cached lease was created at %s", lease.CreationTimestamp.String()))

		if err := checkLeasedNetworkForLeakedVirtualMachines(ctx, lease, r.Metadata, false, logger); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func checkLeasedNetworkForLeakedVirtualMachines(ctx context.Context, lease *v1.Lease, metadata *vsphere.Metadata, leaseDeleted bool, logger logr.Logger) error {
	for _, network := range lease.Status.Topology.Networks {
		logger.Info(fmt.Sprintf("Found lease %s with network %s", lease.Name, network))
		for server, _ := range metadata.VCenterCredentials {
			s, err := metadata.Session(ctx, server)
			if err != nil {
				return err
			}

			datacenters, err := s.Finder.DatacenterList(ctx, "/...")
			if err != nil {
				return err
			}

			for _, dc := range datacenters {
				s.Finder.SetDatacenter(dc)
				networkBasename := path.Base(network)

				networkList, err := s.Finder.NetworkList(ctx, networkBasename)
				if err != nil {
					return err
				}

				for _, networkRef := range networkList {
					var dvpg mo.DistributedVirtualPortgroup

					if err := s.PropertyCollector().RetrieveOne(ctx, networkRef.Reference(), []string{"vm"}, &dvpg); err != nil {
						return err
					}
					// No virtual machines no problems...
					if len(dvpg.Vm) > 0 {
						virtualMachinesMo := make([]mo.VirtualMachine, 0, len(dvpg.Vm))

						if err := s.PropertyCollector().Retrieve(ctx, dvpg.Vm, []string{"name", "summary", "config"}, &virtualMachinesMo); err != nil {
							return err
						}

						// there should be no virtual machines left on a ci-vlan- port group
						for _, vm := range virtualMachinesMo {
							if vm.Config != nil {
								if vm.Config.CreateDate != nil {

									// if the lease was created _after_ the virtual machines then they probably shouldn't be there right?
									if leaseDeleted || lease.CreationTimestamp.Time.After(*vm.Config.CreateDate) {
										logger.Info(fmt.Sprintf("destroying virtual machine %s uptime %d created %s", vm.Name, vm.Summary.QuickStats.UptimeSeconds, vm.Config.CreateDate.String()))

										if err := deleteVirtualMachine(ctx, s, vm.Reference()); err != nil {
											return err
										}

									}
								}
							}
						}
					}
				}
			}
		}
	}
	return nil
}

func deleteVirtualMachine(ctx context.Context, session *session.Session, ref types.ManagedObjectReference) error {
	vmObj := object.NewVirtualMachine(session.Client.Client, ref)

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
		UpdateFunc:  func(updateEvent event.UpdateEvent) bool { return false },
		GenericFunc: func(genericEvent event.GenericEvent) bool { return false },
	}).Complete(r)
}