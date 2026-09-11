# Machine Scale-Ladder Benchmark

## Goal
Measure cumulative worker scale-up from 50 to 300 workers in 50-node increments, recording Machine provisioning time, Node Ready time, and Machine API errors for each increment.

## Design
Add a separate labeled e2e benchmark instead of changing PERF-01. It scales the existing worker MachineSet, waits for the newly expected Machines and Nodes to be Ready, and writes one JSON result per target. The benchmark captures Machine API controller/operator pod logs and cluster events for each step, with environment controls for start, stop, step, timeout, results directory, and cleanup target.

## Constraints
- Default ladder: 50, 100, 150, 200, 250, 300.
- Existing cluster must start at the configured start target and be healthy.
- A step is complete only when the target worker count is Running and Ready.
- Failed steps retain artifacts and fail the test.
- Cleanup scales the original MachineSet back to the initial target.
- No cluster mutation outside the MachineSet replica count and final cleanup.
