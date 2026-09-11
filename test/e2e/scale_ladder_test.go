package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jcallen/testing-day2-vcenter/pkg/framework"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
)

type scaleStepResult struct {
	TargetWorkers       int       `json:"targetWorkers"`
	ScaleRequestTime    time.Time `json:"scaleRequestTime"`
	FirstMachineCreated time.Time `json:"firstMachineCreated,omitempty"`
	LastMachineRunning  time.Time `json:"lastMachineRunning,omitempty"`
	LastNodeReady       time.Time `json:"lastNodeReady,omitempty"`
	ProvisioningSeconds float64   `json:"provisioningSeconds"`
	ReadySeconds        float64   `json:"readySeconds"`
	MachinesRunning     int       `json:"machinesRunning"`
	NodesReady          int       `json:"nodesReady"`
	Complete            bool      `json:"complete"`
	LogErrorCount       int       `json:"logErrorCount"`
	ControllerLogs      string    `json:"controllerLogs,omitempty"`
	OperatorLogs        string    `json:"operatorLogs,omitempty"`
	Events              string    `json:"events,omitempty"`
	Error               string    `json:"error,omitempty"`
}

var _ = Describe("Machine scale ladder", Ordered, Label("perf-scale", "mutating", "p1"), func() {
	var (
		cfg        framework.ScaleLadderConfig
		machineSet string
		initial    int
	)

	BeforeAll(func() {
		var err error
		cfg, err = framework.ParseScaleLadderConfig(os.Getenv)
		Expect(err).NotTo(HaveOccurred())
		sets := listMachineSets()
		for _, set := range sets {
			if set.Spec.Replicas != nil && int(*set.Spec.Replicas) == cfg.Start {
				machineSet = set.Name
				initial = cfg.Start
				break
			}
		}
		Expect(machineSet).NotTo(BeEmpty(), "need a worker MachineSet at PERF_SCALE_START")
		Expect(framework.WaitForAllMachinesHealthy(suiteCtx, clients.Machine, framework.DefaultTimeout)).To(Succeed())
		Expect(framework.WaitForAllNodesReady(suiteCtx, clients.Kube, framework.DefaultTimeout)).To(Succeed())
	})

	AfterAll(NodeTimeout(30*time.Minute), func(ctx SpecContext) {
		if machineSet == "" {
			return
		}
		_ = framework.ScaleMachineSet(ctx, clients.Machine, machineSet, int32(initial))
		_ = framework.WaitForAllNodesReady(ctx, clients.Kube, framework.LongTimeout)
	})

	It("scales workers in 50-node increments and records MAO diagnostics", NodeTimeout(6*time.Hour), func(ctx SpecContext) {
		Expect(os.MkdirAll(cfg.ResultsDir, 0o755)).To(Succeed())
		for target := cfg.Start + cfg.Step; target <= cfg.Stop; target += cfg.Step {
			step := runScaleStep(ctx, machineSet, target, cfg.Timeout)
			path := filepath.Join(cfg.ResultsDir, fmt.Sprintf("step-%03d.json", target))
			data, err := json.MarshalIndent(step, "", "  ")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(path, data, 0o644)).To(Succeed())
			GinkgoWriter.Printf("scale=%d running=%d ready=%d provisioning=%s ready-time=%s errors=%d\n", target, step.MachinesRunning, step.NodesReady, time.Duration(step.ProvisioningSeconds*float64(time.Second)), time.Duration(step.ReadySeconds*float64(time.Second)), step.LogErrorCount)
			Expect(step.Complete).To(BeTrue(), "scale step %d incomplete: %s", target, step.Error)
		}
	})
})

func runScaleStep(ctx context.Context, name string, target int, timeout time.Duration) scaleStepResult {
	started := time.Now()
	result := scaleStepResult{TargetWorkers: target, ScaleRequestTime: started}
	if err := framework.ScaleMachineSet(ctx, clients.Machine, name, int32(target)); err != nil {
		result.Error = err.Error()
		return withScaleDiagnostics(ctx, result, started)
	}
	var lastErr error
	_ = wait.PollUntilContextTimeout(ctx, framework.DefaultPolling, timeout, true, func(ctx context.Context) (bool, error) {
		machines, err := clients.Machine.MachineV1beta1().Machines(framework.MachineAPINamespace).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{"machine.openshift.io/cluster-api-machineset": name}.String()})
		if err != nil {
			lastErr = err
			return false, nil
		}
		result.MachinesRunning = 0
		result.NodesReady = 0
		for _, machine := range machines.Items {
			if machine.DeletionTimestamp != nil {
				continue
			}
			if machine.CreationTimestamp.Time.Before(result.FirstMachineCreated) || result.FirstMachineCreated.IsZero() {
				result.FirstMachineCreated = machine.CreationTimestamp.Time
			}
			if machine.Status.Phase != nil && *machine.Status.Phase == "Running" {
				result.MachinesRunning++
				if result.LastMachineRunning.Before(time.Now()) {
					result.LastMachineRunning = time.Now()
				}
			}
			if machine.Status.NodeRef != nil {
				node, err := clients.Kube.CoreV1().Nodes().Get(ctx, machine.Status.NodeRef.Name, metav1.GetOptions{})
				if err == nil && nodeReady(node) {
					result.NodesReady++
					if result.LastNodeReady.Before(time.Now()) {
						result.LastNodeReady = time.Now()
					}
				}
			}
		}
		if result.MachinesRunning >= target && result.NodesReady >= target {
			return true, nil
		}
		lastErr = fmt.Errorf("running=%d/%d ready=%d/%d", result.MachinesRunning, target, result.NodesReady, target)
		return false, nil
	})
	if result.MachinesRunning >= target && result.NodesReady >= target {
		result.Complete = true
		result.ProvisioningSeconds = result.LastMachineRunning.Sub(started).Seconds()
		result.ReadySeconds = result.LastNodeReady.Sub(started).Seconds()
	} else if lastErr != nil {
		result.Error = lastErr.Error()
	}
	return withScaleDiagnostics(ctx, result, started)
}

func withScaleDiagnostics(ctx context.Context, result scaleStepResult, started time.Time) scaleStepResult {
	result.ControllerLogs = podLogs(ctx, "k8s-app=controller", started, "machine-controller", "machineset-controller")
	result.OperatorLogs = podLogs(ctx, "k8s-app=machine-api-operator", started, "machine-api-operator")
	events, _ := clients.Kube.CoreV1().Events(framework.MachineAPINamespace).List(ctx, metav1.ListOptions{})
	var b strings.Builder
	for _, event := range events.Items {
		fmt.Fprintf(&b, "%s %s %s: %s\n", event.LastTimestamp.Time.Format(time.RFC3339), event.Type, event.Reason, event.Message)
	}
	result.Events = b.String()
	result.LogErrorCount = countErrors(result.ControllerLogs) + countErrors(result.OperatorLogs)
	return result
}

func podLogs(ctx context.Context, selector string, since time.Time, containers ...string) string {
	pods, err := clients.Kube.CoreV1().Pods(framework.MachineAPINamespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, container := range containers {
		for _, pod := range pods.Items {
			if pod.Status.Phase != corev1.PodRunning {
				continue
			}
			data, err := clients.Kube.CoreV1().Pods(framework.MachineAPINamespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: container, SinceTime: &metav1.Time{Time: since}}).Do(ctx).Raw()
			if err == nil {
				fmt.Fprintf(&b, "=== %s/%s ===\n%s\n", pod.Name, container, data)
			}
		}
	}
	return b.String()
}

func countErrors(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(strings.ToLower(line), "error") || strings.Contains(line, "E012") {
			count++
		}
	}
	return count
}

func nodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
