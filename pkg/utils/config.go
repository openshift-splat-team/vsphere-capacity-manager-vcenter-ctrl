package utils

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// ProtectionConfig defines prefixes/patterns used to protect vSphere objects from cleanup.
type ProtectionConfig struct {
	Tags          []string `json:"tags"`
	Folders       []string `json:"folders"`
	ResourcePools []string `json:"resourcepools"`
}

// SafetyConfig defines safety thresholds for cleanup operations.
type SafetyConfig struct {
	MinAgeHours        int `json:"min_age_hours"`
	GracePeriodHours   int `json:"grace_period_hours"`
	KubevolsMinAgeDays int `json:"kubevols_min_age_days"`
}

// CleanupIntervalsConfig defines the delay (in hours) between runs of each cleanup function.
type CleanupIntervalsConfig struct {
	Folders         int `json:"folders"`
	Tags            int `json:"tags"`
	CNSVolumes      int `json:"cnsvolumes"`
	ResourcePools   int `json:"resourcepools"`
	StoragePolicies int `json:"storagepolicies"`
	Kubevols        int `json:"kubevols"`
}

// CleanupConfig groups interval settings for cleanup functions.
type CleanupConfig struct {
	Intervals CleanupIntervalsConfig `json:"intervals"`
}

// FeaturesConfig controls feature flags for the controller.
type FeaturesConfig struct {
	DryRun                bool `json:"dry_run"`
	EnableFolders         bool `json:"enable_folders"`
	EnableTags            bool `json:"enable_tags"`
	EnableCNSVolumes      bool `json:"enable_cnsvolumes"`
	EnableResourcePools   bool `json:"enable_resourcepools"`
	EnableStoragePolicies bool `json:"enable_storagepolicies"`
	EnableKubevols        bool `json:"enable_kubevols"`
}

// LoggingConfig controls the logging behavior of the controller.
type LoggingConfig struct {
	Level          string `json:"level"`
	Format         string `json:"format"`
	EnableAuditLog bool   `json:"enable_audit_log"`
}

// ControllerConfig holds the parsed configuration from the controller ConfigMap.
type ControllerConfig struct {
	VCenters   []VCenterConfig  `json:"vcenters"`
	Cleanup    CleanupConfig    `json:"cleanup"`
	Safety     SafetyConfig     `json:"safety"`
	Features   FeaturesConfig   `json:"features"`
	Protection ProtectionConfig `json:"protection"`
	Logging    LoggingConfig    `json:"logging"`
}

// VCenterConfig maps a vCenter server to its credential Secret.
type VCenterConfig struct {
	Server    string `json:"server"`
	SecretRef string `json:"secretRef"`
}

// ReadControllerConfig reads and parses the controller ConfigMap into a ControllerConfig.
func ReadControllerConfig(namespace, configMapName string, c client.Client) (*ControllerConfig, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*60)
	defer cancel()

	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Name: configMapName, Namespace: namespace}, &cm); err != nil {
		return nil, fmt.Errorf("failed to get ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	configData, ok := cm.Data["config.yaml"]
	if !ok {
		return nil, fmt.Errorf("ConfigMap %s/%s missing 'config.yaml' key", namespace, configMapName)
	}

	var config ControllerConfig
	if err := yaml.Unmarshal([]byte(configData), &config); err != nil {
		return nil, fmt.Errorf("failed to parse config.yaml from ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	if len(config.VCenters) == 0 {
		return nil, fmt.Errorf("no vcenters configured in ConfigMap %s/%s", namespace, configMapName)
	}

	config.applyDefaults()

	return &config, nil
}

func (c *ControllerConfig) applyDefaults() {
	// Protection defaults
	if len(c.Protection.Tags) == 0 {
		c.Protection.Tags = []string{"us-"}
	}
	if len(c.Protection.Folders) == 0 {
		c.Protection.Folders = []string{"debug", "template"}
	}
	if len(c.Protection.ResourcePools) == 0 {
		c.Protection.ResourcePools = []string{"ci-", "qeci-"}
	}

	// Safety defaults
	if c.Safety.MinAgeHours == 0 {
		c.Safety.MinAgeHours = 2
	}
	if c.Safety.GracePeriodHours == 0 {
		c.Safety.GracePeriodHours = 12
	}
	if c.Safety.KubevolsMinAgeDays == 0 {
		c.Safety.KubevolsMinAgeDays = 21
	}

	// Cleanup interval defaults (hours)
	if c.Cleanup.Intervals.Folders == 0 {
		c.Cleanup.Intervals.Folders = 1
	}
	if c.Cleanup.Intervals.Tags == 0 {
		c.Cleanup.Intervals.Tags = 8
	}
	if c.Cleanup.Intervals.CNSVolumes == 0 {
		c.Cleanup.Intervals.CNSVolumes = 8
	}
	if c.Cleanup.Intervals.ResourcePools == 0 {
		c.Cleanup.Intervals.ResourcePools = 2
	}
	if c.Cleanup.Intervals.StoragePolicies == 0 {
		c.Cleanup.Intervals.StoragePolicies = 12
	}
	if c.Cleanup.Intervals.Kubevols == 0 {
		c.Cleanup.Intervals.Kubevols = 24
	}

	// Features defaults: if no enable flags are set, enable all
	if !c.Features.EnableFolders && !c.Features.EnableTags && !c.Features.EnableCNSVolumes &&
		!c.Features.EnableResourcePools && !c.Features.EnableStoragePolicies && !c.Features.EnableKubevols {
		c.Features.EnableFolders = true
		c.Features.EnableTags = true
		c.Features.EnableCNSVolumes = true
		c.Features.EnableResourcePools = true
		c.Features.EnableStoragePolicies = true
		c.Features.EnableKubevols = true
	}

	// Logging defaults
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}
}
