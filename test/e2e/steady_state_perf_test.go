package e2e

// Steady-state provisioning benchmark (perf-steady).
//
// Mirrors the customer-reported scenario: a large cluster already at steady
// state, scaled up in small increments. Phases:
//
//	P0  drain all worker MachineSets to 0 (also self-heals OVN-crashed nodes)
//	P1  ramp 0 -> target in fixed-size batches, full convergence (every new
//	    machine Running AND every node Ready) before the next batch
//	P2  single round of steady-state increments (+1, +2, +3, +4, +5)
//	P3  capture MAO/MCS operator logs and write steady-state-results.json
//
// Per-machine timing chain (node name == machine name on vSphere):
//	machine.created -> machine.provisioning -> machine.provisioned ->
//	node.created (ignition/MCS delivery complete) -> node.ready (MCD+CNI)
//
// Deliberately labeled perf-steady (not perf) so make test-perf does not
// pick it up. Deterministic: each step converges before the next starts.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jcallen/testing-day2-vcenter/pkg/framework"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	perfSSMachineStepTimeout = 60 * time.Minute // new machines must reach Running
	perfSSNodeReadyTimeout   = 45 * time.Minute // all nodes must be Ready
	perfSSDrainTimeout       = 30 * time.Minute // per-MS drain after scaling to 0
	perfSSRestoreTimeout     = 60 * time.Minute // wait for restored cluster
)

// Package-scoped so the step helpers below can share Describe state.
// The spec is Ordered and the label excludes it from normal runs, so no
// concurrent access is possible.
var (
	steadyRampMS   string
	steadyCurrent  int
	steadySeenMacs map[string]bool
)

var _ = Describe("Steady-state provisioning benchmark", Ordered, Label("perf-steady", "mutating", "p1"), func() {
	var (
		target       int
		rampBatch    int
		increments   []int
		resultsDir   string
		originals  map[string]int // MS name -> original replica count
		runStart   time.Time
		rampResults  []steadyStepResult
		steadyResults []steadyStepResult
	)

	// Parsed at tree-build time (not in BeforeAll) so the P2 spec count is
	// known when the specs are registered.
	target = ssEnvInt("PERF_SS_TARGET", 350)
	rampBatch = ssEnvInt("PERF_SS_RAMP_BATCH", 10)
	resultsDir = os.Getenv("PERF_SS_RESULTS_DIR")
	if resultsDir == "" {
		resultsDir = "reports"
	}
	increments = ssEnvIntList("PERF_SS_INCREMENTS", "1,2,3,4,5")

	BeforeAll(func() {
		steadySeenMacs = map[string]bool{}
		GinkgoWriter.Printf("steady-state benchmark: target=%d rampBatch=%d increments=%v resultsDir=%s\n",
			target, rampBatch, increments, resultsDir)
	})

	AfterAll(NodeTimeout(90*time.Minute), func(ctx SpecContext) {
		if originals == nil {
			GinkgoWriter.Println("nothing to restore (P0 did not run)")
			return
		}
		for name, reps := range originals {
			if reps <= 0 {
				continue
			}
			GinkgoWriter.Printf("restoring machineset %s to %d replicas\n", name, reps)
			if err := framework.ScaleMachineSet(ctx, clients.Machine, name, int32(reps)); err != nil {
				GinkgoWriter.Printf("warning: restore scale of %s: %v\n", name, err)
				continue
			}
		}
		if err := framework.WaitForAllNodesReady(ctx, clients.Kube, perfSSRestoreTimeout); err != nil {
			GinkgoWriter.Printf("warning: nodes not fully Ready after restore: %v\n", err)
		}
	})

	It("P0: should drain all worker MachineSets", func(ctx SpecContext) {
		workerSets, err := framework.ListWorkerMachineSets(ctx, clients.Machine)
		Expect(err).NotTo(HaveOccurred())
		Expect(workerSets).NotTo(BeEmpty(), "no worker MachineSets found")

		originals = map[string]int{}
		for _, s := range workerSets {
			reps := 0
			if s.Spec.Replicas != nil {
				reps = int(*s.Spec.Replicas)
			}
			originals[s.Name] = reps
			if reps > 0 {
				GinkgoWriter.Printf("scaling machineset %s %d -> 0\n", s.Name, reps)
				Expect(framework.ScaleMachineSet(ctx, clients.Machine, s.Name, 0)).To(Succeed())
			}
		}
		for _, s := range workerSets {
			if originals[s.Name] == 0 {
				continue
			}
			Expect(drainMachineSet(ctx, s.Name)).To(Succeed(), "worker machineset %s must drain", s.Name)
		}

		// Delete leftover benchmark sets from prior runs (perf-bench-*, perf-newml-*).
		for _, s := range listMachineSets() {
			if strings.HasPrefix(s.Name, "perf-bench-") || strings.HasPrefix(s.Name, "perf-newml-") {
				GinkgoWriter.Printf("deleting leftover machineset %s\n", s.Name)
				framework.ForceDeleteMachineSetMachines(ctx, clients.Machine, s.Name)
				Expect(framework.DeleteMachineSet(ctx, clients.Machine, s.Name)).To(Succeed())
			}
		}

		steadyRampMS = workerSets[0].Name
		Expect(framework.WaitForAllNodesReady(ctx, clients.Kube, perfSSDrainTimeout)).To(Succeed(),
			"cluster must settle (all remaining nodes Ready) before the ramp")
		runStart = time.Now()
		Expect(os.MkdirAll(resultsDir, 0o755)).To(Succeed())
		GinkgoWriter.Printf("cluster drained; ramp machine set: %s (originals: %v)\n", steadyRampMS, originals)
	})

	It("P1: should ramp to target in batches with full convergence", func(ctx SpecContext) {
		replicas := 0
		for i := 1; replicas < target; i++ {
			replicas += rampBatch
			if replicas > target {
				replicas = target
			}
			res := runSteadyStep(ctx, i, replicas)
			rampResults = append(rampResults, res)
		}
	})

	for _, inc := range increments {
		inc := inc
		It(fmt.Sprintf("P2+%d: should provision %d machine(s) at steady state", inc, inc), func(ctx SpecContext) {
			res := runSteadyStep(ctx, inc, steadyCurrent+inc)
			steadyResults = append(steadyResults, res)
		})
	}

	It("P3: should capture operator logs and write results", func(ctx SpecContext) {
		logDir := captureOperatorLogs(ctx, filepath.Join(resultsDir, "logs"), runStart)

		doc := steadyStateDoc{
			ClusterTarget: target,
			RampBatch:     rampBatch,
			SteadyIncrements: increments,
			RampMachineSet:  steadyRampMS,
			StartTime:       runStart,
			EndTime:         time.Now(),
			Ramp:            rampResults,
			Steady:          steadyResults,
		}
		writeSteadyResults(resultsDir, doc)
		printSteadySummary(doc, logDir)
	})
})

// steadyStepResult captures one converged ramp batch or steady increment.
type steadyStepResult struct {
	Index         int                           `json:"index"`
	ReplicasAfter int                           `json:"replicasAfter"`
	Start         time.Time                     `json:"start"`
	End           time.Time                     `json:"end"`
	Duration      string                        `json:"duration"`
	Machines      []framework.SteadyMachineTiming `json:"machines"`
}

// steadyStateDoc is the top-level results document written to disk.
type steadyStateDoc struct {
	ClusterTarget    int                `json:"clusterTarget"`
	RampBatch        int                `json:"rampBatch"`
	SteadyIncrements []int              `json:"steadyIncrements"`
	RampMachineSet   string             `json:"rampMachineSet"`
	StartTime        time.Time          `json:"startTime"`
	EndTime          time.Time          `json:"endTime"`
	Ramp             []steadyStepResult `json:"ramp"`
	Steady           []steadyStepResult `json:"steady"`
}

func ssEnvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func ssEnvIntList(name, def string) []int {
	v := os.Getenv(name)
	if v == "" {
		v = def
	}
	var out []int
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if n, err := strconv.Atoi(part); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	return out
}

// drainMachineSet waits for a MachineSet to drain, force-deleting machines
// if the wait times out.
func drainMachineSet(ctx SpecContext, msName string) error {
	if err := framework.WaitForMachineSetDrainedWithLog(ctx, clients.Machine, msName, perfSSDrainTimeout); err != nil {
		GinkgoWriter.Printf("drain of %s timed out, force-deleting machines\n", msName)
		framework.ForceDeleteMachineSetMachines(ctx, clients.Machine, msName)
		if err2 := framework.WaitForMachineSetDrainedWithLog(ctx, clients.Machine, msName, perfSSDrainTimeout); err2 != nil {
			return fmt.Errorf("drain of %s: %w (retry: %v)", msName, err, err2)
		}
	}
	return nil
}

// runSteadyStep scales steadyRampMS to `after` replicas and blocks until
// every new machine is Running AND every node in the cluster is Ready.
// Records the five-timestamp timing chain for each new machine.
func runSteadyStep(ctx SpecContext, index, after int) steadyStepResult {
	res := steadyStepResult{Index: index, ReplicasAfter: after, Start: time.Now()}
	batch := after - steadyCurrent
	GinkgoWriter.Printf("\n=== steady-step %d: scaling %s %d -> %d (+%d) ===\n", index, steadyRampMS, steadyCurrent, after, batch)

	Expect(framework.ScaleMachineSet(ctx, clients.Machine, steadyRampMS, int32(after))).To(Succeed())

	macs, err := watchNewMachinePhases(ctx, batch)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "step %d: new machines did not reach Running in %s", index, perfSSMachineStepTimeout)
	ExpectWithOffset(1, framework.WaitForAllNodesReady(ctx, clients.Kube, perfSSNodeReadyTimeout)).To(
		Succeed(), "step %d: all nodes must be Ready before the next step (OVN/kubelet join blocker)", index)

	names := make([]string, 0, len(macs))
	for _, m := range macs {
		names = append(names, m.Name)
	}
	join, _ := framework.WatchNodeJoin(ctx, clients.Kube, names, 3*time.Minute)
	for i := range macs {
		if j, ok := join[macs[i].Name]; ok {
			macs[i].NodeCreated = j.NodeCreated
			macs[i].NodeReady = j.NodeReady
		}
	}
	steadyCurrent = after
	res.End = time.Now()
	res.Duration = fmtDuration(res.End.Sub(res.Start))
	res.Machines = macs
	printStepSummary(res)
	return res
}

// watchNewMachinePhases polls machines belonging to rampMS and records phase
// timestamps for machines not in seenMachines. Returns when `expect` of the
// new machines are Running (or the timeout elapses with partial records).
func watchNewMachinePhases(ctx SpecContext, expect int) ([]framework.SteadyMachineTiming, error) {
	recs := map[string]*framework.SteadyMachineTiming{}
	var lastErr error
	pollErr := wait.PollUntilContextTimeout(ctx, 2*time.Second, perfSSMachineStepTimeout, true, func(ctx context.Context) (bool, error) {
		machines, err := clients.Machine.MachineV1beta1().Machines(framework.MachineAPINamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "machine.openshift.io/cluster-api-machineset=" + steadyRampMS,
		})
		if err != nil {
			lastErr = err
			return false, nil
		}
		now := time.Now()
		running := 0
		for _, m := range machines.Items {
			if m.DeletionTimestamp != nil || steadySeenMacs[m.Name] {
				continue
			}
			steadySeenMacs[m.Name] = true
			rec, ok := recs[m.Name]
			if !ok {
				rec = &framework.SteadyMachineTiming{MachineTimingRecord: framework.MachineTimingRecord{
					Name:    m.Name,
					Created: m.CreationTimestamp.Time,
				}}
				recs[m.Name] = rec
			}
			phase := ""
			if m.Status.Phase != nil {
				phase = *m.Status.Phase
			}
			switch phase {
			case "Provisioning":
				if rec.Provisioning.IsZero() {
					rec.Provisioning = now
				}
			case "Provisioned":
				if rec.Provisioning.IsZero() {
					rec.Provisioning = now
				}
				if rec.Provisioned.IsZero() {
					rec.Provisioned = now
				}
			case "Running":
				if rec.Provisioning.IsZero() {
					rec.Provisioning = now
				}
				if rec.Provisioned.IsZero() {
					rec.Provisioned = now
				}
				if rec.Running.IsZero() {
					rec.Running = now
				}
				running++
			}
		}
		lastErr = nil
		return running >= expect, nil
	})
	if pollErr != nil {
		if lastErr != nil {
			return nil, fmt.Errorf("watch %d new machines on %s: %w (last list error: %v)", expect, steadyRampMS, pollErr, lastErr)
		}
		return nil, fmt.Errorf("watch %d new machines on %s: %w", expect, steadyRampMS, pollErr)
	}
	out := make([]framework.SteadyMachineTiming, 0, len(recs))
	for _, r := range recs {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// captureOperatorLogs saves MAO and MCS pod logs since `since` into logDir.
func captureOperatorLogs(ctx SpecContext, logDir string, since time.Time) string {
	Expect(os.MkdirAll(logDir, 0o755)).To(Succeed())
	capture := func(ns, prefix, filePrefix string) {
		pods, err := framework.ListPodNames(ctx, clients.Kube, ns, prefix)
		if err != nil {
			GinkgoWriter.Printf("warning: list %s pods: %v\n", prefix, err)
			return
		}
		for _, p := range pods {
			b, err := framework.PodLogsSince(ctx, clients.Kube, ns, p, since)
			if err != nil {
				GinkgoWriter.Printf("warning: logs for %s/%s: %v\n", ns, p, err)
				continue
			}
			path := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", filePrefix, p))
			if err := os.WriteFile(path, b, 0o644); err != nil {
				GinkgoWriter.Printf("warning: write %s: %v\n", path, err)
				continue
			}
			GinkgoWriter.Printf("saved %s (%d bytes)\n", path, len(b))
		}
	}
	capture("openshift-machine-api", "machine-api-operator", "mao")
	capture("openshift-machine-config-operator", "machine-config-server", "mcs")
	capture("openshift-machine-config-operator", "machine-config-daemon", "mcd")
	return logDir
}

func writeSteadyResults(dir string, doc steadyStateDoc) {
	b, err := json.MarshalIndent(doc, "", "  ")
	Expect(err).NotTo(HaveOccurred())
	path := filepath.Join(dir, "steady-state-results.json")
	Expect(os.WriteFile(path, b, 0o644)).To(Succeed())
	GinkgoWriter.Printf("results written to %s\n", path)
}

// gapStats computes p50/p90/max over a set of durations.
type gapStats struct {
	p50  time.Duration
	p90  time.Duration
	max  time.Duration
	n    int
}

func gapStatsOf(durations []time.Duration) *gapStats {
	if len(durations) == 0 {
		return nil
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return &gapStats{
		p50: percentile(durations, 0.50),
		p90: percentile(durations, 0.90),
		max: durations[len(durations)-1],
		n:   len(durations),
	}
}

func printGap(label string, g *gapStats) {
	if g == nil {
		GinkgoWriter.Printf("  %-28s n=0\n", label)
		return
	}
	GinkgoWriter.Printf("  %-28s n=%-3d p50=%-8s p90=%-8s max=%s\n",
		label, g.n, fmtDuration(g.p50), fmtDuration(g.p90), fmtDuration(g.max))
}

// printStepSummary prints the four gap distributions for one converged step:
//	mao        machine.created -> machine.provisioning   (MAO queue/reconcile)
//	clone      machine.provisioning -> machine.provisioned (vCenter clone)
//	join       machine.provisioned -> node.created       (boot + MCS/ignition)
//	ready      node.created -> node.ready                (MCD + CNI join)
func printStepSummary(res steadyStepResult) {
	var mao, clone, join, ready []time.Duration
	for _, m := range res.Machines {
		if !m.Provisioning.IsZero() {
			mao = append(mao, m.Provisioning.Sub(m.Created))
		}
		if !m.Provisioned.IsZero() && !m.Provisioning.IsZero() {
			clone = append(clone, m.Provisioned.Sub(m.Provisioning))
		}
		if !m.NodeCreated.IsZero() && !m.Provisioned.IsZero() {
			join = append(join, m.NodeCreated.Sub(m.Provisioned))
		}
		if !m.NodeReady.IsZero() && !m.NodeCreated.IsZero() {
			ready = append(ready, m.NodeReady.Sub(m.NodeCreated))
		}
	}
	GinkgoWriter.Printf("--- step %d summary: +%d machines, %s, %d recorded ---\n",
		res.Index, res.ReplicasAfter, res.Duration, len(res.Machines))
	printGap("mao (created->provisioning)", gapStatsOf(mao))
	printGap("clone (provisioning->provisioned)", gapStatsOf(clone))
	printGap("join (provisioned->node.created)", gapStatsOf(join))
	printGap("ready (node.created->node.ready)", gapStatsOf(ready))
}

func printSteadySummary(doc steadyStateDoc, logDir string) {
	GinkgoWriter.Printf("\n=== steady-state benchmark complete ===\n")
	GinkgoWriter.Printf("cluster target:   %d nodes (ramp %d in batches of %d)\n",
		doc.ClusterTarget, len(doc.Ramp), doc.RampBatch)
	GinkgoWriter.Printf("total run:        %s (started %s)\n",
		fmtDuration(doc.EndTime.Sub(doc.StartTime)), doc.StartTime.Format(time.RFC3339))
	GinkgoWriter.Printf("\n--- ramp steps ---\n")
	for _, r := range doc.Ramp {
		GinkgoWriter.Printf("  step %-3d -> %-4d machines in %s\n", r.Index, r.ReplicasAfter, r.Duration)
	}
	GinkgoWriter.Printf("\n--- steady increments (at ~%d nodes) ---\n", doc.ClusterTarget)
	for _, s := range doc.Steady {
		printStepSummary(s)
	}
	GinkgoWriter.Printf("\noperator logs: %s\n", logDir)
}
