package e2e

import (
	"fmt"
	"time"

	"github.com/jcallen/testing-day2-vcenter/pkg/framework"
	"github.com/jcallen/testing-day2-vcenter/pkg/vsphere"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("ValidatingAdmissionPolicies", Label("readonly", "admission", "p0"), func() {
	Context("when VSphereMultiVCenterDay2 is enabled", func() {
		BeforeEach(func() {
			requireGateEnabled()
		})

		// SPLAT-2854: VAP constructors that omit server-defaulted fields
		// (MatchPolicy, NamespaceSelector, ObjectSelector) cause resourceapply
		// to see a diff on every sync cycle, triggering spurious updates.
		// After the fix, resourceVersion must be stable across sync cycles.
		It("should not update VAPs on every sync cycle (SPLAT-2854)", Label("p1"), func() {
			vapNames := []string{
				framework.VAPMachineFailureDomainName,
				framework.VAPCPMSFailureDomainName,
				framework.VAPMachineSetFailureDomainName,
			}

			initialVersions := make(map[string]string, len(vapNames))
			for _, name := range vapNames {
				vap, err := clients.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(suiteCtx, name, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred(), "VAP %q should exist", name)
				initialVersions[name] = vap.ResourceVersion
				GinkgoWriter.Printf("VAP %s initial resourceVersion=%s\n", name, vap.ResourceVersion)
			}

			// MAO syncs every ~5s during active reconciliation.
			// Wait 30s to cover ~6 sync cycles — enough to detect spurious updates.
			syncCycles := 6
			syncInterval := 5 * time.Second
			wait := time.Duration(syncCycles) * syncInterval
			GinkgoWriter.Printf("waiting %s (%d sync cycles) for resourceVersion stability\n", wait, syncCycles)
			time.Sleep(wait)

			for _, name := range vapNames {
				vap, err := clients.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(suiteCtx, name, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(vap.ResourceVersion).To(Equal(initialVersions[name]),
					fmt.Sprintf("VAP %s resourceVersion changed from %s to %s — spurious update detected (SPLAT-2854)",
						name, initialVersions[name], vap.ResourceVersion))
			}
		})

		It("should install vSphere failure domain VAP resources", func() {
			for _, name := range []string{
				framework.VAPMachineFailureDomainName,
				framework.VAPCPMSFailureDomainName,
				framework.VAPMachineSetFailureDomainName,
			} {
				vap, err := clients.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(suiteCtx, name, metav1.GetOptions{})
				Expect(err).NotTo(HaveOccurred(), "VAP %q should exist", name)
				Expect(vap.Spec.Validations).NotTo(BeEmpty())

				_, err = clients.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Get(suiteCtx, name, metav1.GetOptions{})
				if err != nil {
					_, err = clients.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Get(suiteCtx, name+"-binding", metav1.GetOptions{})
				}
				Expect(err).NotTo(HaveOccurred(), "VAP binding for %q should exist", name)
			}
		})

		It("should deny removing a failure domain referenced by a Machine (N-SEQ-01)", Label("mutating", "multi-vcenter"), func() {
			requireMultiVCenter()
			infra := currentInfrastructure()
			region, zone, ok := findMachineBackedFailureDomain(infra)
			if !ok {
				Skip("no Machine-backed failure domain found")
			}
			expectFailureDomainRemovalDenied(infra, region, zone)
		})

		It("should deny removing a failure domain referenced by a CPMS (N-SEQ-02)", Label("mutating", "multi-vcenter"), func() {
			requireMultiVCenter()
			infra := currentInfrastructure()
			region, zone, ok := findCPMSBackedFailureDomain(infra)
			if !ok {
				Skip("no CPMS-backed failure domain found")
			}
			expectFailureDomainRemovalDenied(infra, region, zone)
		})

		It("should deny removing a failure domain referenced by a MachineSet (N-SEQ-03)", Label("mutating", "multi-vcenter"), func() {
			requireMultiVCenter()
			infra := currentInfrastructure()
			fds := framework.GetFailureDomains(infra)
			if len(fds) == 0 {
				Skip("no failure domains configured")
			}

			sets := listMachineSets()
			if len(sets) == 0 {
				Skip("no existing MachineSets to clone providerSpec from")
			}

			fd := fds[0]
			msName := "e2e-vap-ms-n-seq-03"
			ms := framework.CloneMachineSetForVAP(sets[0], msName, fd.Region, fd.Zone, 1)

			created, err := framework.CreateMachineSet(suiteCtx, clients.Machine, ms)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_ = framework.ScaleMachineSet(suiteCtx, clients.Machine, created.Name, 0)
				Eventually(func() error {
					return framework.WaitForMachineSetDrained(suiteCtx, clients.Machine, created.Name)
				}).WithTimeout(framework.LongTimeout).WithPolling(framework.DefaultPolling).Should(Succeed())
				_ = framework.DeleteMachineSet(suiteCtx, clients.Machine, created.Name)
			})

			GinkgoWriter.Printf("waiting for MachineSet %s to scale to 1...\n", msName)
			Eventually(func() error {
				return framework.WaitForMachineSetMachines(suiteCtx, clients.Machine, msName, 1)
			}).WithTimeout(framework.LongTimeout).WithPolling(framework.DefaultPolling).Should(Succeed())

			expectFailureDomainRemovalDenied(infra, fd.Region, fd.Zone)
		})

		It("should allow removing an unreferenced failure domain via dry-run", Label("multi-vcenter"), func() {
			requireMultiVCenter()
			infra := currentInfrastructure()
			fds := framework.GetFailureDomains(infra)
			if len(fds) == 0 {
				Skip("no failure domains configured")
			}

			var candidate *configv1.VSpherePlatformFailureDomainSpec
			for i := range fds {
				spec := specWithoutFailureDomain(infra, fds[i].Region, fds[i].Zone)
				_, err := patchInfrastructureSpec(spec, true)
				if err == nil {
					candidate = &fds[i]
					GinkgoWriter.Printf("FD %q (region=%s zone=%s) is unreferenced\n", fds[i].Name, fds[i].Region, fds[i].Zone)
					break
				}
				GinkgoWriter.Printf("FD %q is referenced: %s\n", fds[i].Name, framework.InfrastructurePatchError(err))
			}
			if candidate == nil {
				Skip("all failure domains are referenced by Machines, CPMS, or MachineSets")
			}
		})

		// SPLAT-2826: the Machine and MachineSet failure-domain VAPs only
		// compared labels against the *proposed* spec, so any Infrastructure
		// update was denied when Machine/MachineSet region/zone labels matched
		// no failure domain (e.g. vCenter tags out of sync with FD definitions).
		// The fix (machine-api-operator PR #1536) adds an oldFds variable from
		// oldObject and denies only removal of an FD that existed before. These
		// dry-run tests keep a MachineSet whose labels match no FD and verify
		// day-2 operations still succeed. They fail pre-fix on any cluster,
		// because the synthetic labels make the mismatch condition hold by
		// construction.
		Context("SPLAT-2826: Machine labels matching no failure domain", Label("p1", "mutating", "multi-vcenter"), func() {
			const (
				bogusRegion = "splat2826-nowhere"
				bogusZone   = "splat2826-nowhere-1a"
				probeMS     = "e2e-vap-splat-2826"
			)

			BeforeEach(func() {
				requireMultiVCenter()
				infra := currentInfrastructure()
				for _, fd := range framework.GetFailureDomains(infra) {
					Expect(fd.Region).NotTo(Equal(bogusRegion),
						"synthetic region %q collides with real failure domain %q", bogusRegion, fd.Name)
					Expect(fd.Zone).NotTo(Equal(bogusZone),
						"synthetic zone %q collides with real failure domain %q", bogusZone, fd.Name)
				}

				sets := listMachineSets()
				if len(sets) == 0 {
					Skip("no existing MachineSets to clone providerSpec from")
				}

				_, err := clients.Machine.MachineV1beta1().MachineSets(framework.MachineAPINamespace).Get(suiteCtx, probeMS, metav1.GetOptions{})
				if err == nil {
					return // leftover probe from a previous run
				}

				// replicas=0: no VMs are ever created; the MachineSet VAP matches
				// template labels, so the probe trips the VAP without provisioning.
				ms := framework.CloneMachineSetForVAP(sets[0], probeMS, bogusRegion, bogusZone, 0)
				created, err := framework.CreateMachineSet(suiteCtx, clients.Machine, ms)
				Expect(err).NotTo(HaveOccurred())
				Expect(created.Name).To(Equal(probeMS))
				DeferCleanup(func() {
					_ = framework.DeleteMachineSet(suiteCtx, clients.Machine, probeMS)
				})
			})

			It("should allow re-applying an unchanged Infrastructure spec via dry-run", func() {
				infra := currentInfrastructure()
				spec := vsphere.CloneInfrastructureSpec(infra.Spec)
				expectPatchAllowedDryRun(&spec)
			})

			It("should allow adding a vCenter (no FD change) via dry-run", func() {
				infra := currentInfrastructure()
				if len(framework.GetVCenters(infra)) >= 3 {
					Skip("cluster already has 3 vCenters")
				}
				expectPatchAllowedDryRun(addSecondVCenterSpec(infra))
			})

			It("should allow removing an unreferenced failure domain via dry-run", func() {
				infra := currentInfrastructure()
				fds := framework.GetFailureDomains(infra)
				if len(fds) == 0 {
					Skip("no failure domains configured")
				}

				// Build the set of FD region/zone pairs that are legitimately
				// in use (the probe MachineSet references none by construction).
				referenced := map[string]bool{}
				for _, m := range listMachines() {
					if r, z, ok := machineLabeledFailureDomain(m); ok {
						referenced[r+"/"+z] = true
					}
				}
				for _, s := range listMachineSets() {
					if s.Name == probeMS || s.Spec.Template.Labels == nil {
						continue
					}
					r, z := s.Spec.Template.Labels[framework.MachineRegionLabel], s.Spec.Template.Labels[framework.MachineZoneLabel]
					if r != "" && z != "" {
						referenced[r+"/"+z] = true
					}
				}
				fdByName := map[string]configv1.VSpherePlatformFailureDomainSpec{}
				for _, fd := range fds {
					fdByName[fd.Name] = fd
				}
				for _, cpms := range listCPMS() {
					for _, name := range framework.CPMSVSphereFailureDomainNames(&cpms) {
						if fd, ok := fdByName[name]; ok {
							referenced[fd.Region+"/"+fd.Zone] = true
						}
					}
				}

				var candidate *configv1.VSpherePlatformFailureDomainSpec
				for i := range fds {
					if !referenced[fds[i].Region+"/"+fds[i].Zone] {
						candidate = &fds[i]
						GinkgoWriter.Printf("FD %q (region=%s zone=%s) is unreferenced\n", fds[i].Name, fds[i].Region, fds[i].Zone)
						break
					}
				}
				if candidate == nil {
					Skip("all failure domains are referenced by Machines, CPMS, or MachineSets")
				}

				spec := specWithoutFailureDomain(infra, candidate.Region, candidate.Zone)
				_, err := patchInfrastructureSpec(spec, true)
				Expect(err).NotTo(HaveOccurred(),
					"removing unreferenced FD %q should be allowed even though Machine labels match no FD (SPLAT-2826): %s",
					candidate.Name, framework.InfrastructurePatchError(err))
			})
		})
	})

	Context("when VSphereMultiVCenterDay2 is disabled", func() {
		It("should not require vSphere VAP resources", func() {
			if gateEnabled {
				Skip("gate is enabled on this cluster")
			}
			_, err := clients.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(suiteCtx, framework.VAPMachineFailureDomainName, metav1.GetOptions{})
			Expect(err).To(HaveOccurred())
		})
	})
})
