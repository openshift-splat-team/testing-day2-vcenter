# Machine Scale-Ladder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a 50→300 worker scale-ladder benchmark with per-step provisioning/Ready timing and Machine API diagnostics.

**Architecture:** A dedicated Ginkgo spec scales the existing MachineSet in cumulative increments. Framework helpers watch Machine/Node state by polling existing APIs, capture MAO logs/events, and write resumable JSON artifacts.

**Tech Stack:** Go, Ginkgo v2, Kubernetes typed clients, OpenShift APIs.

**Spec:** `docs/superpowers/specs/2026-02-13-scale-ladder-design.md`

## Global Constraints
- Defaults are 50, 300, and 50 for start, stop, and step.
- A step requires all target workers Running and Ready.
- Failed steps retain artifacts and fail the test.
- Cleanup returns the MachineSet to its initial replica count.

---

### Task 1: Add tested scale configuration parsing
**Files:** Create `test/e2e/scale_ladder_test.go`; test through package compilation and dry-run.
- [ ] Add environment parsing for start/stop/step/timeout/results/cleanup values with validation.
- [ ] Run `go test ./test/e2e -run TestE2E` and confirm the live suite remains skipped without `RUN_E2E`.

### Task 2: Add the scale-ladder benchmark
**Files:** Modify `test/e2e/scale_ladder_test.go`, `Makefile`, `docs/running-tests.md`.
- [ ] Select the existing worker MachineSet and validate initial readiness.
- [ ] Scale cumulatively through configured targets.
- [ ] Record per-step timing and strict completion.
- [ ] Add cleanup returning to the initial replica count.
- [ ] Add `make test-perf-scale` with a 6-hour timeout.

### Task 3: Add diagnostics and artifacts
**Files:** Modify `test/e2e/scale_ladder_test.go`.
- [ ] Capture Machine API controller/operator logs and namespace events per step.
- [ ] Write JSON step artifacts and a summary artifact.
- [ ] Preserve artifacts on failures.

### Task 4: Verify
- [ ] Run `gofmt` and `go test ./...`.
- [ ] Run Ginkgo dry-run for the new label.
- [ ] Review diff and working tree.
