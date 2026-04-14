package controller

import (
	"sync"
	"time"

	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/zap"

	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/utils"
)

var _ = Describe("Protection Helpers", func() {
	Context("IsProtectedTag", func() {
		It("returns true when tag matches a protected prefix", func() {
			Expect(IsProtectedTag("us-east-ci-12345", []string{"us-"})).To(BeTrue())
		})

		It("returns false when tag does not match any prefix", func() {
			Expect(IsProtectedTag("ci-12345", []string{"us-"})).To(BeFalse())
		})

		It("returns false when prefix list is empty", func() {
			Expect(IsProtectedTag("us-east", []string{})).To(BeFalse())
		})

		It("returns false when tag name is empty", func() {
			Expect(IsProtectedTag("", []string{"us-"})).To(BeFalse())
		})

		It("matches against multiple prefixes", func() {
			prefixes := []string{"us-", "eu-", "ap-"}
			Expect(IsProtectedTag("eu-west-ci-123", prefixes)).To(BeTrue())
			Expect(IsProtectedTag("ap-south-ci-456", prefixes)).To(BeTrue())
			Expect(IsProtectedTag("ci-789", prefixes)).To(BeFalse())
		})
	})

	Context("IsTargetFolder", func() {
		It("returns true when folder contains a target pattern", func() {
			Expect(IsTargetFolder("ci-12345", DefaultFolderTargetPatterns)).To(BeTrue())
		})

		It("returns true for user- pattern", func() {
			Expect(IsTargetFolder("user-test", DefaultFolderTargetPatterns)).To(BeTrue())
		})

		It("returns true for build- pattern", func() {
			Expect(IsTargetFolder("build-abc", DefaultFolderTargetPatterns)).To(BeTrue())
		})

		It("returns false when folder does not match any pattern", func() {
			Expect(IsTargetFolder("production", DefaultFolderTargetPatterns)).To(BeFalse())
		})

		It("returns false when patterns list is empty", func() {
			Expect(IsTargetFolder("ci-12345", []string{})).To(BeFalse())
		})
	})

	Context("IsProtectedFolder", func() {
		It("returns true on exact match", func() {
			Expect(IsProtectedFolder("debug", []string{"debug", "template"})).To(BeTrue())
		})

		It("returns false on partial match", func() {
			Expect(IsProtectedFolder("debug-extra", []string{"debug", "template"})).To(BeFalse())
		})

		It("returns false when list is empty", func() {
			Expect(IsProtectedFolder("debug", []string{})).To(BeFalse())
		})

		It("returns false when folder name does not match", func() {
			Expect(IsProtectedFolder("ci-12345", []string{"debug", "template"})).To(BeFalse())
		})
	})

	Context("IsTargetResourcePool", func() {
		It("returns true when name has target prefix", func() {
			Expect(IsTargetResourcePool("ci-12345", []string{"ci-", "qeci-"})).To(BeTrue())
		})

		It("returns true for qeci- prefix", func() {
			Expect(IsTargetResourcePool("qeci-67890", []string{"ci-", "qeci-"})).To(BeTrue())
		})

		It("returns false when name does not match any prefix", func() {
			Expect(IsTargetResourcePool("production-pool", []string{"ci-", "qeci-"})).To(BeFalse())
		})

		It("returns false when prefix list is empty", func() {
			Expect(IsTargetResourcePool("ci-12345", []string{})).To(BeFalse())
		})

		It("uses DefaultResourcePoolTargetPrefixes correctly", func() {
			Expect(IsTargetResourcePool("ci-op-abc", DefaultResourcePoolTargetPrefixes)).To(BeTrue())
			Expect(IsTargetResourcePool("qeci-xyz", DefaultResourcePoolTargetPrefixes)).To(BeTrue())
			Expect(IsTargetResourcePool("ipi-ci-clusters", DefaultResourcePoolTargetPrefixes)).To(BeFalse())
			Expect(IsTargetResourcePool("production", DefaultResourcePoolTargetPrefixes)).To(BeFalse())
		})
	})

	Context("IsProtectedResourcePool", func() {
		It("returns true when name matches a protected prefix", func() {
			Expect(IsProtectedResourcePool("ipi-ci-clusters", []string{"ipi-"})).To(BeTrue())
		})

		It("returns true on exact match with prefix", func() {
			Expect(IsProtectedResourcePool("ipi-ci-clusters", []string{"ipi-ci-clusters"})).To(BeTrue())
		})

		It("returns false when name does not match any protected prefix", func() {
			Expect(IsProtectedResourcePool("ci-op-12345", []string{"ipi-", "management-"})).To(BeFalse())
		})

		It("returns false when protected list is empty", func() {
			Expect(IsProtectedResourcePool("ipi-ci-clusters", []string{})).To(BeFalse())
		})

		It("returns false when name is empty", func() {
			Expect(IsProtectedResourcePool("", []string{"ipi-"})).To(BeFalse())
		})

		It("matches against multiple prefixes", func() {
			prefixes := []string{"ipi-", "management-", "prod-"}
			Expect(IsProtectedResourcePool("ipi-ci-clusters", prefixes)).To(BeTrue())
			Expect(IsProtectedResourcePool("management-pool", prefixes)).To(BeTrue())
			Expect(IsProtectedResourcePool("prod-workloads", prefixes)).To(BeTrue())
			Expect(IsProtectedResourcePool("ci-op-12345", prefixes)).To(BeFalse())
		})

		It("protects a resource pool that also matches target prefixes", func() {
			// A pool starting with "ci-" is a target, but if it's also protected it should be skipped
			targetPrefixes := DefaultResourcePoolTargetPrefixes
			protectedPrefixes := []string{"ci-special-"}
			rpName := "ci-special-pool"
			Expect(IsTargetResourcePool(rpName, targetPrefixes)).To(BeTrue())
			Expect(IsProtectedResourcePool(rpName, protectedPrefixes)).To(BeTrue())
		})
	})

	Context("IsProtectedVirtualMachine", func() {
		It("returns true when VM name matches a protected prefix", func() {
			Expect(IsProtectedVirtualMachine("kea-dhcp-server1", []string{"kea-dhcp-"})).To(BeTrue())
		})

		It("returns false when VM name does not match any prefix", func() {
			Expect(IsProtectedVirtualMachine("ci-test-vm", []string{"kea-dhcp-"})).To(BeFalse())
		})

		It("returns false when prefix list is empty", func() {
			Expect(IsProtectedVirtualMachine("kea-dhcp-server1", []string{})).To(BeFalse())
		})

		It("returns false when VM name is empty", func() {
			Expect(IsProtectedVirtualMachine("", []string{"kea-dhcp-"})).To(BeFalse())
		})

		It("matches against multiple prefixes", func() {
			prefixes := []string{"kea-dhcp-", "infra-", "bastion-"}
			Expect(IsProtectedVirtualMachine("kea-dhcp-server1", prefixes)).To(BeTrue())
			Expect(IsProtectedVirtualMachine("infra-dns", prefixes)).To(BeTrue())
			Expect(IsProtectedVirtualMachine("bastion-host", prefixes)).To(BeTrue())
			Expect(IsProtectedVirtualMachine("ci-test-vm", prefixes)).To(BeFalse())
		})
	})

	Context("IsTargetStoragePolicy", func() {
		It("returns true when policy name has the prefix", func() {
			Expect(IsTargetStoragePolicy("openshift-storage-policy-ci-123", "openshift-storage-policy-")).To(BeTrue())
		})

		It("returns false when policy name does not have the prefix", func() {
			Expect(IsTargetStoragePolicy("vSAN Default Storage Policy", "openshift-storage-policy-")).To(BeFalse())
		})
	})

	Context("IsTargetCNSVolume", func() {
		It("returns true when cluster ID has a target prefix", func() {
			Expect(IsTargetCNSVolume("ci-12345", []string{"ci-", "user-", "build-"})).To(BeTrue())
		})

		It("returns false when cluster ID does not match", func() {
			Expect(IsTargetCNSVolume("production-cluster", []string{"ci-", "user-", "build-"})).To(BeFalse())
		})
	})

	Context("IsKubevolExpired", func() {
		It("returns true when age equals threshold", func() {
			Expect(IsKubevolExpired(21, 21)).To(BeTrue())
		})

		It("returns true when age exceeds threshold", func() {
			Expect(IsKubevolExpired(30, 21)).To(BeTrue())
		})

		It("returns false when age is below threshold", func() {
			Expect(IsKubevolExpired(10, 21)).To(BeFalse())
		})

		It("works with configurable threshold", func() {
			Expect(IsKubevolExpired(5, 5)).To(BeTrue())
			Expect(IsKubevolExpired(4, 5)).To(BeFalse())
		})
	})

	Context("shouldDelete", func() {
		var reconciler *VSphereObjectReconciler

		BeforeEach(func() {
			zapLog, _ := zap.NewDevelopment()
			reconciler = &VSphereObjectReconciler{
				logger: zapr.NewLogger(zapLog),
				Logging: utils.LoggingConfig{
					EnableAuditLog: true,
				},
			}
		})

		It("returns false in dry-run mode", func() {
			reconciler.Features.DryRun = true
			Expect(reconciler.shouldDelete("folder", "test-folder")).To(BeFalse())
		})

		It("returns true when not in dry-run mode", func() {
			reconciler.Features.DryRun = false
			Expect(reconciler.shouldDelete("folder", "test-folder")).To(BeTrue())
		})
	})

	Context("isOldEnough", func() {
		var reconciler *VSphereObjectReconciler

		BeforeEach(func() {
			zapLog, _ := zap.NewDevelopment()
			reconciler = &VSphereObjectReconciler{
				logger: zapr.NewLogger(zapLog),
				Safety: utils.SafetyConfig{
					MinAgeHours: 2,
				},
			}
		})

		It("returns false on first encounter", func() {
			Expect(reconciler.isOldEnough("folder", "server1", "test-folder")).To(BeFalse())
		})

		It("returns false when min age has not elapsed", func() {
			// First call records the time
			reconciler.isOldEnough("folder", "server1", "test-folder")
			// Second call should still return false (not enough time has passed)
			Expect(reconciler.isOldEnough("folder", "server1", "test-folder")).To(BeFalse())
		})

		It("returns true after min age has elapsed", func() {
			// Manually seed firstSeen with a time far in the past
			key := "folder/server1/old-folder"
			reconciler.firstSeen.Store(key, time.Now().Add(-3*time.Hour))

			Expect(reconciler.isOldEnough("folder", "server1", "old-folder")).To(BeTrue())
		})

		It("cleans up entry after returning true", func() {
			key := "folder/server1/old-folder"
			reconciler.firstSeen.Store(key, time.Now().Add(-3*time.Hour))

			reconciler.isOldEnough("folder", "server1", "old-folder")

			// Entry should have been deleted
			_, loaded := reconciler.firstSeen.Load(key)
			Expect(loaded).To(BeFalse())
		})

		It("tracks different objects independently", func() {
			reconciler.firstSeen = sync.Map{}
			reconciler.isOldEnough("folder", "server1", "folder-a")
			reconciler.isOldEnough("tag", "server1", "tag-b")

			_, loadedA := reconciler.firstSeen.Load("folder/server1/folder-a")
			_, loadedB := reconciler.firstSeen.Load("tag/server1/tag-b")
			Expect(loadedA).To(BeTrue())
			Expect(loadedB).To(BeTrue())
		})
	})
})
