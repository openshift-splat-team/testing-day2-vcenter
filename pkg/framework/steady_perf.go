package framework

import (
	"context"
	"fmt"
	"sort"
	"time"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	machineclient "github.com/openshift/client-go/machine/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// MachineRoleLabel marks machine templates that join the worker pool. It is
// set on the MachineSet template (not the MachineSet object) by the installer.
const MachineRoleLabel = "machine.openshift.io/cluster-api-machine-role"

// NodeJoinTiming holds node-level join timestamps for one machine.
// NodeCreated (node object creationTimestamp) marks the end of ignition:
// the node only registers once machine-config-server delivered its config.
// NodeReady (Ready=True lastTransitionTime) marks the end of MCD + CNI join.
type NodeJoinTiming struct {
	NodeCreated time.Time `json:"nodeCreated"`
	NodeReady   time.Time `json:"nodeReady"`
}

// SteadyMachineTiming is MachineTimingRecord plus node join events. Node
// name equals machine name on the vsphere platform.
type SteadyMachineTiming struct {
	MachineTimingRecord
	NodeCreated time.Time `json:"nodeCreated,omitempty"`
	NodeReady   time.Time `json:"nodeReady,omitempty"`
}

// ListWorkerMachineSets returns all MachineSets whose machine template is
// labeled role=worker, sorted by name.
func ListWorkerMachineSets(ctx context.Context, client machineclient.Interface) ([]machinev1beta1.MachineSet, error) {
	sets, err := client.MachineV1beta1().MachineSets(MachineAPINamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list machinesets: %w", err)
	}
	var worker []machinev1beta1.MachineSet
	for _, s := range sets.Items {
		if s.Spec.Template.Labels[MachineRoleLabel] == "worker" {
			worker = append(worker, s)
		}
	}
	sort.Slice(worker, func(i, j int) bool { return worker[i].Name < worker[j].Name })
	return worker, nil
}

// WatchNodeJoin polls nodes whose names are in `names` and records each
// node's creationTimestamp and Ready=True lastTransitionTime. Returns the
// (possibly partial) map plus the poll error, so callers can record whatever
// was joined even on timeout.
func WatchNodeJoin(ctx context.Context, client kubernetes.Interface, names []string, timeout time.Duration) (map[string]NodeJoinTiming, error) {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	joined := make(map[string]NodeJoinTiming)
	var lastErr error
	pollErr := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			lastErr = err
			return false, nil
		}
		for _, n := range nodes.Items {
			if !want[n.Name] {
				continue
			}
			t, ok := joined[n.Name]
			if !ok {
				t = NodeJoinTiming{NodeCreated: n.CreationTimestamp.Time}
			}
			for _, c := range n.Status.Conditions {
				if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue && t.NodeReady.IsZero() {
					t.NodeReady = c.LastTransitionTime.Time
				}
			}
			joined[n.Name] = t
		}
		lastErr = nil
		for _, t := range joined {
			if t.NodeReady.IsZero() {
				return false, nil
			}
		}
		return len(joined) == len(want), nil
	})
	if pollErr != nil && lastErr != nil {
		return joined, fmt.Errorf("%w: last error: %v", pollErr, lastErr)
	}
	return joined, pollErr
}

// PodLogsSince returns the pod's log lines emitted since `since`.
func PodLogsSince(ctx context.Context, client kubernetes.Interface, namespace, pod string, since time.Time) ([]byte, error) {
	opts := &corev1.PodLogOptions{SinceTime: &metav1.Time{Time: since}}
	return client.CoreV1().Pods(namespace).GetLogs(pod, opts).Do(ctx).Raw()
}

// ListPodNames returns pod names in a namespace whose name has the given prefix.
func ListPodNames(ctx context.Context, client kubernetes.Interface, namespace, namePrefix string) ([]string, error) {
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", namespace, err)
	}
	var names []string
	for _, p := range pods.Items {
		if len(p.Name) >= len(namePrefix) && p.Name[:len(namePrefix)] == namePrefix {
			names = append(names, p.Name)
		}
	}
	return names, nil
}
