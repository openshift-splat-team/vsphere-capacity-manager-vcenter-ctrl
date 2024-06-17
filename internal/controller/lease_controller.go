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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"
)

// LeaseReconciler reconciles a Lease object
type LeaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

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
	logger.Info(fmt.Sprintf("Deleted lease %s", req.NamespacedName))

	lease := &v1.Lease{}

	if err := r.Get(ctx, client.ObjectKey{Name: req.Name, Namespace: req.Namespace}, lease); err != nil {
		// If the lease doesn't exist ignore it.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	for _, network := range lease.Status.Topology.Networks {
		logger.Info(fmt.Sprintf("Found lease %s with network %s", lease.Name, network))
	}

	/*
		for _, ownRef := range lease.OwnerReferences {
			if ownRef.Kind == v1.NetworkKind {
				r.Get()ownRef.Name

			}
		}

	*/

	return ctrl.Result{}, nil

}

// SetupWithManager sets up the controller with the Manager.
func (r *LeaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1.Lease{}).WithEventFilter(predicate.Funcs{
		DeleteFunc:  func(deleteEvent event.DeleteEvent) bool { return true },
		UpdateFunc:  func(updateEvent event.UpdateEvent) bool { return false },
		GenericFunc: func(genericEvent event.GenericEvent) bool { return false },
	}).Complete(r)
}
