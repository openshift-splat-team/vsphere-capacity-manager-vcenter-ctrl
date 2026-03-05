# AGENTS.md — Coding Agent Guidelines

## Project Overview

Go 1.23 Kubernetes controller (Kubebuilder v4 / Operator SDK) that cleans up vSphere
resources (VMs, folders, tags, CNS volumes, resource pools, storage policies) when
`vsphere-capacity-manager` Lease CRDs are deleted. Uses controller-runtime v0.18.5
and govmomi v0.52.0.

Module: `github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl`

## Build / Lint / Test Commands

```bash
# Build
make build                # builds bin/manager (runs manifests, generate, fmt, vet first)
go build ./...            # quick compile check without codegen

# Format and vet
make fmt                  # go fmt ./...
make vet                  # go vet ./...

# Lint
make lint                 # golangci-lint run (v1.54.2, config in .golangci.yml)
make lint-fix             # golangci-lint run --fix

# Unit/integration tests (uses envtest with etcd + kube-apiserver)
make test                 # all tests except e2e, outputs cover.out

# Run a single test by name (regex match)
go test ./internal/controller/ -run "TestName" -v
go test ./pkg/utils/ -run "TestControllerConfig_ApplyDefaults" -v

# Run a single Ginkgo spec by description
go test ./internal/controller/ -v -ginkgo.focus="description text"

# Run tests in a specific file's package
go test ./internal/controller/ -v
go test ./pkg/utils/ -v

# E2E tests (requires Kind cluster)
make test-e2e

# Code generation
make manifests            # generate RBAC, CRD manifests via controller-gen
make generate             # generate DeepCopy methods

# Docker
make docker-build IMG=<image>
make docker-push IMG=<image>
```

## Project Structure

```
cmd/main.go                              # Entrypoint, manager setup
internal/controller/
  lease_controller.go                    # LeaseReconciler: event-driven cleanup on lease deletion
  vsphere_object_controller.go           # VSphereObjectReconciler: scheduled cleanup (safety net)
  protection.go                          # Pure helper functions for pattern matching / protection
  protection_test.go                     # Ginkgo tests for protection helpers
  lease_controller_test.go               # Ginkgo + vcsim integration tests
  suite_test.go                          # Ginkgo/envtest suite bootstrap
pkg/utils/
  config.go                              # ControllerConfig: ConfigMap parsing with defaults
  config_test.go                         # Standard Go tests for config
  auth.go                                # K8s Secret reading for vCenter credentials
pkg/vsphere/
  session.go                             # vSphere session management (mutex-protected pooling)
config/                                  # Kustomize manifests (RBAC, deployment, samples)
vendor/                                  # Vendored dependencies (go mod vendor)
```

## Code Style

### Imports

Three groups separated by blank lines: (1) stdlib, (2) external, (3) internal.
Enforced by `goimports` linter.

```go
import (
    "context"
    "fmt"
    "time"

    "github.com/go-logr/logr"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/client"

    "github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/vsphere"
)
```

Common aliases: `ctrl` for controller-runtime, `v1` for the Lease API types,
`corev1`, `k8stypes`, `cnstypes`. Dot imports only in test files for Ginkgo/Gomega.

### Naming

- **Structs:** PascalCase, domain-descriptive. Config types suffixed with `Config`.
- **Functions:** Verb-first. `Locked` suffix for methods that require caller to hold a mutex
  (e.g., `sessionLocked`, `addCredentialsLocked`).
- **Variables:** Short idiomatic Go (`ctx`, `s`, `vm`, `rp`, `dc`, `f`, `t`).
  Booleans: `shouldDelete`, `clusterExists`, `ok`.
- **Constants:** PascalCase exported, camelCase unexported.
- **JSON tags:** `snake_case` (e.g., `json:"min_age_hours"`).
- **Log keys:** `snake_case` (e.g., `"cached_leases_count"`, `"creation_timestamp"`).

### Error Handling

- Wrap errors with `fmt.Errorf("context: %w", err)` for traceability.
- In multi-server loops: log error and `continue` to the next server; track `lastErr`
  and return it after the loop if set.
- Use `client.IgnoreNotFound(err)` for K8s API get calls.
- Startup errors: log + `os.Exit(1)`.
- No custom error types; vSphere faults checked via `strings.Contains(err.Error(), ...)`.

### Logging

- Logger: `logr.Logger` backed by `go.uber.org/zap` (via `github.com/go-logr/zapr`).
- Structured key-value pairs: `logger.Info("message", "key", value, ...)`.
- Sub-loggers via `logger.WithName("subsystem")` (e.g., `"tag"`, `"folder"`, `"audit"`).
- `V(1)` for debug-level messages.
- Dry-run messages prefixed with `[DRY RUN]`.
- Audit logging gated on `v.Logging.EnableAuditLog`.

### Context

- `context.Context` is always the first function parameter.
- `context.WithTimeout(context.Background(), 60*time.Second)` with `defer cancel()` for
  bounded operations (secret reads, session creation).
- `context.TODO()` only in background goroutines without a parent context.

### Concurrency

- `sync.Mutex` for lease cache (explicit lock/unlock, sometimes without defer for
  fine-grained control).
- `sync.RWMutex` for session pool (always with `defer m.mu.Unlock()`).
- `sync.Map` for `firstSeen` tracking (no explicit locking needed).
- Public methods acquire the lock, then delegate to `*Locked` variants.

### Tests

Two test styles coexist:

**Ginkgo/Gomega BDD** (internal/controller/): `Describe`/`Context`/`It` hierarchy.
Tests use the same package (whitebox). Suite bootstrapped in `suite_test.go` with envtest.

```go
var _ = Describe("Protection Helpers", func() {
    Context("IsProtectedTag", func() {
        It("returns true when tag matches a protected prefix", func() {
            Expect(IsProtectedTag("us-east-ci-12345", []string{"us-"})).To(BeTrue())
        })
    })
})
```

**Standard Go tests** (pkg/utils/): `TestTypeName_Behavior` naming pattern with direct
`t.Errorf`/`t.Fatalf` assertions. No table-driven tests.

### Formatting

- Standard `gofmt` formatting. No strict line length limit (lines may exceed 120 chars
  for log statements and long function signatures).
- `lll` linter is disabled for `internal/` and `api/` paths via `.golangci.yml`.

### Comments

- GoDoc-style comments on all exported types and functions.
- `TODO` comments include author: `// TODO: jcallen: ...`.
- Kubebuilder RBAC markers: `//+kubebuilder:rbac:groups=...`.
- Some files have Apache 2.0 license headers (kubebuilder-scaffolded ones); newer files
  may omit them.

### Reconciler Patterns

- `Reconcile` returns `(ctrl.Result{}, nil)` on success, `(ctrl.Result{}, err)` on error.
  No `RequeueAfter` is used.
- Background scheduled work runs in goroutines started from `SetupWithManager`, using
  `time.Sleep` loops rather than requeueing.
- Struct embedding of `client.Client` and `*vsphere.Metadata` in reconcilers.

### Dependencies

- Vendored (`vendor/` directory). Run `go mod vendor` after changing `go.mod`.
- `go.mod` has `replace` directives for `sigs.k8s.io/cluster-api` and `vm-operator`.
