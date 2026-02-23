package utils

import (
	"testing"

	"sigs.k8s.io/yaml"
)

func TestControllerConfig_ParsesProtection(t *testing.T) {
	data := `
vcenters:
  - server: 10.0.0.1
    secretRef: secret1
protection:
  tags:
    - eu-
    - ap-
  folders:
    - staging
    - test
  resourcepools:
    - dev-
    - qa-
safety:
  kubevols_min_age_days: 30
`
	var config ControllerConfig
	if err := yaml.Unmarshal([]byte(data), &config); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(config.Protection.Tags) != 2 || config.Protection.Tags[0] != "eu-" {
		t.Errorf("expected tags [eu-, ap-], got %v", config.Protection.Tags)
	}
	if len(config.Protection.Folders) != 2 || config.Protection.Folders[0] != "staging" {
		t.Errorf("expected folders [staging, test], got %v", config.Protection.Folders)
	}
	if len(config.Protection.ResourcePools) != 2 || config.Protection.ResourcePools[0] != "dev-" {
		t.Errorf("expected resourcepools [dev-, qa-], got %v", config.Protection.ResourcePools)
	}
	if config.Safety.KubevolsMinAgeDays != 30 {
		t.Errorf("expected kubevols_min_age_days 30, got %d", config.Safety.KubevolsMinAgeDays)
	}
}

func TestControllerConfig_ApplyDefaults(t *testing.T) {
	config := ControllerConfig{}
	config.applyDefaults()

	if len(config.Protection.Tags) != 1 || config.Protection.Tags[0] != "us-" {
		t.Errorf("expected default tags [us-], got %v", config.Protection.Tags)
	}
	if len(config.Protection.Folders) != 2 || config.Protection.Folders[0] != "debug" {
		t.Errorf("expected default folders [debug, template], got %v", config.Protection.Folders)
	}
	if len(config.Protection.ResourcePools) != 2 || config.Protection.ResourcePools[0] != "ci-" {
		t.Errorf("expected default resourcepools [ci-, qeci-], got %v", config.Protection.ResourcePools)
	}
	if config.Safety.KubevolsMinAgeDays != 21 {
		t.Errorf("expected default kubevols_min_age_days 21, got %d", config.Safety.KubevolsMinAgeDays)
	}
}

func TestControllerConfig_PartialOverride(t *testing.T) {
	data := `
vcenters:
  - server: 10.0.0.1
    secretRef: secret1
protection:
  tags:
    - eu-
safety:
  kubevols_min_age_days: 14
`
	var config ControllerConfig
	if err := yaml.Unmarshal([]byte(data), &config); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	config.applyDefaults()

	// Tags should be the override value, not defaults
	if len(config.Protection.Tags) != 1 || config.Protection.Tags[0] != "eu-" {
		t.Errorf("expected tags [eu-], got %v", config.Protection.Tags)
	}
	// Folders and ResourcePools should get defaults since they weren't set
	if len(config.Protection.Folders) != 2 || config.Protection.Folders[0] != "debug" {
		t.Errorf("expected default folders [debug, template], got %v", config.Protection.Folders)
	}
	if len(config.Protection.ResourcePools) != 2 || config.Protection.ResourcePools[0] != "ci-" {
		t.Errorf("expected default resourcepools [ci-, qeci-], got %v", config.Protection.ResourcePools)
	}
	// Safety should keep the override
	if config.Safety.KubevolsMinAgeDays != 14 {
		t.Errorf("expected kubevols_min_age_days 14, got %d", config.Safety.KubevolsMinAgeDays)
	}
}

func TestControllerConfig_ParsesAllSections(t *testing.T) {
	data := `
vcenters:
  - server: 10.0.0.1
    secretRef: secret1
cleanup:
  intervals:
    folders: 2
    tags: 4
    cnsvolumes: 6
    resourcepools: 3
    storagepolicies: 10
    kubevols: 48
safety:
  min_age_hours: 4
  grace_period_hours: 6
  kubevols_min_age_days: 30
features:
  dry_run: true
  enable_folders: true
  enable_tags: false
  enable_cnsvolumes: true
  enable_resourcepools: false
  enable_storagepolicies: true
  enable_kubevols: false
protection:
  tags:
    - eu-
  folders:
    - staging
  resourcepools:
    - dev-
logging:
  level: debug
  format: console
  enable_audit_log: true
`
	var config ControllerConfig
	if err := yaml.Unmarshal([]byte(data), &config); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Cleanup intervals
	if config.Cleanup.Intervals.Folders != 2 {
		t.Errorf("expected folders interval 2, got %d", config.Cleanup.Intervals.Folders)
	}
	if config.Cleanup.Intervals.Tags != 4 {
		t.Errorf("expected tags interval 4, got %d", config.Cleanup.Intervals.Tags)
	}
	if config.Cleanup.Intervals.CNSVolumes != 6 {
		t.Errorf("expected cnsvolumes interval 6, got %d", config.Cleanup.Intervals.CNSVolumes)
	}
	if config.Cleanup.Intervals.ResourcePools != 3 {
		t.Errorf("expected resourcepools interval 3, got %d", config.Cleanup.Intervals.ResourcePools)
	}
	if config.Cleanup.Intervals.StoragePolicies != 10 {
		t.Errorf("expected storagepolicies interval 10, got %d", config.Cleanup.Intervals.StoragePolicies)
	}
	if config.Cleanup.Intervals.Kubevols != 48 {
		t.Errorf("expected kubevols interval 48, got %d", config.Cleanup.Intervals.Kubevols)
	}

	// Safety
	if config.Safety.MinAgeHours != 4 {
		t.Errorf("expected min_age_hours 4, got %d", config.Safety.MinAgeHours)
	}
	if config.Safety.GracePeriodHours != 6 {
		t.Errorf("expected grace_period_hours 6, got %d", config.Safety.GracePeriodHours)
	}
	if config.Safety.KubevolsMinAgeDays != 30 {
		t.Errorf("expected kubevols_min_age_days 30, got %d", config.Safety.KubevolsMinAgeDays)
	}

	// Features
	if !config.Features.DryRun {
		t.Error("expected dry_run true")
	}
	if !config.Features.EnableFolders {
		t.Error("expected enable_folders true")
	}
	if config.Features.EnableTags {
		t.Error("expected enable_tags false")
	}
	if !config.Features.EnableCNSVolumes {
		t.Error("expected enable_cnsvolumes true")
	}
	if config.Features.EnableResourcePools {
		t.Error("expected enable_resourcepools false")
	}
	if !config.Features.EnableStoragePolicies {
		t.Error("expected enable_storagepolicies true")
	}
	if config.Features.EnableKubevols {
		t.Error("expected enable_kubevols false")
	}

	// Logging
	if config.Logging.Level != "debug" {
		t.Errorf("expected level debug, got %s", config.Logging.Level)
	}
	if config.Logging.Format != "console" {
		t.Errorf("expected format console, got %s", config.Logging.Format)
	}
	if !config.Logging.EnableAuditLog {
		t.Error("expected enable_audit_log true")
	}
}

func TestControllerConfig_ApplyDefaults_AllSections(t *testing.T) {
	config := ControllerConfig{}
	config.applyDefaults()

	// Safety defaults
	if config.Safety.MinAgeHours != 2 {
		t.Errorf("expected default min_age_hours 2, got %d", config.Safety.MinAgeHours)
	}
	if config.Safety.GracePeriodHours != 12 {
		t.Errorf("expected default grace_period_hours 12, got %d", config.Safety.GracePeriodHours)
	}
	if config.Safety.KubevolsMinAgeDays != 21 {
		t.Errorf("expected default kubevols_min_age_days 21, got %d", config.Safety.KubevolsMinAgeDays)
	}

	// Cleanup interval defaults
	if config.Cleanup.Intervals.Folders != 1 {
		t.Errorf("expected default folders interval 1, got %d", config.Cleanup.Intervals.Folders)
	}
	if config.Cleanup.Intervals.Tags != 8 {
		t.Errorf("expected default tags interval 8, got %d", config.Cleanup.Intervals.Tags)
	}
	if config.Cleanup.Intervals.CNSVolumes != 8 {
		t.Errorf("expected default cnsvolumes interval 8, got %d", config.Cleanup.Intervals.CNSVolumes)
	}
	if config.Cleanup.Intervals.ResourcePools != 2 {
		t.Errorf("expected default resourcepools interval 2, got %d", config.Cleanup.Intervals.ResourcePools)
	}
	if config.Cleanup.Intervals.StoragePolicies != 12 {
		t.Errorf("expected default storagepolicies interval 12, got %d", config.Cleanup.Intervals.StoragePolicies)
	}
	if config.Cleanup.Intervals.Kubevols != 24 {
		t.Errorf("expected default kubevols interval 24, got %d", config.Cleanup.Intervals.Kubevols)
	}

	// Features defaults: all enable flags should be true
	if !config.Features.EnableFolders {
		t.Error("expected default enable_folders true")
	}
	if !config.Features.EnableTags {
		t.Error("expected default enable_tags true")
	}
	if !config.Features.EnableCNSVolumes {
		t.Error("expected default enable_cnsvolumes true")
	}
	if !config.Features.EnableResourcePools {
		t.Error("expected default enable_resourcepools true")
	}
	if !config.Features.EnableStoragePolicies {
		t.Error("expected default enable_storagepolicies true")
	}
	if !config.Features.EnableKubevols {
		t.Error("expected default enable_kubevols true")
	}
	// dry_run defaults to false
	if config.Features.DryRun {
		t.Error("expected default dry_run false")
	}

	// Logging defaults
	if config.Logging.Level != "info" {
		t.Errorf("expected default level info, got %s", config.Logging.Level)
	}
	if config.Logging.Format != "json" {
		t.Errorf("expected default format json, got %s", config.Logging.Format)
	}
	if config.Logging.EnableAuditLog {
		t.Error("expected default enable_audit_log false")
	}
}

func TestControllerConfig_PartialOverride_NewSections(t *testing.T) {
	data := `
vcenters:
  - server: 10.0.0.1
    secretRef: secret1
cleanup:
  intervals:
    folders: 5
    tags: 10
safety:
  min_age_hours: 3
logging:
  level: warn
`
	var config ControllerConfig
	if err := yaml.Unmarshal([]byte(data), &config); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	config.applyDefaults()

	// Overridden values
	if config.Cleanup.Intervals.Folders != 5 {
		t.Errorf("expected folders interval 5, got %d", config.Cleanup.Intervals.Folders)
	}
	if config.Cleanup.Intervals.Tags != 10 {
		t.Errorf("expected tags interval 10, got %d", config.Cleanup.Intervals.Tags)
	}
	if config.Safety.MinAgeHours != 3 {
		t.Errorf("expected min_age_hours 3, got %d", config.Safety.MinAgeHours)
	}
	if config.Logging.Level != "warn" {
		t.Errorf("expected level warn, got %s", config.Logging.Level)
	}

	// Default values for non-overridden fields
	if config.Cleanup.Intervals.CNSVolumes != 8 {
		t.Errorf("expected default cnsvolumes interval 8, got %d", config.Cleanup.Intervals.CNSVolumes)
	}
	if config.Cleanup.Intervals.ResourcePools != 2 {
		t.Errorf("expected default resourcepools interval 2, got %d", config.Cleanup.Intervals.ResourcePools)
	}
	if config.Safety.GracePeriodHours != 12 {
		t.Errorf("expected default grace_period_hours 12, got %d", config.Safety.GracePeriodHours)
	}
	if config.Logging.Format != "json" {
		t.Errorf("expected default format json, got %s", config.Logging.Format)
	}
}

func TestControllerConfig_FeaturesDefaulting(t *testing.T) {
	// When features section is omitted entirely, all enable flags should default to true
	data := `
vcenters:
  - server: 10.0.0.1
    secretRef: secret1
`
	var config ControllerConfig
	if err := yaml.Unmarshal([]byte(data), &config); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	config.applyDefaults()

	if !config.Features.EnableFolders {
		t.Error("expected enable_folders true when features omitted")
	}
	if !config.Features.EnableTags {
		t.Error("expected enable_tags true when features omitted")
	}
	if !config.Features.EnableCNSVolumes {
		t.Error("expected enable_cnsvolumes true when features omitted")
	}
	if !config.Features.EnableResourcePools {
		t.Error("expected enable_resourcepools true when features omitted")
	}
	if !config.Features.EnableStoragePolicies {
		t.Error("expected enable_storagepolicies true when features omitted")
	}
	if !config.Features.EnableKubevols {
		t.Error("expected enable_kubevols true when features omitted")
	}
}

func TestControllerConfig_FeaturesExplicitDisable(t *testing.T) {
	// When at least one enable flag is explicitly set, defaults should NOT trigger
	data := `
vcenters:
  - server: 10.0.0.1
    secretRef: secret1
features:
  enable_folders: true
`
	var config ControllerConfig
	if err := yaml.Unmarshal([]byte(data), &config); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	config.applyDefaults()

	if !config.Features.EnableFolders {
		t.Error("expected enable_folders true (explicitly set)")
	}
	// All other enable flags should remain false since one was explicitly set
	if config.Features.EnableTags {
		t.Error("expected enable_tags false when one flag is explicit")
	}
	if config.Features.EnableCNSVolumes {
		t.Error("expected enable_cnsvolumes false when one flag is explicit")
	}
	if config.Features.EnableResourcePools {
		t.Error("expected enable_resourcepools false when one flag is explicit")
	}
	if config.Features.EnableStoragePolicies {
		t.Error("expected enable_storagepolicies false when one flag is explicit")
	}
	if config.Features.EnableKubevols {
		t.Error("expected enable_kubevols false when one flag is explicit")
	}
}
