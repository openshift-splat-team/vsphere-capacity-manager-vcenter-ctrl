package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/vmware/govmomi/cns"
	cnstypes "github.com/vmware/govmomi/cns/types"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/pbm"
	pbmtypes "github.com/vmware/govmomi/pbm/types"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/utils"
	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/vsphere"
)

type VSphereObjectReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	*vsphere.Metadata
	Protection utils.ProtectionConfig
	Safety     utils.SafetyConfig
	Features   utils.FeaturesConfig
	Cleanup    utils.CleanupConfig
	Logging    utils.LoggingConfig

	logger    logr.Logger
	firstSeen sync.Map // key: "objectType/server/objectName" → value: time.Time
}

type pruneFunctions struct {
	name    string
	execute func(ctx context.Context) error
	delay   time.Duration
	lastRun time.Time
	enabled bool
}

func (v *VSphereObjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	v.Client = mgr.GetClient()
	v.Scheme = mgr.GetScheme()

	zapLog, err := v.buildLogger()
	if err != nil {
		return err
	}
	v.logger = zapr.NewLogger(zapLog)

	gracePeriod := time.Duration(v.Safety.GracePeriodHours) * time.Hour
	if gracePeriod < 5*time.Minute {
		gracePeriod = 5 * time.Minute
	}

	go func() {
		ctx := context.TODO()

		pf := []pruneFunctions{
			{
				name:    "folders",
				execute: v.folder,
				delay:   time.Hour * time.Duration(v.Cleanup.Intervals.Folders),
				lastRun: time.Now(),
				enabled: v.Features.EnableFolders,
			},
			{
				name:    "tags",
				execute: v.tag,
				delay:   time.Hour * time.Duration(v.Cleanup.Intervals.Tags),
				lastRun: time.Now(),
				enabled: v.Features.EnableTags,
			},
			{
				name:    "cnsvolumes",
				execute: v.cns,
				delay:   time.Hour * time.Duration(v.Cleanup.Intervals.CNSVolumes),
				lastRun: time.Now(),
				enabled: v.Features.EnableCNSVolumes,
			},
			{
				name:    "resourcepools",
				execute: v.resourcepool,
				delay:   time.Hour * time.Duration(v.Cleanup.Intervals.ResourcePools),
				lastRun: time.Now(),
				enabled: v.Features.EnableResourcePools,
			},
			{
				name:    "storagepolicies",
				execute: v.storagepolicy,
				delay:   time.Hour * time.Duration(v.Cleanup.Intervals.StoragePolicies),
				lastRun: time.Now(),
				enabled: v.Features.EnableStoragePolicies,
			},
			{
				name:    "kubevols",
				execute: v.kubevols,
				delay:   time.Hour * time.Duration(v.Cleanup.Intervals.Kubevols),
				lastRun: time.Now(),
				enabled: v.Features.EnableKubevols,
			},
		}

		for {
			v.logger.Info("Waiting for prune functions to finish...")
			if err := v.PruneVSphereObjects(ctx, pf); err != nil {
				v.logger.WithName("setup").Error(err, "")
			}

			time.Sleep(gracePeriod)
		}
	}()

	return nil
}

func (v *VSphereObjectReconciler) buildLogger() (*zap.Logger, error) {
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(v.Logging.Level)); err != nil {
		level = zapcore.InfoLevel
	}

	var cfg zap.Config
	switch v.Logging.Format {
	case "console":
		cfg = zap.NewDevelopmentConfig()
	default:
		cfg = zap.NewProductionConfig()
	}
	cfg.Level = zap.NewAtomicLevelAt(level)

	return cfg.Build()
}

// shouldDelete checks dry-run mode and performs audit logging.
// Returns true if the caller should proceed with deletion.
func (v *VSphereObjectReconciler) shouldDelete(functionName, objectName string) bool {
	if v.Features.DryRun {
		v.logger.WithName(functionName).Info("[DRY RUN] would delete", "object", objectName)
		v.auditLog(functionName, "dry-run-skip", objectName)
		return false
	}
	v.auditLog(functionName, "delete", objectName)
	return true
}

// isOldEnough tracks first-seen time for deletion candidates and returns true
// only after MinAgeHours has elapsed since the object was first seen.
// Does not apply to kubevols (they have their own file-age check).
func (v *VSphereObjectReconciler) isOldEnough(objectType, server, objectName string) bool {
	key := fmt.Sprintf("%s/%s/%s", objectType, server, objectName)
	now := time.Now()

	val, loaded := v.firstSeen.LoadOrStore(key, now)
	if !loaded {
		v.logger.WithName(objectType).Info("first seen, tracking for min age", "object", objectName, "server", server)
		return false
	}

	firstSeenTime := val.(time.Time)
	minAge := time.Duration(v.Safety.MinAgeHours) * time.Hour
	if time.Since(firstSeenTime) >= minAge {
		v.firstSeen.Delete(key)
		return true
	}

	v.logger.WithName(objectType).Info("not old enough for deletion",
		"object", objectName, "server", server,
		"first_seen", firstSeenTime, "min_age_hours", v.Safety.MinAgeHours)
	return false
}

// auditLog writes a structured audit log entry for deletion actions.
func (v *VSphereObjectReconciler) auditLog(functionName, action, objectName string) {
	if v.Logging.EnableAuditLog {
		v.logger.WithName("audit").Info("cleanup action",
			"function", functionName, "action", action, "object", objectName)
	}
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

		_, folderNameMap, err := getFolderList(s, v.logger, v.Protection.Folders)
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
			if IsTargetCNSVolume(vol.Metadata.ContainerCluster.ClusterId, DefaultFolderTargetPatterns) {
				if _, ok := folderNameMap[vol.Metadata.ContainerCluster.ClusterId]; !ok {
					if !v.isOldEnough("cns", server, vol.Metadata.ContainerCluster.ClusterId) {
						continue
					}
					if !v.shouldDelete("cns", vol.Metadata.ContainerCluster.ClusterId) {
						continue
					}
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

func getFolderList(s *session.Session, logger logr.Logger, protectedFolders []string) ([]*object.Folder, map[string]bool, error) {
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
			if IsTargetFolder(folderName, DefaultFolderTargetPatterns) && !IsProtectedFolder(folderName, protectedFolders) {
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

		folders, _, err := getFolderList(s, v.logger, v.Protection.Folders)
		if err != nil {
			v.logger.WithName("folder").Error(err, "getFolderList")
			continue
		}

		for _, f := range folders {
			v.logger.WithName("folder").Info("checking", "name", f.Name())
			fc, err := f.Children(ctx)
			if err != nil {
				v.logger.WithName("folder").Error(err, "children")
				continue
			}
			v.logger.WithName("folder").Info("children", "name", f.Name(), "length", len(fc))
			if len(fc) == 0 {
				if !v.isOldEnough("folder", server, f.Name()) {
					continue
				}
				if !v.shouldDelete("folder", f.Name()) {
					continue
				}
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
		_, folderNameMap, err := getFolderList(s, v.logger, v.Protection.Folders)
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
			if IsProtectedTag(t.Name, v.Protection.Tags) {
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

			// Only delete tags with ZERO attachments
			if attachmentCount == 0 {
				if !v.isOldEnough("tag", server, t.Name) {
					continue
				}
				if !v.shouldDelete("tag", t.Name) {
					continue
				}
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

				// Only process resource pools matching target prefixes
				if !IsTargetResourcePool(rpName, v.Protection.ResourcePools) {
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
					if !v.isOldEnough("resourcepool", server, rpName) {
						continue
					}
					if !v.shouldDelete("resourcepool", rpName) {
						continue
					}
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
		_, folderNameMap, err := getFolderList(s, v.logger, v.Protection.Folders)
		if err != nil {
			v.logger.WithName("storagepolicy").Error(err, "getFolderList")
			continue
		}

		// Process each policy
		for _, policy := range policies {
			policyName := policy.GetPbmProfile().Name

			// Check if policy matches openshift-storage-policy- pattern
			if IsTargetStoragePolicy(policyName, "openshift-storage-policy-") {
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
						if !v.isOldEnough("storagepolicy", server, policyName) {
							continue
						}
						if !v.shouldDelete("storagepolicy", policyName) {
							continue
						}
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

func (v *VSphereObjectReconciler) kubevols(ctx context.Context) error {
	const (
		kubevolPath     = "kubevols"
		vmdkFilePattern = "*.vmdk"
	)

	minAgeInDays := v.Safety.KubevolsMinAgeDays

	for server := range v.Metadata.VCenterCredentials {
		v.logger.WithName("kubevols").Info("vcenter", "name", server)
		s, err := v.Metadata.Session(ctx, server)
		if err != nil {
			v.logger.WithName("kubevols").Error(err, "vcenter", "name", server)
			continue
		}

		// Get all datastores to search for kubevols directories
		datacenters, err := s.Finder.DatacenterList(ctx, "*")
		if err != nil {
			v.logger.WithName("kubevols").Error(err, "datacenter list")
			continue
		}

		for _, dc := range datacenters {
			s.Finder.SetDatacenter(dc)

			datastores, err := s.Finder.DatastoreList(ctx, "*")
			if err != nil {
				v.logger.WithName("kubevols").Error(err, "datastore list")
				continue
			}

			for _, ds := range datastores {
				v.logger.WithName("kubevols").Info("checking datastore", "name", ds.Name())

				// Create datastore browser
				browser, err := ds.Browser(ctx)
				if err != nil {
					v.logger.WithName("kubevols").Error(err, "get browser", "datastore", ds.Name())
					continue
				}

				// Construct the kubevols path
				kubevolsPath := ds.Path(kubevolPath)

				// Create search spec for .vmdk files
				searchSpec := types.HostDatastoreBrowserSearchSpec{
					MatchPattern: []string{vmdkFilePattern},
					Details: &types.FileQueryFlags{
						FileType:     true,
						FileSize:     true,
						Modification: true,
					},
				}

				// Search for files in kubevols directory
				task, err := browser.SearchDatastore(ctx, kubevolsPath, &searchSpec)
				if err != nil {
					// Directory might not exist on this datastore, continue
					if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "does not exist") {
						v.logger.WithName("kubevols").Info("kubevols directory not found", "datastore", ds.Name())
						continue
					}
					v.logger.WithName("kubevols").Error(err, "search datastore", "datastore", ds.Name())
					continue
				}

				result, err := task.WaitForResult(ctx)
				if err != nil {
					v.logger.WithName("kubevols").Error(err, "wait for search result", "datastore", ds.Name())
					continue
				}

				searchResult, ok := result.Result.(types.HostDatastoreBrowserSearchResults)
				if !ok {
					v.logger.WithName("kubevols").Error(nil, "unexpected search result type", "datastore", ds.Name())
					continue
				}

				// Process each file
				for _, fileInfo := range searchResult.File {
					if fileInfo.GetFileInfo() == nil {
						continue
					}

					file := fileInfo.GetFileInfo()
					if file.Modification == nil {
						v.logger.WithName("kubevols").Info("skipping file without modification time", "file", file.Path)
						continue
					}

					// Calculate age of the file
					age := time.Since(*file.Modification)
					ageInDays := int(age.Hours() / 24)

					if IsKubevolExpired(ageInDays, minAgeInDays) {
						filePath := ds.Path(kubevolPath + "/" + file.Path)
						if !v.shouldDelete("kubevols", filePath) {
							continue
						}
						v.logger.WithName("kubevols").Info("deleting old kubevol file",
							"file", file.Path,
							"age_days", ageInDays,
							"path", filePath)

						// Delete the file
						fileManager := object.NewFileManager(s.Client.Client)
						deleteTask, err := fileManager.DeleteDatastoreFile(ctx, filePath, dc)
						if err != nil {
							v.logger.WithName("kubevols").Error(err, "delete file", "file", filePath)
							continue
						}

						if err := deleteTask.Wait(ctx); err != nil {
							v.logger.WithName("kubevols").Error(err, "wait for delete", "file", filePath)
							continue
						}

						v.logger.WithName("kubevols").Info("deleted old kubevol file", "file", file.Path, "age_days", ageInDays)
					} else {
						v.logger.WithName("kubevols").Info("skipping recent file", "file", file.Path, "age_days", ageInDays)
					}
				}
			}
		}
	}

	return nil
}

func (v *VSphereObjectReconciler) PruneVSphereObjects(ctx context.Context, pf []pruneFunctions) error {
	for i, f := range pf {
		if !f.enabled {
			v.logger.WithName("prune").Info("skipping disabled function", "name", f.name)
			continue
		}
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
