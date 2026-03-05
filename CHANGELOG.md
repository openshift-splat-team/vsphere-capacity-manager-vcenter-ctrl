# Changelog

All notable changes to the vSphere Capacity Manager vCenter Controller will be
documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Fixed

- **CRITICAL: Resource pool protection semantics inversion in VSphereObjectReconciler.**
  The `protection.resourcepools` config field was being used as the **target list**
  (pools to delete) instead of a **protection list** (pools to protect from deletion).
  This was the opposite semantic from `protection.tags` and `protection.folders`, which
  correctly protect matching objects. Resource pool target prefixes (`ci-`, `qeci-`) are
  now hardcoded in `DefaultResourcePoolTargetPrefixes` (matching the `DefaultFolderTargetPatterns`
  pattern), and `protection.resourcepools` is now correctly used to **protect** pools
  from deletion via the new `IsProtectedResourcePool` helper function.
  (`internal/controller/protection.go`, `internal/controller/vsphere_object_controller.go`)

- **CRITICAL: LeaseReconciler had no resource pool protection.**
  When a Lease was deleted, the `LeaseReconciler` would search for all `ManagedEntity`
  objects matching the cluster ID via wildcard (`getManagedEntitiesByClusterId`). Any
  `ResourcePool` matching the pattern fell through to the `default` case in
  `deleteByManagedEntity` and was destroyed with no protection check, no empty-pool
  check, no dry-run check, and no minimum age check. This caused the `ipi-ci-clusters`
  resource pool to be deleted despite being listed in the protection config.
  The fix adds an explicit `"ResourcePool"` case in `deleteByManagedEntity` that checks
  `IsProtectedResourcePool` before deletion, and wires the `Protection` config into
  the `LeaseReconciler` struct.
  (`internal/controller/lease_controller.go`, `cmd/main.go`)

### Changed

- `protection.resourcepools` config field now has **protection semantics** (matching
  tags and folders). Values are prefixes of resource pool names that should be
  **protected from deletion**, not targeted for deletion. **This is a breaking change
  for anyone who was relying on the old (incorrect) behavior of using this field as a
  target list.**

- Default value for `protection.resourcepools` is now empty (no pools protected by
  default). Previously defaulted to `["ci-", "qeci-"]` which were being used as
  deletion targets. Target prefixes are now hardcoded in
  `DefaultResourcePoolTargetPrefixes`.

### Added

- `DefaultResourcePoolTargetPrefixes` variable in `internal/controller/protection.go` --
  hardcoded `["ci-", "qeci-"]` target prefixes for resource pool cleanup, following the
  same pattern as `DefaultFolderTargetPatterns`.

- `IsProtectedResourcePool` function in `internal/controller/protection.go` -- checks
  whether a resource pool name starts with any protected prefix. Used by both
  `VSphereObjectReconciler` and `LeaseReconciler`.

- `Protection` field (`utils.ProtectionConfig`) on `LeaseReconciler` struct, wired
  from `controllerConfig.Protection` in `cmd/main.go`.

- Explicit `"ResourcePool"` case in `LeaseReconciler.deleteByManagedEntity` with
  protection check before deletion.

- Tests for `IsProtectedResourcePool` in `internal/controller/protection_test.go`,
  including interaction tests verifying that a pool can match a target prefix while
  being protected.

- Test for `DefaultResourcePoolTargetPrefixes` verifying that `ipi-ci-clusters` is
  not a target (does not start with `ci-` or `qeci-`).

### Migration Guide

If you have an existing config with `protection.resourcepools`, update it to list
pools you want to **protect** (not pools you want to target for deletion):

```yaml
# BEFORE (old broken semantics -- these were deletion targets, not protection):
protection:
  resourcepools:
    - ci-
    - qeci-

# AFTER (correct semantics -- these are protected from deletion):
protection:
  resourcepools:
    - ipi-ci-clusters
    - management-pool
```

Target prefixes (`ci-`, `qeci-`) are now built into the controller as
`DefaultResourcePoolTargetPrefixes` and do not need to be configured. Any resource
pool whose name starts with `ci-` or `qeci-` will be a candidate for cleanup
(if empty and old enough in the scheduled path, or if matched by cluster ID in the
lease path) **unless** it is explicitly protected via `protection.resourcepools`.
