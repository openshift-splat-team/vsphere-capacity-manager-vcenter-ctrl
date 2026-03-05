# Configuration Guide

This document describes the configuration options for the vSphere Capacity Manager vCenter Controller, including cleanup operations and safety settings.

## Overview

The controller performs two types of operations:
1. **Event-driven cleanup**: Triggered when a Lease is deleted (CI job completes)
2. **Scheduled cleanup**: Periodic sweeps to catch leaked resources

## ConfigMap Configuration

The controller reads its configuration from a Kubernetes ConfigMap. See
`config/samples/cleanup-config.yaml` for a complete template.

Configuration is parsed by `pkg/utils/config.go` (`ReadControllerConfig`) and loaded
once at startup. Changes to the ConfigMap require a pod restart to take effect.

### Configurable Settings

The following settings are configured via the ConfigMap with sensible defaults:

#### Cleanup Intervals

| Operation | Current Interval | Configurable Key |
|-----------|-----------------|------------------|
| Folders | 1 hour | `cleanup.intervals.folders` |
| Tags | 8 hours | `cleanup.intervals.tags` |
| CNS Volumes | 8 hours | `cleanup.intervals.cnsvolumes` |
| Resource Pools | 2 hours | `cleanup.intervals.resourcepools` |
| Storage Policies | 12 hours | `cleanup.intervals.storagepolicies` |
| Kubevols | 24 hours | `cleanup.intervals.kubevols` |

**Location in code:**
- `internal/controller/vsphere_object_controller.go` - `pruneFunctions` array (lines 53-89)

#### Safety Thresholds

| Setting | Current Value | Configurable Key |
|---------|--------------|------------------|
| Kubevols minimum age | 21 days | `safety.kubevols_min_age_days` |
| General minimum age | N/A (future) | `safety.min_age_hours` |
| Grace period | N/A (future) | `safety.grace_period_hours` |

**Location in code:**
- `internal/controller/vsphere_object_controller.go` - `kubevols()` function (line 487)

#### Protected Resources

All protection prefixes/names are configurable via the `protection` section of the
ConfigMap. The controller uses a two-tier model for cleanup decisions:

1. **Target patterns** (hardcoded): identify resources that are candidates for deletion
2. **Protection lists** (from config): exempt specific resources from deletion, even if
   they match a target pattern

| Resource Type | Target Logic | Protection Config | Default Protection |
|--------------|--------------|-------------------|--------------------|
| Tags | Contains `ci-` | `protection.tags` (prefix match) | `["us-"]` |
| Folders | Contains `ci-`, `user-`, `build-` | `protection.folders` (exact match) | `["debug", "template"]` |
| Resource pools | Starts with `ci-`, `qeci-` | `protection.resourcepools` (prefix match) | none |
| RHCOS VMs | N/A | Hardcoded `rhcos-` prefix skip | N/A |

**Example:** A resource pool named `ci-special-pool` starts with `ci-` so it is a
cleanup target. But if `protection.resourcepools` contains `ci-special-`, the pool
will be protected and skipped.

**Location in code:**
- Target patterns: `internal/controller/protection.go` (`DefaultFolderTargetPatterns`, `DefaultResourcePoolTargetPrefixes`)
- Protection helpers: `internal/controller/protection.go` (`IsProtectedTag`, `IsProtectedFolder`, `IsProtectedResourcePool`)
- Scheduled cleanup: `internal/controller/vsphere_object_controller.go`
- Lease-driven cleanup: `internal/controller/lease_controller.go` (`deleteByManagedEntity`)

## Cleanup Operations

### 1. Folder Cleanup (`folder()`)
**Schedule:** Every 1 hour
**Criteria:**
- Folder name contains `ci-`, `user-`, or `build-`
- Folder has zero children

**Safety:**
- Only deletes empty folders
- Continues on individual failures

**Code:** `vsphere_object_controller.go:175-215`

### 2. Tag Cleanup (`tag()`)
**Schedule:** Every 8 hours
**Criteria:**
- Tag name contains `ci-`
- Tag is NOT a zonal tag (`us-*`)
- Tag has zero attachments
- No matching folder exists

**Safety:**
- Only deletes tags with 0 attachments (fixed from ≤1)
- Protects zonal infrastructure tags
- Only deletes categories when completely empty

**Code:** `vsphere_object_controller.go:226-313`

### 3. CNS Volume Cleanup (`cns()`)
**Schedule:** Every 8 hours
**Criteria:**
- Volume cluster ID starts with `ci-`
- No matching folder exists for cluster ID

**Safety:**
- Verifies folder doesn't exist before deletion
- Attempts force delete first, falls back to non-force

**Code:** `vsphere_object_controller.go:87-144`

### 4. Resource Pool Cleanup (`resourcepool()`)
**Schedule:** Every 2 hours
**Criteria:**
- Resource pool name starts with a target prefix (`ci-` or `qeci-`, hardcoded in `DefaultResourcePoolTargetPrefixes`)
- Resource pool is NOT in the `protection.resourcepools` list
- Resource pool has zero VMs
- Resource pool has been seen as empty for at least `min_age_hours` (default: 2 hours)

**Safety:**
- Two-tier targeting: must match a target prefix AND must not match a protection prefix
- Only deletes empty pools
- Uses property collector to verify VM count
- Minimum age check prevents deleting pools that were only briefly empty
- Dry-run mode support via `features.dry_run`

**Lease-driven cleanup:**
When a Lease is deleted, the `LeaseReconciler` searches for all managed entities matching
the cluster ID. Resource pools found this way are checked against `protection.resourcepools`
before deletion. Protected pools are skipped with a log message.

**Code:** `vsphere_object_controller.go` (`resourcepool` function), `lease_controller.go` (`deleteByManagedEntity`)

### 5. Storage Policy Cleanup (`storagepolicy()`)
**Schedule:** Every 12 hours
**Criteria:**
- Policy name matches `openshift-storage-policy-{clusterId}`
- No matching folder exists for cluster ID

**Safety:**
- Extracts cluster ID from policy name
- Verifies cluster folder doesn't exist
- Uses PBM API for safe deletion

**Code:** `vsphere_object_controller.go:395-476`

### 6. Kubevols Cleanup (`kubevols()`)
**Schedule:** Every 24 hours
**Criteria:**
- File in `/kubevols` directory on any datastore
- File is a `.vmdk` file
- File is 21+ days old (based on modification time)

**Safety:**
- 21-day minimum age threshold
- Only targets .vmdk files
- Gracefully handles missing directories
- Skips files without modification timestamps

**Code:** `vsphere_object_controller.go:484-608`

### 7. Orphan VM Detection (`deleteVirtualMachinesByPortGroup()`)
**Trigger:** Event-driven (on Lease deletion)
**Criteria:**
- VM created before lease creation time, OR
- VM has 0GB disk AND is powered off

**Safety:**
- Only deletes VMs on lease port groups
- Time-based filtering (created before lease)
- Excludes VMs with cluster ID in name/folder
- Excludes RHCOS template VMs
- Power state verification

**Code:** `lease_controller.go:241-335`

## Safety Features

### Current Implementation

1. **Name Pattern Filtering**
   - All operations filter by specific patterns (`ci-*`, `qeci-*`, `openshift-storage-policy-*`)
   - Prevents accidental deletion of system resources

2. **State Verification**
   - Checks for empty folders, zero VMs in pools, zero attachments on tags
   - Verifies cluster doesn't exist before resource deletion

3. **Error Handling**
   - All operations continue on individual failures
   - Comprehensive error logging for troubleshooting
   - No cascading failures

4. **Audit Logging**
   - Structured JSON logging for all operations
   - Deletion reasons logged for audit trail
   - Success and failure states clearly indicated

### Implemented Safety Features

1. **Dry-Run Mode** (`features.dry_run: true`)
   - Logs what would be deleted with `[DRY RUN]` prefix without actually deleting
   - Available for all scheduled cleanup operations
   - Enabled via the `features` section of the ConfigMap

2. **Minimum Age Requirements** (`safety.min_age_hours`)
   - Configurable minimum age (default: 2 hours) for scheduled cleanup targets
   - Resources must be seen as deletion candidates for at least this long before
     they are actually deleted
   - Kubevols have a separate `safety.kubevols_min_age_days` threshold (default: 21 days)

3. **Grace Periods** (`safety.grace_period_hours`)
   - Configurable grace period (default: 12 hours) after cluster deletion

4. **Configurable Protection Lists**
   - Tags, folders, and resource pools can be protected via the `protection` section
   - See "Protected Resources" above for details

### Future Enhancements (Not Yet Implemented)

1. **Circuit Breaker**
   - Maximum deletions per run (e.g., 100)
   - Maximum deletion rate (e.g., 25% of resources)
   - Prevents mass deletions from bugs

2. **Multi-Stage Deletion**
   - Mark resources for deletion
   - Confirm after waiting period
   - Execute deletion
   - Provides time for manual intervention

## Deployment Configuration

### Environment Variables

The controller uses environment variables for vCenter credentials:

```yaml
env:
- name: VCENTER_SERVER
  value: "vcenter.example.com"
# Credentials are typically loaded from Kubernetes secrets
```

### Command-Line Flags

```bash
--secret-namespace string      Namespace containing vCenter secrets
--secrets string               Comma-delimited secret names
--secret-data-keys string      Secret data keys
--secret-vcenters string       vCenter server names
--leader-elect                 Enable leader election (default: false)
--metrics-bind-address string  Metrics endpoint (default: ":8080")
--health-probe-bind-address string  Health endpoint (default: ":8081")
```

## Monitoring and Metrics

### Health Endpoints

- **Liveness:** `/healthz` - Checks if controller is running
- **Readiness:** `/readyz` - Checks if controller is ready to serve requests
- **Metrics:** `:8080/metrics` - Prometheus metrics endpoint

### Available Metrics

Currently, the controller exposes standard controller-runtime metrics. Future enhancements could include:

- `vsphere_resources_deleted_total{type, vcenter}` - Counter of deleted resources
- `vsphere_resources_pending_deletion{type}` - Gauge of resources marked for deletion
- `vsphere_cleanup_duration_seconds{operation}` - Histogram of cleanup durations
- `vsphere_cleanup_errors_total{operation}` - Counter of cleanup errors

## Configuration Reference

### Protection Section

```yaml
protection:
  # Tag name prefixes to protect from deletion (prefix match).
  # Tags whose names start with any of these prefixes will be skipped.
  # Default: ["us-"]
  tags:
    - us-east
    - us-west

  # Folder names to protect from deletion (exact match).
  # Folders whose names exactly match any of these will be skipped.
  # Default: ["debug", "template"]
  folders:
    - debug
    - template
    - management

  # Resource pool name prefixes to protect from deletion (prefix match).
  # Resource pools whose names start with any of these prefixes will be
  # skipped, even if they match the built-in target prefixes (ci-, qeci-).
  # Default: [] (no pools protected by default)
  resourcepools:
    - ipi-ci-clusters
    - management-
```

### Features Section

```yaml
features:
  # When true, log what would be deleted without actually deleting.
  dry_run: false

  # Enable/disable individual cleanup operations.
  # If all flags are omitted, all operations are enabled by default.
  enable_folders: true
  enable_tags: true
  enable_cnsvolumes: true
  enable_resourcepools: true
  enable_storagepolicies: true
  enable_kubevols: true
```

### Safety Section

```yaml
safety:
  # Minimum hours a resource must be seen as a deletion candidate before
  # it is actually deleted. Default: 2
  min_age_hours: 2

  # Grace period in hours after cluster deletion. Default: 12
  grace_period_hours: 12

  # Minimum age in days for kubevol VMDK files before deletion. Default: 21
  kubevols_min_age_days: 21
```

## References

- Main controller: `internal/controller/vsphere_object_controller.go`
- Lease reconciler: `internal/controller/lease_controller.go`
- Protection helpers: `internal/controller/protection.go`
- Config parsing: `pkg/utils/config.go`
- Sample config: `config/samples/cleanup-config.yaml`
- Changelog: `CHANGELOG.md`
- Implementation plan: `IMPLEMENTATION_PLAN.md`
- Deployment: `deploy.yaml`
