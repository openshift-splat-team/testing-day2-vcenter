# testing-day2-vcenter

QA/QE test suite for the OpenShift vSphere Multi-vCenter Day 2 feature (`VSphereMultiVCenterDay2` feature gate). Tests are Ginkgo v2 / Gomega e2e tests designed to run against a live OpenShift cluster.

## Build & Test

```bash
make build                # compile all packages
make vet                  # go vet
make test-readonly        # run readonly e2e tests (safe, no cluster mutation)
make test-p0              # run p0 readonly tests only
make test-mutating        # run mutating tests (changes cluster state, restores after)
make test-storage         # run storage provisioning tests (needs lab config)
make test-storage-readonly # storage tests that don't provision PVCs
make test-csi-operator    # run CSI operator FD lifecycle tests (needs lab config)
make test-csi-topology    # run CSI ClusterCSIDriver topology config tests (TOPO-01–06, no lab config needed)
make test-csi-orphan      # run CSI synthetic orphan tag tests (SYNTH-*, needs lab config + apply-lab already run)
make test-perf            # run provisioning performance benchmark (mutating, creates 64 machines)
make test-real            # run tests requiring a real second vCenter (needs config/lab.yaml)
make test-e2e             # full end-to-end: baseline → apply → verify → all tests → restore
make apply-lab            # add second vCenter to cluster using lab config
make restore-lab          # revert cluster to pre-apply state
make verify-lab           # verify second vCenter was added correctly
```

`apply-lab` and `restore-lab` wait for full cluster readiness (operators stable + all Machines Running) before returning.

All e2e tests require `KUBECONFIG` pointing at a vSphere-platform OpenShift cluster.

### Test Reports & Ginkgo Flags

JUnit XML reports are written to `reports/` (gitignored). Each target writes a separate file (e.g. `readonly.xml`, `mutating.xml`). The `test-e2e` target writes per-phase reports (`phase1-readonly.xml`, `phase3-readonly.xml`, etc.).

Override Ginkgo behavior via `GINKGO_FLAGS` (default: `-v`):

```bash
make test-readonly GINKGO_FLAGS="-vv"              # verbose + GinkgoWriter output for passing tests
make test-mutating GINKGO_FLAGS="-v --fail-fast"    # stop on first failure
make test-mutating GINKGO_FLAGS="-v --focus='N-TOPO-01'"   # run one test
make test-mutating GINKGO_FLAGS="-v --skip='N-SEQ-01|N-SEQ-02'"  # skip specific tests
```

## Remote Testing

The test cluster is accessed via SSH. The workflow is:
1. Edit and commit locally, `git push`
2. `ssh jcallen@10.38.201.171 'cd ~/Development/testing-day2-vcenter && git pull'`
3. `ssh jcallen@10.38.201.171 'KUBECONFIG=$HOME/before-installer-testing/vsphere-ipi/auth/kubeconfig make -C ~/Development/testing-day2-vcenter test-readonly'`

Always use `make -C <path> <target>` when running remotely. Never rsync — use git push/pull.

## Project Layout

```
cmd/day2-vcenter/       CLI tool for apply/restore/verify lab operations
pkg/framework/          Kubernetes/OpenShift client helpers, constants, CR operations
pkg/lab/                Lab apply/restore/verify workflow, credential management
pkg/labconfig/          Lab YAML config loading and validation
pkg/vsphere/            vSphere types, cloud config parser, Infrastructure spec helpers
test/e2e/               Ginkgo e2e test suites
  helpers_test.go       BeforeSuite, shared test utilities, spec builders
  infrastructure_validation_test.go   xValidation tests (N-INF-*)
  vap_test.go           ValidatingAdmissionPolicy tests (N-SEQ-*)
  configmap_content_test.go           Cloud config format/parity tests
  configmap_ownership_test.go         ConfigMap ownership migration tests
  operator_health_test.go             ClusterOperator health checks
  topology_lifecycle_test.go          Mutating lifecycle tests
  csi_storage_test.go                 CSI storage provisioning tests
  csi_operator_lifecycle_test.go      CSI operator FD lifecycle tests (tag/SPBM/PV-safety)
  csi_topology_config_test.go         ClusterCSIDriver topology config + precedence tests (TOPO-*)
  csi_orphan_tag_test.go              Synthetic orphan tag tests via direct datastore tagging (SYNTH-*)
  real_vcenter_test.go                Tests requiring real second vCenter
  problem_detector_test.go            vsphere-problem-detector tests (stub)
config/lab.yaml.example Lab config template
plans/                  Test plan documents
```

## Key Constants (pkg/framework/constants.go)

- Source ConfigMap: `openshift-config/cloud-provider-config`, data key `config`
- Managed ConfigMap: `openshift-config-managed/kube-cloud-config`, data key `cloud.conf`
- CCM ConfigMap: `openshift-cloud-controller-manager/cloud-conf`, data key `cloud.conf`
- ClusterOperators checked: `cloud-controller-manager`, `config-operator`, `machine-api`, `storage`
- Feature gate: `VSphereMultiVCenterDay2`

## Test Labels

- `readonly` — safe to run, no cluster mutation. xValidation tests use server-side dry-run. Multi-vCenter tests skip on single-vCenter clusters.
- `mutating` — modifies cluster state (backup/restore around each test). VAP denial tests use real patches (denied = no mutation).
- `p0`, `p1`, `p2` — priority tiers
- `validation` — xValidation (CRD CEL rules) tests
- `admission` — ValidatingAdmissionPolicy tests
- `config` — cloud config content tests
- `operator` — ClusterOperator health tests
- `csi-operator` — CSI operator FD lifecycle tests (tag cleanup, SPBM, PV safety); also covers `csi-topology` and `csi-orphan`
- `csi-topology` — ClusterCSIDriver topology config + Infrastructure precedence tests (TOPO-*)
- `csi-orphan` — synthetic orphan tag tests via direct datastore tagging, second vCenter only (SYNTH-*)
- `perf` — provisioning performance benchmark (creates a temporary MachineSet, scales to N machines, records timing)
- `real-vcenter` — requires lab config with real second vCenter

## Known Issues

### VAP blocks all Infrastructure updates when Machine labels don't match Infrastructure FDs
The `vsphere-failure-domain-in-use-by-machine` VAP checks Machine labels (`machine.openshift.io/region`, `machine.openshift.io/zone`) against the proposed Infrastructure spec. If any Machine has region/zone labels that don't correspond to a failure domain in the spec, the VAP denies the update — even identity patches or adding a new vCenter. These labels are set from vCenter tags. On clusters where vCenter tags are out of sync with Infrastructure FD region/zone values, this blocks all day-2 operations. Three readonly tests currently fail due to this on the test cluster. Regression coverage for the fix (machine-api-operator PR #1536) lives in `test/e2e/vap_test.go`, context `SPLAT-2826: Machine labels matching no failure domain` — it creates a 0-replica probe MachineSet with region/zone labels matching no FD, then verifies identity patches, vCenter adds, and unreferenced-FD removals are allowed via dry-run (labels: `mutating multi-vcenter p1`).

### Cloud config YAML field names
The cloud config `nodes` section uses camelCase YAML keys (`externalNetworkSubnetCidr`, `internalNetworkSubnetCidr`), not kebab-case. The `NodesConfig` struct in `pkg/vsphere/cloud_config.go` must match.

## Gotchas

- The ClusterOperator is named `config-operator`, NOT `cluster-config-operator`.
- `ReplaceInfrastructureSpec` uses JSON merge patch — arrays are replaced entirely, not merged element-wise. When the VAP diffs old vs new, it sees the full array replacement.
- `expectPatchRejected` accepts either xValidation or VAP error messages via `SatisfyAny`, since both admission layers can reject the same bad spec.
- The CSI operator's `findOrphanedTags()` treats any tagged, non-FD `datacenter/datastore` pair as an orphan regardless of how the tag got there — this is exploited by `csi_orphan_tag_test.go` to test orphan cleanup by directly tagging a local-disk datastore via govmomi, without touching the VAP-guarded Infrastructure spec.
- CSI cloud config `[Labels]` section uses key `topology-categories` (comma-separated category names), not the legacy `region`/`zone` keys — see `framework.CSIConfigTopologyCategories`.
