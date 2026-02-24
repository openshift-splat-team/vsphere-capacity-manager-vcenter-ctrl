# Configuration Guide

This document describes the configuration options for the vSphere Capacity Manager vCenter Controller, including cleanup operations and safety settings.

## Overview

The controller performs two types of operations:
1. **Event-driven cleanup**: Triggered when a Lease is deleted (CI job completes)
2. **Scheduled cleanup**: Periodic sweeps to catch leaked resources

## ConfigMap Configuration

A ConfigMap can be used to configure operational parameters (future implementation). See `config/samples/cleanup-config.yaml` for the template.

### Current Hard-Coded Settings

The following settings are currently hard-coded in the controller but can be made configurable in future versions:

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

| Resource Type | Protection Logic | Location |
|--------------|------------------|----------|
| Zonal tags | Tags starting with `us-` | `vsphere_object_controller.go:252` |
| System folders | "debug" folder | `vsphere_object_controller.go` (via getFolderList) |
| RHCOS VMs | VMs starting with `rhcos-` | `lease_controller.go:233` |

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
- Resource pool name starts with `ci-` or `qeci-`
- Resource pool has zero VMs

**Safety:**
- Only deletes empty pools
- Uses property collector to verify VM count

**Code:** `vsphere_object_controller.go:315-393`

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

### Future Enhancements (Not Yet Implemented)

The following safety features are planned but not yet implemented:

1. **Circuit Breaker**
   - Maximum deletions per run (e.g., 100)
   - Maximum deletion rate (e.g., 25% of resources)
   - Prevents mass deletions from bugs

2. **Multi-Stage Deletion**
   - Mark resources for deletion
   - Confirm after waiting period
   - Execute deletion
   - Provides time for manual intervention

3. **Dry-Run Mode**
   - Log what would be deleted without actually deleting
   - Test cleanup logic safely
   - Validate before production deployment

4. **Minimum Age Requirements**
   - Configurable minimum age for all resources
   - Additional buffer beyond lease lifecycle
   - Currently only implemented for kubevols (21 days)

5. **Grace Periods**
   - Time window after cluster deletion before resource cleanup
   - Allows for manual recovery if needed
   - Currently not implemented

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

## Future Configuration Implementation

To implement ConfigMap-based configuration:

1. **Add ConfigMap mounting** in deployment:
```yaml
volumeMounts:
- name: config
  mountPath: /etc/vsphere-cleanup
volumes:
- name: config
  configMap:
    name: vsphere-cleanup-config
```

2. **Update controller code** to read config:
```go
// Add to main.go or controller setup
import "github.com/spf13/viper"

viper.SetConfigName("cleanup-config")
viper.AddConfigPath("/etc/vsphere-cleanup")
viper.AutomaticEnv()

// Read intervals
folderInterval := viper.GetDuration("cleanup.intervals.folders")
```

3. **Apply configuration** to pruneFunctions:
```go
pf := []pruneFunctions{
    {
        name:    "folders",
        execute: v.folder,
        delay:   config.GetDuration("cleanup.intervals.folders"),
        lastRun: time.Now(),
    },
    // ...
}
```

## References

- Main controller: `internal/controller/vsphere_object_controller.go`
- Lease reconciler: `internal/controller/lease_controller.go`
- Implementation plan: `IMPLEMENTATION_PLAN.md`
- Deployment: `deploy.yaml`
