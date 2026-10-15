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

// Tests for VSphereObjectReconciler: the deletion gating helpers and each
// scheduled cleanup function, exercised against the vcsim harness defined in
// lease_controller_vcsim_test.go.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/vmware/govmomi/cns"
	cnstypes "github.com/vmware/govmomi/cns/types"
	"github.com/vmware/govmomi/pbm"
	pbmtypes "github.com/vmware/govmomi/pbm/types"
	"github.com/vmware/govmomi/vapi/tags"
	"github.com/vmware/govmomi/vim25/types"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/utils"
	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/vsphere"
	v1 "github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

func newTestVSphereObjectReconciler(md *vsphere.Metadata, protection utils.ProtectionConfig) *VSphereObjectReconciler {
	sch := runtime.NewScheme()
	Expect(v1.AddToScheme(sch)).To(Succeed())
	return &VSphereObjectReconciler{
		Client:     k8sClient,
		Scheme:     sch,
		Metadata:   md,
		Protection: protection,
		Safety:     utils.SafetyConfig{MinAgeHours: 1},
		Features:   utils.FeaturesConfig{}, // DryRun: false
		Logging:    utils.LoggingConfig{EnableAuditLog: true},
		logger:     logr.Discard(),
	}
}

// seedFirstSeen pre-populates the first-seen tracker so the object is
// immediately eligible for deletion.
func seedFirstSeen(v *VSphereObjectReconciler, objectType, server, name string, seen time.Time) {
	v.firstSeen.Store(fmt.Sprintf("%s/%s/%s", objectType, server, name), seen)
}

var _ = Describe("VSphereObject controller", func() {
	ctx := context.Background()

	Context("deletion gating", func() {
		It("shouldDelete honors dry-run mode", func() {
			v := newTestVSphereObjectReconciler(vsphere.NewMetadata(), utils.ProtectionConfig{})
			Expect(v.shouldDelete("folder", "x")).To(BeTrue())

			v.Features.DryRun = true
			Expect(v.shouldDelete("folder", "x")).To(BeFalse())
		})

		It("isOldEnough gates on first-seen age and resets after firing", func() {
			v := newTestVSphereObjectReconciler(vsphere.NewMetadata(), utils.ProtectionConfig{})

			// first sighting is only tracked
			Expect(v.isOldEnough("folder", "srv", "f1")).To(BeFalse())
			// and still not old enough on the second look
			Expect(v.isOldEnough("folder", "srv", "f1")).To(BeFalse())

			// a sighting older than MinAgeHours fires...
			seedFirstSeen(v, "folder", "srv", "f2", time.Now().Add(-2*time.Hour))
			Expect(v.isOldEnough("folder", "srv", "f2")).To(BeTrue())
			// ...clears its tracking entry...
			_, ok := v.firstSeen.Load("folder/srv/f2")
			Expect(ok).To(BeFalse())
			// ...and the next sighting starts tracking from scratch
			Expect(v.isOldEnough("folder", "srv", "f2")).To(BeFalse())
		})

		It("PruneVSphereObjects runs only due enabled functions", func() {
			v := newTestVSphereObjectReconciler(vsphere.NewMetadata(), utils.ProtectionConfig{})

			ran := false
			lastRunBefore := time.Now().Add(-2 * time.Hour)
			pf := []pruneFunctions{
				{
					name: "due", execute: func(ctx context.Context) error { ran = true; return nil },
					delay: time.Hour, lastRun: lastRunBefore, enabled: true,
				},
				{
					name: "disabled", execute: func(ctx context.Context) error {
						Fail("disabled function must not run")
						return nil
					},
					enabled: false,
				},
				{
					name: "not-due", execute: func(ctx context.Context) error {
						Fail("not-yet-due function must not run")
						return nil
					},
					delay: time.Hour, lastRun: time.Now(), enabled: true,
				},
				{
					name: "failing", execute: func(ctx context.Context) error { return errors.New("boom") },
					delay: time.Hour, lastRun: time.Now().Add(-2 * time.Hour), enabled: true,
				},
			}

			Expect(v.PruneVSphereObjects(ctx, pf)).To(Succeed())
			Expect(ran).To(BeTrue())
			// the executed function's lastRun is refreshed so it will not
			// immediately run again
			Expect(pf[0].lastRun).To(BeTemporally(">", lastRunBefore))
			Expect(pf[3].lastRun).To(BeTemporally(">", time.Now().Add(-2*time.Hour)))
		})
	})

	Context("vcsim: folder cleanup", func() {
		var env *vcsimEnv
		var md *vsphere.Metadata
		var v *VSphereObjectReconciler

		BeforeEach(func() {
			env = startVcsim()
			md = metadataFor(env)
			v = newTestVSphereObjectReconciler(md, utils.ProtectionConfig{
				Folders: []string{"ci-protected"},
			})
		})

		AfterEach(func() {
			env.stop()
		})

		It("deletes old empty target folders and keeps everything else", func() {
			_ = env.createFolder(ctx, "ci-gone")
			busy := env.createFolder(ctx, "ci-busy")
			_ = env.createVM(ctx, "ci-busy-vm", busy, nil)
			_ = env.createFolder(ctx, "ci-young")
			_ = env.createFolder(ctx, "ci-protected")
			_ = env.createFolder(ctx, "prod-infra")

			old := time.Now().Add(-2 * time.Hour)
			seedFirstSeen(v, "folder", env.serverURL, "ci-gone", old)
			seedFirstSeen(v, "folder", env.serverURL, "ci-busy", old)

			Expect(v.folder(ctx)).To(Succeed())

			Expect(env.folderExists(ctx, "ci-gone")).To(BeFalse(), "old empty target folder should be deleted")
			Expect(env.folderExists(ctx, "ci-busy")).To(BeTrue(), "folder with children must survive")
			Expect(env.folderExists(ctx, "ci-young")).To(BeTrue(), "newly seen folder must survive")
			Expect(env.folderExists(ctx, "ci-protected")).To(BeTrue(), "protected folder must survive")
			Expect(env.folderExists(ctx, "prod-infra")).To(BeTrue(), "non-target folder must survive")
		})

		It("getFolderList returns only unprotected target folders", func() {
			_ = env.createFolder(ctx, "ci-alpha")
			_ = env.createFolder(ctx, "ci-protected")
			_ = env.createFolder(ctx, "prod-infra")

			s, err := md.Session(ctx, env.serverURL)
			Expect(err).NotTo(HaveOccurred())

			folders, nameMap, err := getFolderList(s, logr.Discard(), []string{"ci-protected"})
			Expect(err).NotTo(HaveOccurred())
			Expect(nameMap).To(HaveKey("ci-alpha"))
			Expect(nameMap).NotTo(HaveKey("ci-protected"))
			Expect(nameMap).NotTo(HaveKey("prod-infra"))
			Expect(folders).To(HaveLen(1))
			Expect(folders[0].Name()).To(Equal("ci-alpha"))
		})
	})

	Context("vcsim: tag cleanup", func() {
		var env *vcsimEnv
		var md *vsphere.Metadata
		var v *VSphereObjectReconciler

		BeforeEach(func() {
			env = startVcsim()
			md = metadataFor(env)
			v = newTestVSphereObjectReconciler(md, utils.ProtectionConfig{
				Tags: []string{"ci-zonal-"},
			})
		})

		AfterEach(func() {
			env.stop()
		})

		It("deletes unused ci- tags and empty categories, keeping everything else", func() {
			s, err := md.Session(ctx, env.serverURL)
			Expect(err).NotTo(HaveOccurred())

			// unused tag in a category of its own: both go away
			env.createCategoryAndTag(ctx, md, "cats-solo", "ci-unused")
			// unused tag sharing a category with a non-ci tag: tag goes, category stays
			catShared := env.createCategoryAndTag(ctx, md, "cats-shared", "ci-shared")
			_, err = s.TagManager.CreateTag(ctx, &tags.Tag{
				Name: "keep-me", Description: "d", CategoryID: catShared,
			})
			Expect(err).NotTo(HaveOccurred())
			// attached tag survives
			catAttached, err := s.TagManager.CreateCategory(ctx, &tags.Category{
				Name: "cats-attached", Description: "d", Cardinality: "MULTIPLE",
			})
			Expect(err).NotTo(HaveOccurred())
			tagAttached, err := s.TagManager.CreateTag(ctx, &tags.Tag{
				Name: "ci-attached", Description: "d", CategoryID: catAttached,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.TagManager.AttachTag(ctx, tagAttached, env.vmFolder)).To(Succeed())
			// tag whose name matches an existing folder survives (cluster active)
			env.createFolder(ctx, "ci-matched")
			env.createCategoryAndTag(ctx, md, "cats-matched", "ci-matched")
			// protected tag survives
			env.createCategoryAndTag(ctx, md, "cats-zonal", "ci-zonal-east")

			seedFirstSeen(v, "tag", env.serverURL, "ci-unused", time.Now().Add(-2*time.Hour))
			seedFirstSeen(v, "tag", env.serverURL, "ci-shared", time.Now().Add(-2*time.Hour))

			Expect(v.tag(ctx)).To(Succeed())

			allTags, err := s.TagManager.GetTags(ctx)
			Expect(err).NotTo(HaveOccurred())
			names := make([]string, 0, len(allTags))
			for _, t := range allTags {
				names = append(names, t.Name)
			}
			Expect(names).NotTo(ContainElement("ci-unused"))
			Expect(names).NotTo(ContainElement("ci-shared"))
			Expect(names).To(ContainElement("keep-me"))
			Expect(names).To(ContainElement("ci-attached"))
			Expect(names).To(ContainElement("ci-matched"))
			Expect(names).To(ContainElement("ci-zonal-east"))

			cats := env.categoryNames(ctx, md)
			Expect(cats).NotTo(ContainElement("cats-solo"), "empty category should be deleted")
			Expect(cats).To(ContainElement("cats-shared"), "category with remaining tags must survive")
		})
	})

	Context("vcsim: resource pool cleanup", func() {
		var env *vcsimEnv
		var v *VSphereObjectReconciler

		BeforeEach(func() {
			env = startVcsim()
			v = newTestVSphereObjectReconciler(metadataFor(env), utils.ProtectionConfig{
				ResourcePools: []string{"ci-protected"},
			})
		})

		AfterEach(func() {
			env.stop()
		})

		It("deletes old empty target resource pools and keeps everything else", func() {
			_ = env.createResourcePool(ctx, "ci-gone")
			busy := env.createResourcePool(ctx, "ci-busy")
			_ = env.createVM(ctx, "ci-busy-vm", nil, busy)
			_ = env.createResourcePool(ctx, "qeci-gone")
			_ = env.createResourcePool(ctx, "prod-pool")
			_ = env.createResourcePool(ctx, "ci-protected")

			old := time.Now().Add(-2 * time.Hour)
			seedFirstSeen(v, "resourcepool", env.serverURL, "ci-gone", old)
			seedFirstSeen(v, "resourcepool", env.serverURL, "qeci-gone", old)
			seedFirstSeen(v, "resourcepool", env.serverURL, "ci-busy", old)

			Expect(v.resourcepool(ctx)).To(Succeed())

			Expect(env.resourcePoolExists(ctx, "ci-gone")).To(BeFalse())
			Expect(env.resourcePoolExists(ctx, "qeci-gone")).To(BeFalse())
			Expect(env.resourcePoolExists(ctx, "ci-busy")).To(BeTrue(), "pool with a vm must survive")
			Expect(env.resourcePoolExists(ctx, "prod-pool")).To(BeTrue(), "non-target pool must survive")
			Expect(env.resourcePoolExists(ctx, "ci-protected")).To(BeTrue(), "protected pool must survive")
		})
	})

	Context("vcsim: cns volume cleanup", func() {
		var env *vcsimEnv
		var md *vsphere.Metadata
		var v *VSphereObjectReconciler

		BeforeEach(func() {
			env = startVcsim()
			md = metadataFor(env)
			v = newTestVSphereObjectReconciler(md, utils.ProtectionConfig{})
		})

		AfterEach(func() {
			env.stop()
		})

		It("removes cns volumes whose cluster has no folder", func() {
			s, err := md.Session(ctx, env.serverURL)
			Expect(err).NotTo(HaveOccurred())
			cnsClient, err := cns.NewClient(ctx, s.Client.Client)
			Expect(err).NotTo(HaveOccurred())
			ds, err := env.finder.Datastore(ctx, "LocalDS_0")
			Expect(err).NotTo(HaveOccurred())

			createVolume := func(name, clusterId string) {
				task, err := cnsClient.CreateVolume(ctx, []cnstypes.CnsVolumeCreateSpec{{
					Name:       name,
					VolumeType: "BLOCK",
					Datastores: []types.ManagedObjectReference{ds.Reference()},
					Metadata: cnstypes.CnsVolumeMetadata{
						ContainerCluster: cnstypes.CnsContainerCluster{
							ClusterId:     clusterId,
							ClusterType:   "kubernetes",
							ClusterFlavor: "VANILLA",
							VSphereUser:   "user@vsphere.local",
						},
					},
					BackingObjectDetails: &cnstypes.CnsBlockBackingDetails{
						CnsBackingObjectDetails: cnstypes.CnsBackingObjectDetails{CapacityInMb: 1024},
					},
				}})
				Expect(err).NotTo(HaveOccurred())
				Expect(task.Wait(ctx)).To(Succeed())
			}

			createVolume("vol-dead", "ci-dead-cluster")
			env.createFolder(ctx, "ci-alive-cluster")
			createVolume("vol-alive", "ci-alive-cluster")

			seedFirstSeen(v, "cns", env.serverURL, "ci-dead-cluster", time.Now().Add(-2*time.Hour))

			Expect(v.cns(ctx)).To(Succeed())

			qr, err := cnsClient.QueryAllVolume(ctx, cnstypes.CnsQueryFilter{}, cnstypes.CnsQuerySelection{})
			Expect(err).NotTo(HaveOccurred())
			clusterIds := make([]string, 0, len(qr.Volumes))
			for _, vol := range qr.Volumes {
				clusterIds = append(clusterIds, vol.Metadata.ContainerCluster.ClusterId)
			}
			Expect(clusterIds).NotTo(ContainElement("ci-dead-cluster"))
			Expect(clusterIds).To(ContainElement("ci-alive-cluster"))
		})
	})

	Context("vcsim: storage policy cleanup", func() {
		var env *vcsimEnv
		var md *vsphere.Metadata
		var v *VSphereObjectReconciler

		BeforeEach(func() {
			env = startVcsim()
			md = metadataFor(env)
			v = newTestVSphereObjectReconciler(md, utils.ProtectionConfig{})
		})

		AfterEach(func() {
			env.stop()
		})

		It("removes storage policies for clusters without folders", func() {
			s, err := md.Session(ctx, env.serverURL)
			Expect(err).NotTo(HaveOccurred())
			pbmClient, err := pbm.NewClient(ctx, s.Client.Client)
			Expect(err).NotTo(HaveOccurred())

			createProfile := func(name string) {
				_, err := pbmClient.CreateProfile(ctx, pbmtypes.PbmCapabilityProfileCreateSpec{
					Name:        name,
					Description: "test policy",
					Category:    "REQUIREMENT",
					ResourceType: pbmtypes.PbmProfileResourceType{
						ResourceType: string(pbmtypes.PbmProfileResourceTypeEnumSTORAGE),
					},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			createProfile("openshift-storage-policy-ci-dead")
			env.createFolder(ctx, "ci-alive-cluster")
			createProfile("openshift-storage-policy-ci-alive-cluster")

			seedFirstSeen(v, "storagepolicy", env.serverURL, "openshift-storage-policy-ci-dead",
				time.Now().Add(-2*time.Hour))

			Expect(v.storagepolicy(ctx)).To(Succeed())

			resourceType := pbmtypes.PbmProfileResourceType{
				ResourceType: string(pbmtypes.PbmProfileResourceTypeEnumSTORAGE),
			}
			profileIds, err := pbmClient.QueryProfile(ctx, resourceType, "")
			Expect(err).NotTo(HaveOccurred())
			policies, err := pbmClient.RetrieveContent(ctx, profileIds)
			Expect(err).NotTo(HaveOccurred())
			names := make([]string, 0, len(policies))
			for _, policy := range policies {
				names = append(names, policy.GetPbmProfile().Name)
			}
			Expect(names).NotTo(ContainElement("openshift-storage-policy-ci-dead"))
			Expect(names).To(ContainElement("openshift-storage-policy-ci-alive-cluster"))
		})
	})

	Context("vcsim: kubevols cleanup", func() {
		It("runs cleanly when no kubevols directories exist", func() {
			env := startVcsim()
			defer env.stop()

			v := newTestVSphereObjectReconciler(metadataFor(env), utils.ProtectionConfig{})
			Expect(v.kubevols(ctx)).To(Succeed())
		})
	})

	Context("manager setup", func() {
		It("builds a zap logger from the logging config", func() {
			v := newTestVSphereObjectReconciler(vsphere.NewMetadata(), utils.ProtectionConfig{})
			v.Logging = utils.LoggingConfig{Level: "debug", Format: "console", EnableAuditLog: true}
			l, err := v.buildLogger()
			Expect(err).NotTo(HaveOccurred())
			Expect(l).NotTo(BeNil())
		})

		It("registers the object controller with a manager", func() {
			sch := runtime.NewScheme()
			Expect(v1.AddToScheme(sch)).To(Succeed())
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: sch})
			Expect(err).NotTo(HaveOccurred())

			v := newTestVSphereObjectReconciler(vsphere.NewMetadata(), utils.ProtectionConfig{})
			// keep the prune loop asleep after its first pass
			v.Safety.GracePeriodHours = 1
			Expect(v.SetupWithManager(mgr)).To(Succeed())
			Expect(v.Client).NotTo(BeNil())
		})
	})
})
