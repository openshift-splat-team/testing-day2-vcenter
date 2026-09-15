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
make test-perf            # run provisioning performance benchmark (PERF-01/02, creates N machines)
make test-perf-steady     # run steady-state provisioning benchmark (drain -> ramp -> +1..+5 increments; hours-long)
make test-real            # run tests requiring a real second vCenter (needs config/lab.yaml)
make test-e2e             # full end-to-end: baseline → apply → verify → all tests → restore
make apply-lab            # add second vCenter to cluster using lab config
make restore-lab          # revert cluster to pre-apply state
make verify-lab           # verify second vCenter was added correctly
```

`apply-lab` and `restore-lab` wait for full cluster readiness (operators stable + all Machines Running) before returning.

All e2e tests require `KUBECONFIG` pointing at a vSphere-platform OpenShift cluster.

Perf tests are excluded from `test-mutating` and `test-e2e` via label filters (`!perf && !perf-steady`).

### Perf test environment variables

| Target | Variables |
|---|---|
| `test-perf` | `PERF_WORKER_COUNT` (default 300), `PERF_STEADY_STATE_SECONDS` (default 60), `PERF_RESULTS_DIR` |
| `test-perf-steady` | `PERF_SS_TARGET` (default 350, total cluster size), `PERF_SS_RAMP_BATCH` (default 10), `PERF_SS_INCREMENTS` (default "1,2,3,4,5"), `PERF_SS_RESULTS_DIR` (default `reports`) |

The steady-state test drains all worker MachineSets to 0 first (restores them in AfterAll), ramps the real `*-worker-0` MachineSet to target in batches with full convergence (every new machine Running AND every node Ready) before each batch, then runs single-round increments at steady state. It writes `steady-state-results.json` (per-machine 5-timestamp chain: machine created/provisioning/provisioned, node created/ready) plus MAO/MCS/MCD pod logs to `<PERF_SS_RESULTS_DIR>/logs/`.

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

The test cluster is reached through the lab bastion (the host that also runs the pull-through registry cache containers). Use the bastion's address from your SSH config / known lab host; it is not recorded here. The bastion clone lives at `~/Development/testing-day2-vcenter` and the kubeconfig at `~/before-installer-testing/vsphere-ipi/auth/kubeconfig`.

```
ssh <bastion> 'cd ~/Development/testing-day2-vcenter && git pull'
ssh <bastion> 'KUBECONFIG=$HOME/before-installer-testing/vsphere-ipi/auth/kubeconfig make -C ~/Development/testing-day2-vcenter test-readonly'
```

Always use `make -C <path> <target>` when running remotely. Never rsync — use git push/pull.

**Branch divergence gotcha:** the bastion clone is on `perf/scale-ladder-50-to-300` tracking the splat-team fork, so plain `git pull` does not bring in new `main` commits. To ship a `main` commit to the bastion, push it over SSH to the bastion repo as a ref, then cherry-pick on the bastion:

```
git push <bastion>:~/Development/testing-day2-vcenter <commit>:refs/heads/perf/steady-state
ssh <bastion> 'cd ~/Development/testing-day2-vcenter && git fetch && git cherry-pick perf/steady-state'
```

Long perf runs are launched with `nohup ... > /tmp/<run>.log 2>&1 &` so they survive SSH disconnects.

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
  provisioning_perf_test.go           Provisioning perf benchmark (PERF-01/02, label `perf`)
  steady_state_perf_test.go           Steady-state provisioning benchmark (label `perf-steady`)
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
- `perf-steady` — steady-state provisioning benchmark (drain, ramp to target in batches, +1..+5 increments; deliberately not `perf` so `test-perf` doesn't trigger it; also excluded from `test-mutating`/`test-e2e`)
- `real-vcenter` — requires lab config with real second vCenter

## Known Issues

### VAP blocks all Infrastructure updates when Machine labels don't match Infrastructure FDs
The `vsphere-failure-domain-in-use-by-machine` VAP checks Machine labels (`machine.openshift.io/region`, `machine.openshift.io/zone`) against the proposed Infrastructure spec. If any Machine has region/zone labels that don't correspond to a failure domain in the spec, the VAP denies the update — even identity patches or adding a new vCenter. These labels are set from vCenter tags. On clusters where vCenter tags are out of sync with Infrastructure FD region/zone values, this blocks all day-2 operations. Three readonly tests currently fail due to this on the test cluster. Regression coverage for the fix (machine-api-operator PR #1536) lives in `test/e2e/vap_test.go`, context `SPLAT-2826: Machine labels matching no failure domain` — it creates a 0-replica probe MachineSet with region/zone labels matching no FD, then verifies identity patches, vCenter adds, and unreferenced-FD removals are allowed via dry-run (labels: `mutating multi-vcenter p1`).

### OVN crash-loop at ~350 total nodes (k8s 1.36 WatchList + 60s informer sync timeout)
On CI payload 5.1.0-0.ci-2026-09-14-010613, at 350 total nodes the `ovnkube-controller` container in every `ovnkube-node` pod crash-loops: the initial Pod informer cache sync (k8s 1.36 WatchList client) exceeds the hardcoded 60s `types.InformerSyncTimeout`, so CNI config is never written and kubelet stays NotReady (`no CNI configuration file`). Deterministic — retries re-LIST the same pods and never converge until the cluster shrinks below ~300 nodes. This is a known failure class (long-standing 60s timeout, no tunable), now reliably triggered by the unified per-node OVN design at scale — not a new ovnkube regression per se, but worth filing with the repro. Workaround for perf runs: keep total node count ≤ ~300, or drain and re-ramp (the steady-state test's P0 self-heals this).

### Cloud config YAML field names
The cloud config `nodes` section uses camelCase YAML keys (`externalNetworkSubnetCidr`, `internalNetworkSubnetCidr`), not kebab-case. The `NodesConfig` struct in `pkg/vsphere/cloud_config.go` must match.

## Gotchas

- The ClusterOperator is named `config-operator`, NOT `cluster-config-operator`.
- `ReplaceInfrastructureSpec` uses JSON merge patch — arrays are replaced entirely, not merged element-wise. When the VAP diffs old vs new, it sees the full array replacement.
- `expectPatchRejected` accepts either xValidation or VAP error messages via `SatisfyAny`, since both admission layers can reject the same bad spec.
- The CSI operator's `findOrphanedTags()` treats any tagged, non-FD `datacenter/datastore` pair as an orphan regardless of how the tag got there — this is exploited by `csi_orphan_tag_test.go` to test orphan cleanup by directly tagging a local-disk datastore via govmomi, without touching the VAP-guarded Infrastructure spec.
- CSI cloud config `[Labels]` section uses key `topology-categories` (comma-separated category names), not the legacy `region`/`zone` keys — see `framework.CSIConfigTopologyCategories`.
- The worker role label (`machine.openshift.io/cluster-api-machine-role=worker`) is on the MachineSet's **machine template** (`spec.template.metadata.labels`), NOT on the MachineSet object — a label-selector query on MachineSets returns nothing. `framework.ListWorkerMachineSets` lists all MachineSets and filters by template labels.
- Ginkgo spec registration happens at tree-build time: any loop that registers `It` blocks (e.g. per-increment specs in the steady-state test) must read its env/config at Describe-closure time, not in `BeforeAll`, or the specs silently don't register.
- On vSphere, node name == machine name, which is what makes the per-machine 5-timestamp correlation chain in the perf tests possible.
