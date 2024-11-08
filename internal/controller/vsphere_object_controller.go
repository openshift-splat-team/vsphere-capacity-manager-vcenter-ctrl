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
	for server, _ := range v.Metadata.VCenterCredentials {
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

		qr, err = c.QueryVolume(ctx, cnstypes.CnsQueryFilter{})
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
	for server, _ := range v.Metadata.VCenterCredentials {
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
	for server, _ := range v.Metadata.VCenterCredentials {
		v.logger.WithName("tag").Info("vcenter", "name", server)
		s, err := v.Metadata.Session(ctx, server)
		if err != nil {
			v.logger.WithName("tag").Error(err, "vcenter", "name", server)
			continue
		}
		allTags, err := s.TagManager.GetTags(ctx)
		if err != nil {
			v.logger.WithName("tag").Error(err, "GetTags")
		}
		_, folderNameMap, err := getFolderList(s, v.logger)
		if err != nil {
			v.logger.WithName("folder").Error(err, "getFolderList")
			continue
		}

		for _, t := range allTags {
			if strings.Contains(t.Name, "ci-") {
				if _, ok := folderNameMap[t.Name]; !ok {
					attached, err := s.TagManager.GetAttachedObjectsOnTags(ctx, []string{t.ID})
					if err != nil {
						v.logger.WithName("tag").Error(err, "GetAttachedObjectsOnTags")
						continue
					}

					v.logger.WithName("tag").Info("attached", "name", t.Name, "length", len(attached))

					// Why <= 1?
					// The tag attachment to the datastore remains if the tag is not previously deleted by the installer
					if len(attached) <= 1 {
						v.logger.WithName("tag").Info("delete", "name", t.Name)
						if err := s.TagManager.DeleteTag(ctx, &t); err != nil {
							v.logger.WithName("tag").Error(err, "GetCategory")
							continue
						}

						cat, err := s.TagManager.GetCategory(ctx, t.CategoryID)
						if err != nil {
							v.logger.WithName("tag").Error(err, "GetCategory")
							continue
						}
						v.logger.WithName("tag").Info("delete", "name", cat.Name)
						if err := s.TagManager.DeleteCategory(ctx, cat); err != nil {
							v.logger.WithName("tag").Error(err, "GetCategory")
							continue
						}
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
