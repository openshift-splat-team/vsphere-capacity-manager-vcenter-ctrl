package controller

import (
	"context"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/vmware/govmomi/cns"
	cnstypes "github.com/vmware/govmomi/cns/types"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/pbm"
	pbmtypes "github.com/vmware/govmomi/pbm/types"
	"github.com/vmware/govmomi/vim25/mo"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/vsphere"
)

type VSphereObjectReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	*vsphere.Metadata

	logger logr.Logger
}
type pruneFunctions struct {
	name    string
	execute func(ctx context.Context) error
	delay   time.Duration
	lastRun time.Time
}

func (v *VSphereObjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	v.Client = mgr.GetClient()
	v.Scheme = mgr.GetScheme()

	zapLog, err := zap.NewProduction()
	if err != nil {
		return err
	}
	v.logger = zapr.NewLogger(zapLog)

	go func() {
		ctx := context.TODO()

		pf := []pruneFunctions{
			{
				name:    "folders",
				execute: v.folder,
				delay:   time.Hour,
				lastRun: time.Now(),
			},
			{
				name:    "tags",
				execute: v.tag,
				delay:   time.Hour * 8,
				lastRun: time.Now(),
			},
			{
				name:    "cnsvolumes",
				execute: v.cns,
				delay:   time.Hour * 8,
				lastRun: time.Now(),
			},
			{
				name:    "resourcepools",
				execute: v.resourcepool,
				delay:   time.Hour * 2,
				lastRun: time.Now(),
			},
			{
				name:    "storagepolicies",
				execute: v.storagepolicy,
				delay:   time.Hour * 12,
				lastRun: time.Now(),
			},
		}

		for {
			v.logger.Info("Waiting for prune functions to finish...")
			if err := v.PruneVSphereObjects(ctx, pf); err != nil {
				v.logger.WithName("setup").Error(err, "")
			}

			time.Sleep(5 * time.Minute)
		}
	}()

	return nil
}

func (v *VSphereObjectReconciler) cns(ctx context.Context) error {
	for server := range v.Metadata.VCenterCredentials {
		var c *cns.Client
		var qr *cnstypes.CnsQueryResult

		s, err := v.Metadata.Session(ctx, server)

		if err != nil {
			v.logger.WithName("cns").Error(err, "vcenter", "name", server)
			continue
		}

		_, folderNameMap, err := getFolderList(s, v.logger)
		if err != nil {
			v.logger.WithName("cns").Error(err, "folders")
			continue
		}

		c, err = cns.NewClient(ctx, s.Client.Client)
		if err != nil {
			v.logger.WithName("cns").Error(err, "client")
			continue
		}

		qr, err = c.QueryAllVolume(ctx, cnstypes.CnsQueryFilter{}, cnstypes.CnsQuerySelection{})
		if err != nil {
			v.logger.WithName("cns").Error(err, "QueryAllVolume")
			continue
		}

		for _, vol := range qr.Volumes {
			if strings.HasPrefix(vol.Metadata.ContainerCluster.ClusterId, "ci-") {
				if _, ok := folderNameMap[vol.Metadata.ContainerCluster.ClusterId]; !ok {
					v.logger.WithName("cns").Info("delete volume", "clusterid", vol.Metadata.ContainerCluster.ClusterId)

					var task *object.Task
					var err error
					task, err = c.DeleteVolume(ctx, []cnstypes.CnsVolumeId{vol.VolumeId}, true)
					if err != nil {
						v.logger.WithName("cns").Error(err, "delete volume")
						task, err = c.DeleteVolume(ctx, []cnstypes.CnsVolumeId{vol.VolumeId}, false)
						if err != nil {
							v.logger.WithName("cns").Error(err, "delete volume")
							continue
						}
					}

					if err := task.Wait(ctx); err != nil {
						v.logger.WithName("cns").Error(err, "task wait delete volume")
						continue
					}
				}
			}
		}
	}

	return nil
}

func getFolderList(s *session.Session, logger logr.Logger) ([]*object.Folder, map[string]bool, error) {
	ctx := context.Background()
	var allFolders []*object.Folder
	folderNameMap := make(map[string]bool)
	datacenters, err := s.Finder.DatacenterList(ctx, "*")
	if err != nil {
		return nil, nil, err
	}
	for _, dc := range datacenters {
		logger.WithName("folder").Info("datacenter", "name", dc.Name())
		s.Finder.SetDatacenter(dc)
		tempFolders, err := s.Finder.FolderList(ctx, "*")
		if err != nil {
			logger.WithName("folder").Error(err, "list")
			continue
		}

		for _, f := range tempFolders {
			folderName := f.Name()
			if strings.Contains(folderName, "ci-") || strings.Contains(folderName, "user-") || strings.Contains(folderName, "build-") {
				allFolders = append(allFolders, f)
				folderNameMap[folderName] = true
			}
		}

	}
	return allFolders, folderNameMap, nil
}

func (v *VSphereObjectReconciler) folder(ctx context.Context) error {
	for server := range v.Metadata.VCenterCredentials {
		v.logger.WithName("folder").Info("vcenter", "name", server)
		s, err := v.Metadata.Session(ctx, server)
		if err != nil {
			v.logger.WithName("folder").Error(err, "vcenter", "name", server)
			continue
		}

		folders, _, err := getFolderList(s, v.logger)
		if err != nil {
			v.logger.WithName("folder").Error(err, "getFolderList")
			continue
		}

		for _, f := range folders {
			v.logger.WithName("folder").Info("delete", "name", f.Name())
			fc, err := f.Children(ctx)
			if err != nil {
				v.logger.WithName("folder").Error(err, "children")
				continue
			}
			v.logger.WithName("folder").Info("children", "name", f.Name(), "length", len(fc))
			if len(fc) == 0 {
				var task *object.Task
				v.logger.WithName("folder").Info("delete", "name", f.Name())
				task, err = f.Destroy(ctx)
				if err != nil {
					v.logger.WithName("folder").Error(err, "destroy")
					continue
				}

				if err = task.Wait(ctx); err != nil {
					v.logger.WithName("folder").Error(err, "task")
					continue
				}
			}
		}
	}
	return nil
}

/*
 * rp
 * storage policies
 */

func (v *VSphereObjectReconciler) tag(ctx context.Context) error {
	for server := range v.Metadata.VCenterCredentials {
		v.logger.WithName("tag").Info("vcenter", "name", server)
		s, err := v.Metadata.Session(ctx, server)
		if err != nil {
			v.logger.WithName("tag").Error(err, "vcenter", "name", server)
			continue
		}
		allTags, err := s.TagManager.GetTags(ctx)
		if err != nil {
			v.logger.WithName("tag").Error(err, "GetTags")
			continue
		}
		_, folderNameMap, err := getFolderList(s, v.logger)
		if err != nil {
			v.logger.WithName("tag").Error(err, "getFolderList")
			continue
		}

		for _, t := range allTags {
			// Only process CI tags
			if !strings.Contains(t.Name, "ci-") {
				continue
			}

			// Protect zonal tags (us-east, us-west, etc.)
			if strings.HasPrefix(t.Name, "us-") {
				v.logger.WithName("tag").Info("skipping protected zonal tag", "name", t.Name)
				continue
			}

			// Skip if tag name matches an existing folder (cluster still active)
			if _, ok := folderNameMap[t.Name]; ok {
				v.logger.WithName("tag").Info("skipping tag with matching folder", "name", t.Name)
				continue
			}

			// Get attached objects
			attached, err := s.TagManager.GetAttachedObjectsOnTags(ctx, []string{t.ID})
			if err != nil {
				v.logger.WithName("tag").Error(err, "GetAttachedObjectsOnTags", "tag", t.Name)
				continue
			}

			attachmentCount := len(attached)
			v.logger.WithName("tag").Info("tag attachment count", "name", t.Name, "count", attachmentCount)

			// FIXED BUG: Only delete tags with ZERO attachments (was <= 1)
			// Tags with any attachments should not be deleted
			if attachmentCount == 0 {
				v.logger.WithName("tag").Info("deleting unused tag", "name", t.Name)
				if err := s.TagManager.DeleteTag(ctx, &t); err != nil {
					v.logger.WithName("tag").Error(err, "DeleteTag", "tag", t.Name)
					continue
				}

				// Try to delete the category if it's now empty
				cat, err := s.TagManager.GetCategory(ctx, t.CategoryID)
				if err != nil {
					v.logger.WithName("tag").Error(err, "GetCategory", "categoryID", t.CategoryID)
					continue
				}

				// Get remaining tags in this category
				tagsInCategory, err := s.TagManager.GetTagsForCategory(ctx, t.CategoryID)
				if err != nil {
					v.logger.WithName("tag").Error(err, "GetTagsForCategory", "category", cat.Name)
					continue
				}

				// Only delete category if it's now empty
				if len(tagsInCategory) == 0 {
					v.logger.WithName("tag").Info("deleting empty category", "name", cat.Name)
					if err := s.TagManager.DeleteCategory(ctx, cat); err != nil {
						v.logger.WithName("tag").Error(err, "DeleteCategory", "category", cat.Name)
						continue
					}
				} else {
					v.logger.WithName("tag").Info("skipping category deletion", "name", cat.Name, "remaining_tags", len(tagsInCategory))
				}
			} else {
				v.logger.WithName("tag").Info("skipping tag with attachments", "name", t.Name, "count", attachmentCount)
			}
		}
	}

	return nil
}

func (v *VSphereObjectReconciler) resourcepool(ctx context.Context) error {
	for server := range v.Metadata.VCenterCredentials {
		v.logger.WithName("resourcepool").Info("vcenter", "name", server)
		s, err := v.Metadata.Session(ctx, server)
		if err != nil {
			v.logger.WithName("resourcepool").Error(err, "vcenter", "name", server)
			continue
		}

		// Get all datacenters
		datacenters, err := s.Finder.DatacenterList(ctx, "*")
		if err != nil {
			v.logger.WithName("resourcepool").Error(err, "datacenter list")
			continue
		}

		for _, dc := range datacenters {
			v.logger.WithName("resourcepool").Info("datacenter", "name", dc.Name())
			s.Finder.SetDatacenter(dc)

			// Find all resource pools in this datacenter
			resourcePools, err := s.Finder.ResourcePoolList(ctx, "*")
			if err != nil {
				v.logger.WithName("resourcepool").Error(err, "list")
				continue
			}

			for _, rp := range resourcePools {
				rpName := rp.Name()

				// Only process resource pools matching ci-* or qeci-* patterns
				if !strings.HasPrefix(rpName, "ci-") && !strings.HasPrefix(rpName, "qeci-") {
					continue
				}

				v.logger.WithName("resourcepool").Info("checking", "name", rpName)

				// Get VMs in this resource pool using property collector
				var rpMo mo.ResourcePool
				err = rp.Properties(ctx, rp.Reference(), []string{"vm"}, &rpMo)
				if err != nil {
					v.logger.WithName("resourcepool").Error(err, "get properties", "name", rpName)
					continue
				}

				vmCount := len(rpMo.Vm)

				// Only delete if resource pool is empty
				if vmCount == 0 {
					v.logger.WithName("resourcepool").Info("delete empty resource pool", "name", rpName)
					task, err := rp.Destroy(ctx)
					if err != nil {
						v.logger.WithName("resourcepool").Error(err, "destroy", "name", rpName)
						continue
					}

					if err := task.Wait(ctx); err != nil {
						v.logger.WithName("resourcepool").Error(err, "task wait", "name", rpName)
						continue
					}

					v.logger.WithName("resourcepool").Info("deleted", "name", rpName)
				} else {
					v.logger.WithName("resourcepool").Info("skipping non-empty resource pool", "name", rpName, "vm_count", vmCount)
				}
			}
		}
	}

	return nil
}

func (v *VSphereObjectReconciler) storagepolicy(ctx context.Context) error {
	for server := range v.Metadata.VCenterCredentials {
		v.logger.WithName("storagepolicy").Info("vcenter", "name", server)
		s, err := v.Metadata.Session(ctx, server)
		if err != nil {
			v.logger.WithName("storagepolicy").Error(err, "vcenter", "name", server)
			continue
		}

		// Create PBM client for storage policy management
		pbmClient, err := pbm.NewClient(ctx, s.Client.Client)
		if err != nil {
			v.logger.WithName("storagepolicy").Error(err, "create pbm client")
			continue
		}

		// Get all storage policies
		resourceType := pbmtypes.PbmProfileResourceType{
			ResourceType: string(pbmtypes.PbmProfileResourceTypeEnumSTORAGE),
		}
		profileIds, err := pbmClient.QueryProfile(ctx, resourceType, "")
		if err != nil {
			v.logger.WithName("storagepolicy").Error(err, "query profiles")
			continue
		}

		// Retrieve policy details
		policies, err := pbmClient.RetrieveContent(ctx, profileIds)
		if err != nil {
			v.logger.WithName("storagepolicy").Error(err, "retrieve policy content")
			continue
		}

		// Get folder list for cluster existence checks
		_, folderNameMap, err := getFolderList(s, v.logger)
		if err != nil {
			v.logger.WithName("storagepolicy").Error(err, "getFolderList")
			continue
		}

		// Process each policy
		for _, policy := range policies {
			policyName := policy.GetPbmProfile().Name

			// Check if policy matches openshift-storage-policy- pattern
			if strings.HasPrefix(policyName, "openshift-storage-policy-") {
				// Extract cluster ID from policy name
				clusterId := strings.TrimPrefix(policyName, "openshift-storage-policy-")

				if clusterId != "" {
					v.logger.WithName("storagepolicy").Info("checking policy", "name", policyName, "clusterId", clusterId)

					// Check if cluster folder exists (cluster still active)
					clusterExists := false
					for folderName := range folderNameMap {
						if strings.HasPrefix(folderName, clusterId) {
							clusterExists = true
							v.logger.WithName("storagepolicy").Info("cluster exists", "clusterId", clusterId, "folder", folderName)
							break
						}
					}

					// Delete policy if cluster doesn't exist
					if !clusterExists {
						v.logger.WithName("storagepolicy").Info("deleting orphaned storage policy", "name", policyName, "clusterId", clusterId)
						profileId := []pbmtypes.PbmProfileId{policy.GetPbmProfile().ProfileId}
						_, err := pbmClient.DeleteProfile(ctx, profileId)
						if err != nil {
							v.logger.WithName("storagepolicy").Error(err, "delete policy", "name", policyName)
							continue
						}
						v.logger.WithName("storagepolicy").Info("deleted policy", "name", policyName)
					} else {
						v.logger.WithName("storagepolicy").Info("skipping policy for active cluster", "name", policyName, "clusterId", clusterId)
					}
				}
			}
		}
	}

	return nil
}

func (v *VSphereObjectReconciler) PruneVSphereObjects(ctx context.Context, pf []pruneFunctions) error {
	for i, f := range pf {
		pastTime := time.Now().Add(-(f.delay))
		v.logger.WithName("prune").Info("function", "name", f.name, "at", pastTime)
		if pastTime.After(f.lastRun) {
			v.logger.WithName("prune").Info("running", "function", f.name)
			if err := f.execute(ctx); err != nil {
				v.logger.WithName("prune").Error(err, "execute error")
			}
			pf[i].lastRun = time.Now()
		}
	}
	return nil
}
