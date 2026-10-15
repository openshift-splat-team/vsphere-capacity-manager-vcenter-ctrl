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

// Integration tests that exercise the vSphere cleanup flows against govmomi's
// vCenter simulator (vcsim). The simulator provides a real SOAP + vAPI + CNS +
// PBM endpoint stack, so the reconciler talks to it exactly as it would to a
// production vCenter.

import (
	"context"
	"net/url"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vapi/tags"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/utils"
	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/vsphere"
	v1 "github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"

	// blank imports: their init() registers the vAPI (REST), CNS, and PBM
	// endpoints with every simulator service created in this test binary.
	_ "github.com/vmware/govmomi/cns/simulator"
	_ "github.com/vmware/govmomi/pbm/simulator"
	_ "github.com/vmware/govmomi/vapi/simulator"
)

// vcsimEnv wraps a single simulated vCenter with a connected fixture client.
type vcsimEnv struct {
	model     *simulator.Model
	server    *simulator.Server
	serverURL string // used as the Metadata credential key and lease Status.Server
	client    *govmomi.Client
	finder    *find.Finder
	vmFolder  *object.Folder
	rootRP    *object.ResourcePool
}

// startVcsim boots a VPX-model vCenter simulator with REST/CNS/PBM endpoints.
func startVcsim() *vcsimEnv {
	model := simulator.VPX()
	Expect(model.Create()).To(Succeed())
	model.Service.RegisterEndpoints = true
	server := model.Service.NewServer()

	u, err := soap.ParseURL(server.URL.String())
	Expect(err).NotTo(HaveOccurred())
	// the simulator accepts any non-empty credentials when its listen URL
	// carries no userinfo
	u.User = url.UserPassword("user", "pass")

	client, err := govmomi.NewClient(context.Background(), u, true)
	Expect(err).NotTo(HaveOccurred())

	finder := find.NewFinder(client.Client, false)
	dc, err := finder.Datacenter(context.Background(), "DC0")
	Expect(err).NotTo(HaveOccurred())
	finder.SetDatacenter(dc)
	folders, err := dc.Folders(context.Background())
	Expect(err).NotTo(HaveOccurred())
	rootRP, err := finder.ResourcePool(context.Background(), "DC0_C0/Resources")
	Expect(err).NotTo(HaveOccurred())

	return &vcsimEnv{
		model:     model,
		server:    server,
		serverURL: server.URL.String(),
		client:    client,
		finder:    finder,
		vmFolder:  folders.VmFolder,
		rootRP:    rootRP,
	}
}

func (e *vcsimEnv) stop() {
	_ = e.client.Logout(context.Background())
	e.server.Close()
	e.model.Remove()
}

// metadataFor returns a Metadata with credentials for every given simulator.
func metadataFor(envs ...*vcsimEnv) *vsphere.Metadata {
	md := vsphere.NewMetadata()
	for _, e := range envs {
		_, err := md.AddCredentials(e.serverURL, "user", "pass")
		Expect(err).NotTo(HaveOccurred())
	}
	return md
}

func (e *vcsimEnv) createFolder(ctx context.Context, name string) *object.Folder {
	f, err := e.vmFolder.CreateFolder(ctx, name)
	Expect(err).NotTo(HaveOccurred())
	return f
}

func (e *vcsimEnv) createResourcePool(ctx context.Context, name string) *object.ResourcePool {
	rp, err := e.rootRP.Create(ctx, name, types.DefaultResourceConfigSpec())
	Expect(err).NotTo(HaveOccurred())
	return rp
}

// createVM creates a VM attached to the default port group, optionally inside
// the given folder and resource pool.
func (e *vcsimEnv) createVM(ctx context.Context, name string, folder *object.Folder, rp *object.ResourcePool) *object.VirtualMachine {
	if folder == nil {
		folder = e.vmFolder
	}
	if rp == nil {
		rp = e.rootRP
	}
	spec := types.VirtualMachineConfigSpec{
		Name: name,
		Files: &types.VirtualMachineFileInfo{
			VmPathName: "[LocalDS_0]",
		},
		DeviceChange: []types.BaseVirtualDeviceConfigSpec{
			&types.VirtualDeviceConfigSpec{
				Operation: types.VirtualDeviceConfigSpecOperationAdd,
				Device: &types.VirtualE1000{
					VirtualEthernetCard: types.VirtualEthernetCard{
						VirtualDevice: types.VirtualDevice{
							Backing: &types.VirtualEthernetCardNetworkBackingInfo{
								VirtualDeviceDeviceBackingInfo: types.VirtualDeviceDeviceBackingInfo{
									DeviceName: "VM Network",
								},
							},
						},
					},
				},
			},
		},
	}
	task, err := folder.CreateVM(ctx, spec, rp, nil)
	Expect(err).NotTo(HaveOccurred())
	info, err := task.WaitForResult(ctx)
	Expect(err).NotTo(HaveOccurred())
	vmRef, ok := info.Result.(types.ManagedObjectReference)
	Expect(ok).To(BeTrue())
	return object.NewVirtualMachine(e.client.Client, vmRef)
}

func (e *vcsimEnv) folderExists(ctx context.Context, name string) bool {
	_, err := e.finder.Folder(ctx, name)
	return err == nil
}

func (e *vcsimEnv) vmExists(ctx context.Context, name string) bool {
	_, err := e.finder.VirtualMachine(ctx, name)
	return err == nil
}

func (e *vcsimEnv) resourcePoolExists(ctx context.Context, name string) bool {
	_, err := e.finder.ResourcePool(ctx, name)
	return err == nil
}

// attachVMsToNetwork patches the simulator's Network entity so its Vm list
// includes the given VMs. vcsim maintains vm.Network but not the reverse
// network.Vm back-reference that the controller's port group cleanup reads.
func (e *vcsimEnv) attachVMsToNetwork(ctx context.Context, networkName string, vms ...*object.VirtualMachine) {
	network, err := e.finder.Network(ctx, networkName)
	Expect(err).NotTo(HaveOccurred())
	networkMo, ok := e.model.Service.Context.Map.Get(network.Reference()).(*mo.Network)
	Expect(ok).To(BeTrue())
	for _, vm := range vms {
		networkMo.Vm = append(networkMo.Vm, vm.Reference())
	}
}

func (e *vcsimEnv) categoryNames(ctx context.Context, md *vsphere.Metadata) []string {
	s, err := md.Session(ctx, e.serverURL)
	Expect(err).NotTo(HaveOccurred())
	categories, err := s.TagManager.GetCategories(ctx)
	Expect(err).NotTo(HaveOccurred())
	names := make([]string, 0, len(categories))
	for _, c := range categories {
		names = append(names, c.Name)
	}
	return names
}

// createCategoryAndTag creates a tag category and, if tagName is non-empty,
// a tag inside it. Returns the category id.
func (e *vcsimEnv) createCategoryAndTag(ctx context.Context, md *vsphere.Metadata, categoryName, tagName string) string {
	s, err := md.Session(ctx, e.serverURL)
	Expect(err).NotTo(HaveOccurred())
	catID, err := s.TagManager.CreateCategory(ctx, &tags.Category{
		Name:        categoryName,
		Description: categoryName,
		Cardinality: "MULTIPLE",
	})
	Expect(err).NotTo(HaveOccurred())
	if tagName == "" {
		return catID
	}
	_, err = s.TagManager.CreateTag(ctx, &tags.Tag{
		Name:        tagName,
		Description: tagName,
		CategoryID:  catID,
	})
	Expect(err).NotTo(HaveOccurred())
	return catID
}

// validFailureDomain builds a CRD-conformant failure domain for the given
// server, so leases created through the envtest API server pass validation.
func validFailureDomain(server string) v1.FailureDomainSpec {
	return v1.FailureDomainSpec{
		VSpherePlatformFailureDomainSpec: configv1.VSpherePlatformFailureDomainSpec{
			Name:   "fd1",
			Region: "us-east",
			Zone:   "us-east-1",
			Server: server,
			Topology: configv1.VSpherePlatformTopology{
				ComputeCluster: "/DC0/host/DC0_C0",
				Datacenter:     "DC0",
				Datastore:      "/DC0/datastore/LocalDS_0",
				Networks:       []string{"VM Network"},
			},
		},
	}
}

// multiServerLease builds a fulfilled lease for the given PoolInfo servers
// with the standard test cluster id.
func multiServerLease(name string, servers ...string) *v1.Lease {
	poolInfo := make([]v1.FailureDomainSpec, 0, len(servers))
	for _, s := range servers {
		poolInfo = append(poolInfo, validFailureDomain(s))
	}
	return &v1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			Labels:            map[string]string{"cluster-id": "cluster-123"},
			CreationTimestamp: metav1.Now(),
		},
		Status: v1.LeaseStatus{
			Phase:             v1.PHASE_FULFILLED,
			FailureDomainSpec: validFailureDomain(servers[0]),
			PoolInfo:          poolInfo,
		},
	}
}

func newTestLeaseReconciler(md *vsphere.Metadata, protection utils.ProtectionConfig) *LeaseReconciler {
	sch := runtime.NewScheme()
	Expect(v1.AddToScheme(sch)).To(Succeed())
	return &LeaseReconciler{
		Client:     k8sClient,
		Scheme:     sch,
		Metadata:   md,
		Protection: protection,
		leases:     make(map[string]*v1.Lease),
		skipVMs:    make(map[string]string),
		logger:     logr.Discard(),
	}
}

var _ = Describe("Lease controller vcsim integration", func() {
	ctx := context.Background()

	Context("managed entity discovery", func() {
		var env1, env2 *vcsimEnv

		BeforeEach(func() {
			env1 = startVcsim()
			env2 = startVcsim()
		})

		AfterEach(func() {
			env2.stop()
			env1.stop()
		})

		It("searches every PoolInfo server for cluster entities", func() {
			env1.createFolder(ctx, "cluster-123-folder")
			env2.createFolder(ctx, "cluster-123-folder")

			r := newTestLeaseReconciler(metadataFor(env1, env2), utils.ProtectionConfig{})
			r.leases["default/lease-x"] = multiServerLease("lease-x", env1.serverURL, env2.serverURL)

			entities, err := r.getManagedEntitiesByClusterId(ctx, "default/lease-x")
			Expect(err).NotTo(HaveOccurred())
			Expect(entities).To(HaveLen(2))
			Expect(entities).To(HaveKey(env1.serverURL))
			Expect(entities).To(HaveKey(env2.serverURL))
			for _, serverEntities := range entities {
				Expect(serverEntities).To(HaveLen(1))
				Expect(serverEntities[0].Name).To(Equal("cluster-123-folder"))
			}
		})

		It("falls back to the inline Status.Server for leases without PoolInfo", func() {
			env1.createFolder(ctx, "cluster-123-folder")
			env2.createFolder(ctx, "cluster-123-folder")

			r := newTestLeaseReconciler(metadataFor(env1, env2), utils.ProtectionConfig{})
			lease := multiServerLease("lease-legacy", env1.serverURL)
			lease.Status.PoolInfo = nil
			r.leases["default/lease-legacy"] = lease

			entities, err := r.getManagedEntitiesByClusterId(ctx, "default/lease-legacy")
			Expect(err).NotTo(HaveOccurred())
			// only the inline server (env1) is searched
			Expect(entities).To(HaveLen(1))
			Expect(entities).To(HaveKey(env1.serverURL))
		})

		It("returns only the servers that have matching entities", func() {
			env1.createFolder(ctx, "cluster-123-folder")

			r := newTestLeaseReconciler(metadataFor(env1, env2), utils.ProtectionConfig{})
			r.leases["default/lease-x"] = multiServerLease("lease-x", env1.serverURL, env2.serverURL)

			entities, err := r.getManagedEntitiesByClusterId(ctx, "default/lease-x")
			Expect(err).NotTo(HaveOccurred())
			Expect(entities).To(HaveLen(1))
			Expect(entities).To(HaveKey(env1.serverURL))
		})
	})

	Context("managed entity deletion", func() {
		var env *vcsimEnv
		var r *LeaseReconciler

		BeforeEach(func() {
			env = startVcsim()
			r = newTestLeaseReconciler(metadataFor(env), utils.ProtectionConfig{
				ResourcePools: []string{"cluster-123-protected"},
			})
			r.leases["default/lease-x"] = multiServerLease("lease-x", env.serverURL)
		})

		AfterEach(func() {
			env.stop()
		})

		It("destroys folders, virtual machines and resource pools found by cluster id", func() {
			folder := env.createFolder(ctx, "cluster-123-folder")
			_ = env.createVM(ctx, "cluster-123-vm", folder, nil)
			_ = env.createResourcePool(ctx, "cluster-123-rp")
			_ = env.createResourcePool(ctx, "cluster-123-protected-rp")

			entities, err := r.getManagedEntitiesByClusterId(ctx, "default/lease-x")
			Expect(err).NotTo(HaveOccurred())
			Expect(entities[env.serverURL]).NotTo(BeEmpty())

			// pass the entity list twice: every entity must be deleted exactly once
			duplicated := append(entities[env.serverURL], entities[env.serverURL]...)
			err = r.deleteByManagedEntity(ctx, map[string][]mo.ManagedEntity{env.serverURL: duplicated})
			Expect(err).NotTo(HaveOccurred())

			Expect(env.folderExists(ctx, "cluster-123-folder")).To(BeFalse(), "folder should be destroyed")
			Expect(env.vmExists(ctx, "cluster-123-vm")).To(BeFalse(), "vm inside the folder should be destroyed")
			Expect(env.resourcePoolExists(ctx, "cluster-123-rp")).To(BeFalse(), "resource pool should be destroyed")
			Expect(env.resourcePoolExists(ctx, "cluster-123-protected-rp")).To(BeTrue(), "protected resource pool must survive")
		})

		It("returns the children of matching folders grouped by server", func() {
			folder := env.createFolder(ctx, "cluster-123-folder")
			child, err := folder.CreateFolder(ctx, "cluster-123-child")
			Expect(err).NotTo(HaveOccurred())
			_ = env.createVM(ctx, "cluster-123-vm", child, nil)

			entities, err := r.getManagedEntitiesByClusterId(ctx, "default/lease-x")
			Expect(err).NotTo(HaveOccurred())

			children, err := r.childrenOfFolder(ctx, entities)
			Expect(err).NotTo(HaveOccurred())
			Expect(children).To(HaveKey(env.serverURL))
			names := make([]string, 0)
			for _, me := range children[env.serverURL] {
				names = append(names, me.Name)
			}
			Expect(names).To(ContainElement("cluster-123-child"))
			Expect(names).To(ContainElement("cluster-123-vm"))
		})

		It("skips virtual machines on the invalid-state skip list", func() {
			vm := env.createVM(ctx, "cluster-123-stuck", nil, nil)
			r.skipVMs[vm.Reference().Value] = "cluster-123-stuck"

			err := r.deleteByManagedEntity(ctx, map[string][]mo.ManagedEntity{env.serverURL: {
				{ExtensibleManagedObject: mo.ExtensibleManagedObject{Self: vm.Reference()}},
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(env.vmExists(ctx, "cluster-123-stuck")).To(BeTrue(), "skipped vm must survive")
		})
	})

	Context("virtual machine cleanup", func() {
		var env *vcsimEnv
		var r *LeaseReconciler

		BeforeEach(func() {
			env = startVcsim()
			r = newTestLeaseReconciler(metadataFor(env), utils.ProtectionConfig{
				VirtualMachines: []string{"prefix-"},
			})
			r.leases["default/lease-x"] = multiServerLease("lease-x", env.serverURL)
		})

		AfterEach(func() {
			env.stop()
		})

		It("powers off and destroys a running virtual machine", func() {
			vm := env.createVM(ctx, "running-vm", nil, nil)
			task, err := vm.PowerOn(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(task.Wait(ctx)).To(Succeed())

			Expect(r.deleteVirtualMachine(ctx, env.serverURL, vm.Reference())).To(Succeed())
			Expect(env.vmExists(ctx, "running-vm")).To(BeFalse())
		})

		It("removes only orphaned virtual machines on the lease port group", func() {
			// created before the lease snapshot below, so they count as orphans
			orphan := env.createVM(ctx, "orphan-vm", nil, nil)
			clusterVM := env.createVM(ctx, "cluster-123-worker", nil, nil)
			rhcosVM := env.createVM(ctx, "rhcos-4-master", nil, nil)
			protectedVM := env.createVM(ctx, "prefix-vm", nil, nil)
			env.attachVMsToNetwork(ctx, "VM Network", orphan, clusterVM, rhcosVM, protectedVM)

			// the lease snapshot is taken after the VMs exist
			r.leases["default/lease-x"] = multiServerLease("lease-x", env.serverURL)

			Expect(r.deleteVirtualMachinesByPortGroup(ctx, "default/lease-x")).To(Succeed())

			Expect(env.vmExists(ctx, "orphan-vm")).To(BeFalse(), "orphan vm should be deleted")
			Expect(env.vmExists(ctx, "cluster-123-worker")).To(BeTrue(), "cluster vm must survive")
			Expect(env.vmExists(ctx, "rhcos-4-master")).To(BeTrue(), "rhcos vm must survive")
			Expect(env.vmExists(ctx, "prefix-vm")).To(BeTrue(), "protected vm must survive")
		})
	})

	Context("tag cleanup", func() {
		var env1, env2 *vcsimEnv
		var r *LeaseReconciler

		BeforeEach(func() {
			env1 = startVcsim()
			env2 = startVcsim()
			r = newTestLeaseReconciler(metadataFor(env1, env2), utils.ProtectionConfig{})
			r.leases["default/lease-x"] = multiServerLease("lease-x", env1.serverURL, env2.serverURL)
		})

		AfterEach(func() {
			env2.stop()
			env1.stop()
		})

		It("removes cluster tag categories on every server, keeping unrelated ones", func() {
			md := metadataFor(env1, env2)
			env1.createCategoryAndTag(ctx, md, "cluster-123-cattags", "")
			env2.createCategoryAndTag(ctx, md, "cluster-123-cattags", "")
			env1.createCategoryAndTag(ctx, md, "keep-me", "")

			Expect(r.deleteTagsByClusterId(ctx, "default/lease-x")).To(Succeed())

			Expect(env1.categoryNames(ctx, md)).NotTo(ContainElement("cluster-123-cattags"))
			Expect(env1.categoryNames(ctx, md)).To(ContainElement("keep-me"))
			Expect(env2.categoryNames(ctx, md)).NotTo(ContainElement("cluster-123-cattags"))
		})
	})

	Context("deleted lease reconciliation", func() {
		var env1, env2 *vcsimEnv
		var r *LeaseReconciler

		BeforeEach(func() {
			env1 = startVcsim()
			env2 = startVcsim()
			r = newTestLeaseReconciler(metadataFor(env1, env2), utils.ProtectionConfig{})
		})

		AfterEach(func() {
			env2.stop()
			env1.stop()
		})

		It("cleans up every server serving a multi-pool lease", func() {
			for _, env := range []*vcsimEnv{env1, env2} {
				folder := env.createFolder(ctx, "cluster-123-e2e")
				_ = env.createVM(ctx, "cluster-123-e2e-vm", folder, nil)
				_ = env.createResourcePool(ctx, "cluster-123-e2e-rp")
				env.createCategoryAndTag(ctx, metadataFor(env), "cluster-123-e2e", "")
			}

			lease := multiServerLease("e2e-lease", env1.serverURL, env2.serverURL)
			Expect(k8sClient.Create(ctx, lease)).To(Succeed())
			// Create strips the status subresource; re-apply it.
			lease.Status = multiServerLease("e2e-lease", env1.serverURL, env2.serverURL).Status
			Expect(k8sClient.Status().Update(ctx, lease)).To(Succeed())

			req := ctrl.Request{NamespacedName: k8stypes.NamespacedName{Name: "e2e-lease", Namespace: "default"}}

			// first reconcile caches the fulfilled lease and runs leak detection
			_, err := r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Delete(ctx, lease)).To(Succeed())

			// second reconcile sees NotFound and cleans up every server
			_, err = r.Reconcile(ctx, req)
			Expect(err).NotTo(HaveOccurred())

			for _, env := range []*vcsimEnv{env1, env2} {
				Expect(env.folderExists(ctx, "cluster-123-e2e")).To(BeFalse(), "folder on "+env.serverURL)
				Expect(env.vmExists(ctx, "cluster-123-e2e-vm")).To(BeFalse(), "vm on "+env.serverURL)
				Expect(env.resourcePoolExists(ctx, "cluster-123-e2e-rp")).To(BeFalse(), "rp on "+env.serverURL)
				Expect(env.categoryNames(ctx, metadataFor(env))).NotTo(ContainElement("cluster-123-e2e"))
			}

			r.leaseMu.Lock()
			_, exists := r.leases["default/e2e-lease"]
			r.leaseMu.Unlock()
			Expect(exists).To(BeFalse(), "deleted lease must be evicted from the cache")
		})
	})

	Context("pure helpers", func() {
		It("converts object references to managed object references", func() {
			refs := toManagedObjectRefs([]object.Reference{
				object.NewFolder(nil, types.ManagedObjectReference{Type: "Folder", Value: "f-1"}),
				object.NewVirtualMachine(nil, types.ManagedObjectReference{Type: "VirtualMachine", Value: "vm-1"}),
			})
			Expect(refs).To(Equal([]types.ManagedObjectReference{
				{Type: "Folder", Value: "f-1"},
				{Type: "VirtualMachine", Value: "vm-1"},
			}))
		})

		It("extracts messages from soap and vim faults only", func() {
			Expect(extractFaultMessageFromErr(nil)).To(Equal(""))
			Expect(extractFaultMessageFromErr(context.DeadlineExceeded)).To(Equal(""))

			Expect(extractFaultMessageFromErr(soap.WrapSoapFault(&soap.Fault{String: "soap fault message"}))).
				To(Equal("soap fault message"))

			vimFault := &types.InvalidState{}
			vimFault.FaultCause = &types.LocalizedMethodFault{LocalizedMessage: "vim fault message"}
			Expect(extractFaultMessageFromErr(soap.WrapVimFault(vimFault))).To(Equal("vim fault message"))

			// a vim fault without a fault cause yields an empty message
			Expect(extractFaultMessageFromErr(soap.WrapVimFault(&types.InvalidState{}))).To(Equal(""))
		})

		It("detects virtual machines with orphaned 0GB disks", func() {
			Expect(hasOrphanedDisks(mo.VirtualMachine{})).To(BeFalse())

			noDisks := mo.VirtualMachine{Config: &types.VirtualMachineConfigInfo{}}
			Expect(hasOrphanedDisks(noDisks)).To(BeFalse())

			withDisk := func(capacity int64) mo.VirtualMachine {
				return mo.VirtualMachine{Config: &types.VirtualMachineConfigInfo{
					Hardware: types.VirtualHardware{
						Device: []types.BaseVirtualDevice{
							&types.VirtualDisk{CapacityInKB: capacity},
						},
					},
				}}
			}
			Expect(hasOrphanedDisks(withDisk(0))).To(BeTrue())
			Expect(hasOrphanedDisks(withDisk(1024))).To(BeFalse())
		})
	})

	Context("manager setup", func() {
		It("registers the lease controller with a manager", func() {
			sch := runtime.NewScheme()
			Expect(v1.AddToScheme(sch)).To(Succeed())
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: sch})
			Expect(err).NotTo(HaveOccurred())

			r := &LeaseReconciler{
				Client:   mgr.GetClient(),
				Scheme:   sch,
				Metadata: vsphere.NewMetadata(),
				leases:   make(map[string]*v1.Lease),
				skipVMs:  make(map[string]string),
			}
			Expect(r.SetupWithManager(mgr)).To(Succeed())
		})
	})
})
