# Implementation Plan: Extend Controller for CI Resource Cleanup

## Overview
This document outlines the plan to extend the existing `vsphere-capacity-manager-vcenter-ctrl` controller with cleanup operations from PowerShell scripts, while keeping monitoring scripts separate. This approach provides **lease-based safety** (won't delete active CI job resources) and consolidates cleanup into one Kubernetes-native service.

## Background

The original PowerShell scripts in `openshift-serverless-vsphere-notifications` repository include:
1. **Cleanup Scripts** (11 total): orphanvms, cnsvols, kubevols, rp, foldertag, spbm, disable_enable_ha
2. **Monitoring Scripts** (4 total): vm-drs-monitoring, vcenteralarms, esxialerts, lockout

**Problem with Current Approach:**
- PowerShell scripts run on periodic timers (CronJobs)
- No awareness of CI job state (active vs completed)
- Risk of deleting resources from active CI jobs
- Multiple scripts = multiple deployments and maintenance overhead

**Solution:**
- Extend the existing controller which **knows when leases are deleted** (CI jobs complete)
- Event-driven cleanup on lease deletion + periodic cleanup for orphans
- Single Go codebase with comprehensive testing

## Architecture Decision

### Hybrid Approach

**vsphere-capacity-manager-vcenter-ctrl (Extended):**
- `LeaseReconciler`: Event-driven cleanup (VMs, folders, tags on lease delete)
- `VSphereObjectReconciler`: Periodic cleanup (orphans, resource pools, storage policies, kubevols, CNS volumes, empty folders, unused tags)

**Separate Monitoring (Keep PowerShell or convert later):**
- DRS monitoring
- Alarm monitoring
- ESXi health
- Account lockout

**Manual Operations (PowerShell scripts archived):**
- Emergency cleanup
- One-off operations
- Troubleshooting

## Cleanup Architecture - Dual Path Strategy

The controller implements a **dual-path cleanup strategy** for maximum safety and completeness:

### Event-Driven Path (LeaseReconciler)
**File:** `internal/controller/lease_controller.go`

**Trigger:** When a lease is deleted (CI job completes)

**Immediately cleans up:**
- CNS volumes
- Storage Policies
- Folders
- Tags and tag categories
- Kubevols
- VMs by port group

**Advantages:**
- Fast cleanup right after CI job completes
- Reduces resource waste
- Tied to lease lifecycle (safe by design)

### Scheduled Path (VSphereObjectReconciler)
**File:** `internal/controller/vsphere_object_controller.go`

**Trigger:** Runs on periodic schedule (every 1-24 hours depending on operation)

**Acts as safety net to:**
- Confirm deletion of leaked objects not caught by lease deletion
- Apply additional safety checks before final cleanup
- Handle orphaned resources from failed lease deletions
- Catch resources created outside the lease lifecycle
- Clean up resources that have been orphaned for extended periods

**Advantages:**
- Multi-stage confirmation with safety checks
- Catches edge cases and failures
- Independent of lease events
- Can apply time-based safety (minimum age requirements)

### Why Both Paths?

**Immediate cleanup on lease deletion** ensures fast resource reclamation, but might miss:
- Resources created outside lease management
- Partial deletions due to transient failures
- Objects not properly tagged/associated with leases

**Scheduled confirmation cleanup** acts as a **safety net** that:
- Catches everything the event-driven path missed
- Applies stricter safety checks (minimum age, empty duration, etc.)
- Prevents accumulation of leaked resources over time

Together, these provide **defense in depth** for resource cleanup.

## Implementation Phases

### Phase 1: Setup & Baseline ✅ COMPLETED
**Duration:** Day 1

**In openshift-serverless-vsphere-notifications repo:**
- ✅ Reverted uncommitted changes on main branch
- ✅ Verified PowerShell scripts work
- ✅ Archived for reference

**In vsphere-capacity-manager-vcenter-ctrl repo:**
- ✅ Created feature branch `add-cleanup-operations`
- ✅ Verified local build works: `make build`
- ✅ Verified tests pass: `make test`

### Phase 2: Add Resource Pool Cleanup
**Duration:** Week 1
**Status:** IN PROGRESS

**File:** `internal/controller/vsphere_object_controller.go`

**Tasks:**
1. Add to `pruneFunctions` array:
   ```go
   {
       name:    "resourcepools",
       execute: v.resourcepool,
       delay:   time.Hour * 2,
       lastRun: time.Now(),
   }
   ```

2. Implement `resourcepool()` function:
   - Find resource pools matching `^ci*` or `^qeci*`
   - Check if empty (VMCount == 0)
   - Add safety: minimum age 4 hours, must be empty for 1 hour
   - Delete empty pools
   - Use govmomi: `object.NewResourcePool()`, `ResourcePool.Destroy()`

3. Write unit tests:
   - Test empty pool detection
   - Test name pattern matching
   - Test safety timing checks
   - Mock ResourcePool objects

4. Test locally against test vCenter

**PowerShell Reference:** `scripts/rp.ps1`
- Pattern: `^ci*|^qeci*`
- Condition: VM count == 0
- Current safety: None (immediate deletion)
- Enhanced safety: Add minimum age + stable empty duration

### Phase 3: Enhance Tag Cleanup
**Duration:** Week 1-2

**File:** `internal/controller/vsphere_object_controller.go`

**Tasks:**
1. Uncomment `tag()` function (line 222)

2. Fix critical bug in PowerShell equivalent:
   - Change from `≤1 assignments` to `== 0 assignments`
   - Only delete truly unused tags

3. Add attachment counting:
   - Use `TagManager.GetAttachedObjectsOnTags()`
   - Delete tags with zero attachments
   - Delete empty tag categories
   - Protect zonal tags (us-*)

4. Write tests for tag attachment logic

**PowerShell Reference:** `scripts/foldertag.ps1`
- Current code deletes tags with ≤1 attachment (BUG!)
- Should only delete with 0 attachments

### Phase 4: Enhance Orphan VM Detection
**Duration:** Week 2

**File:** `internal/controller/lease_controller.go`

**Tasks:**
1. Enhance `deleteVirtualMachinesByPortGroup` (~line 275):
   - Add disk capacity inspection
   - Check for 0GB disks (orphan indicator)
   - Use `vm.Config.Hardware.Device` inspection
   - Log disk anomalies before deletion

2. Add safety checks:
   - Keep existing time-based filtering (VM created before lease)
   - Keep RHCOS protection
   - Add minimum age requirement (2 hours)

3. Write tests for disk inspection logic

**PowerShell Reference:** `scripts/orphanvms.ps1`
- Pattern: `^ci-*`
- Conditions: PoweredOff + 0GB disk capacity
- Current controller: Only checks power state, not disk capacity

### Phase 5: Add Storage Policy Cleanup
**Duration:** Week 2-3

**File:** `internal/controller/vsphere_object_controller.go`

**Tasks:**
1. Add to `pruneFunctions` array:
   ```go
   {
       name:    "storagepolicies",
       execute: v.storagepolicy,
       delay:   time.Hour * 12,
       lastRun: time.Now(),
   }
   ```

2. Implement `storagepolicy()` function:
   - Find policies matching `openshift-storage-policy-{clusterId}*`
   - Check if cluster folder exists
   - Add 24-hour minimum age
   - Delete if cluster gone for 12+ hours
   - Use govmomi: `pbm.Client`, `QueryProfile()`, `DeleteProfile()`

3. Write tests with mock PBM client

**PowerShell Reference:** `scripts/spbm.ps1`
- Pattern: `openshift-storage-policy-{clusterId}`
- Condition: Cluster folder doesn't exist

### Phase 6: Add Kubevols Cleanup
**Duration:** Week 3

**File:** `internal/controller/vsphere_object_controller.go`

**Tasks:**
1. Add to `pruneFunctions` array:
   ```go
   {
       name:    "kubevols",
       execute: v.kubevols,
       delay:   time.Hour * 24,
       lastRun: time.Now(),
   }
   ```

2. Implement `kubevols()` function:
   - Search `/kubevols` directory on datastores
   - Find `.vmdk` files older than 21 days
   - Check if files are in use (attached to VMs)
   - Delete old, unattached files
   - Use govmomi: `DatastoreBrowser.SearchDatastoreSubFolders()`

3. Write tests with mock datastore browser

**PowerShell Reference:** `scripts/kubevols.ps1`
- Location: `/kubevols` on datastore
- File type: `.vmdk`
- Age threshold: 21 days

### Phase 7: Add Configuration & Safety
**Duration:** Week 4

**Tasks:**
1. Create ConfigMap for operational settings:
   ```yaml
   cleanup:
     intervals:
       resource_pools_hours: 2
       storage_policies_hours: 12
       kubevols_hours: 24
     safety:
       min_age_hours: 2
       grace_period_hours: 12
     features:
       dry_run: false
       slack_notifications: true
   ```

2. Add safety enhancements:
   - Circuit breaker (max deletions per run)
   - Dry-run mode for testing
   - Comprehensive audit logging
   - Multi-stage deletion (mark → confirm → delete)

3. Add metrics:
   - Counter: `resources_deleted_total{type, vcenter}`
   - Gauge: `resources_pending_deletion{type}`
   - Histogram: `cleanup_duration_seconds{operation}`

### Phase 8: Monitoring Scripts Decision
**Duration:** Week 4

**Keep separate** from controller:
- vm-drs-monitoring.ps1
- vcenteralarms.ps1
- esxialerts.ps1
- lockout.ps1

**Options:**
1. Keep as PowerShell CronJobs (minimal work)
2. Convert to Prometheus exporters (better observability)
3. Add as separate metrics controller in same repo

**Recommendation:** Keep as PowerShell for now, convert later if needed

### Phase 9: Testing & Validation
**Duration:** Week 5

**Tasks:**
1. Unit test coverage: Aim for 80%+
2. Integration tests with vcsim
3. Deploy to test cluster:
   - Run alongside PowerShell scripts
   - Compare outputs for 1 week
   - Validate no active CI resources deleted
4. Performance testing:
   - Monitor memory usage
   - Track API call rates
   - Measure cleanup durations

### Phase 10: Production Rollout
**Duration:** Week 6

**Tasks:**
1. Deploy to production with dry-run mode enabled
2. Monitor logs for 2-3 days
3. Enable deletions (disable dry-run)
4. Monitor metrics closely for 1 week
5. Gradually deprecate PowerShell CronJobs:
   - Disable one script at a time
   - Wait 24 hours between each
   - Keep scripts available for emergencies

## Safety Guarantees

✅ **Lease-based protection**: Only cleans resources AFTER CI job completes
✅ **Time-based safety**: Minimum age requirements (2-24 hours)
✅ **State verification**: Checks cluster existence before deletion
✅ **Name filtering**: Only targets CI resources (ci-*, qeci-*)
✅ **Empty checks**: Resource pools, folders only deleted when empty
✅ **Audit logging**: Every deletion logged with reason
✅ **Circuit breaker**: Prevents mass deletions
✅ **Dry-run mode**: Test before enabling deletions

## Code Mapping: PowerShell → Go

| PowerShell Script | Controller Function | Reconciler | Deletion Path | Frequency | Status |
|-------------------|---------------------|------------|---------------|-----------|--------|
| orphanvms.ps1 | Enhance `deleteVirtualMachinesByPortGroup` | LeaseReconciler | Event-driven (on lease delete) | Immediate | Planned |
| foldertag.ps1 (folders) | `folder()` | VSphereObjectReconciler | Both (event + scheduled) | Event + 1 hour | ✅ Exists |
| foldertag.ps1 (tags) | `tag()` | VSphereObjectReconciler | Both (event + scheduled) | Event + 8 hours | Commented out |
| cnsvols.ps1 | `cns()` | VSphereObjectReconciler | Both (event + scheduled) | Event + 8 hours | ✅ Exists |
| rp.ps1 | `resourcepool()` | VSphereObjectReconciler | Both (event + scheduled) | Event + 2 hours | 🔨 In Progress |
| spbm.ps1 | `storagepolicy()` | VSphereObjectReconciler | Both (event + scheduled) | Event + 12 hours | Planned |
| kubevols.ps1 | `kubevols()` | VSphereObjectReconciler | Both (event + scheduled) | Event + 24 hours | Planned |
| vm-drs-monitoring.ps1 | N/A - Keep separate | Monitoring | N/A | 5 min | Keep PowerShell |
| vcenteralarms.ps1 | N/A - Keep separate | Monitoring | N/A | 5 min | Keep PowerShell |
| esxialerts.ps1 | N/A - Keep separate | Monitoring | N/A | 5 min | Keep PowerShell |
| lockout.ps1 | N/A - Keep separate | Monitoring | N/A | 5 min | Keep PowerShell |

## Timeline

- **Week 1:** Resource pools + tag cleanup
- **Week 2:** Orphan VMs + storage policies
- **Week 3:** Kubevols cleanup
- **Week 4:** Configuration + safety + metrics
- **Week 5:** Testing + validation
- **Week 6:** Production rollout

**Total:** 6 weeks (vs 8-12 weeks for standalone Go rewrite)

## Success Criteria

✅ All 6 cleanup operations integrated into controller
✅ No active CI job resources deleted (verified over 1 week)
✅ 80%+ test coverage
✅ Cleanup runs every 1-24 hours (per operation)
✅ PowerShell scripts deprecated but archived
✅ Comprehensive monitoring and alerting

## Rollback Plan

If issues arise:
1. Revert to PowerShell CronJobs immediately
2. Keep controller running in dry-run mode for observation
3. Fix issues in feature branch
4. Re-deploy after validation

PowerShell scripts are **not deleted**, only deprecated, so rollback is instant.

## Repository Structure After Implementation

```
vsphere-capacity-manager-vcenter-ctrl/
├── internal/controller/
│   ├── lease_controller.go          # Event-driven cleanup on lease delete
│   ├── vsphere_object_controller.go # Periodic cleanup operations
│   │   ├── folder()                 # ✅ Existing
│   │   ├── tag()                    # Enhanced
│   │   ├── cns()                    # ✅ Existing
│   │   ├── resourcepool()           # 🆕 NEW
│   │   ├── storagepolicy()          # 🆕 NEW
│   │   └── kubevols()               # 🆕 NEW
│   └── *_test.go                    # Comprehensive tests
├── config/
│   └── configmap.yaml               # 🆕 Operational configuration
└── IMPLEMENTATION_PLAN.md           # This document
```

## References

- Original PowerShell repository: `openshift-serverless-vsphere-notifications`
- Controller repository: `vsphere-capacity-manager-vcenter-ctrl`
- govmomi documentation: https://github.com/vmware/govmomi
- Feature branch: `add-cleanup-operations`

## Notes

- The controller **already has** `LeaseReconciler` which knows when CI jobs complete (lease deletion)
- This is the **key safety feature** that PowerShell scripts lack
- The controller already implements folder and CNS volume cleanup
- Tag cleanup exists but is commented out (needs enhancement)
- Comments in code (line 217-219) mention "rp" and "storage policies" were already planned
