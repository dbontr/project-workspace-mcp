# Cluster Operations and Delivery

Use this reference for Project Workspace MCP, Kubernetes, external systems, artifacts and acceptance. The controller is the authority for leased compute; Git remains the authority for code; knowledge and large binary storage are independently selected.

## Admission and resource selection

1. Discover current controllers, worker connectivity, cluster namespace, allowed OCI images, device-plugin resources, capability manifests, access grants and quota. Distinguish registered from healthy, available from leased and implemented from proposed.
2. Choose the execution scope: finite standalone task, project workspace, shared library import, or explicitly reserved external machine. A standalone task never gains project knowledge implicitly. Kubernetes execution should use the project's authorized namespace and restricted service account.
3. Request CPU, memory, storage and job timeout. Request GPU/NPU/FPGA only when essential. Physical devices require valid exclusive leases; GPU VRAM needs actual advertised capacity. A compiler simulation is not proof of physical board access.
4. Select an exact approved OCI image. Prefer immutable image digests and record toolchain/driver versions. Proprietary FPGA/EDA toolchains require operator-supplied licensed images; never copy licenses or credentials into logs, Git, or arbitrary Pods.
5. Treat USB passthrough, flashing, disk partitioning, firmware changes and host configuration changes as privileged operations. Require exact device/node identity and explicit authorization; never infer permission from mere discoverability.
6. Preserve the existing network/security posture. Do not route through unrestricted host paths, weaken Kubernetes RBAC, attach broad host namespaces or disable Cloudflare/VPN controls to satisfy an operation.

## Project workspaces

Use this repository's [MCP operations reference](mcp-operations.md) and the live MCP schemas for exact tool arguments. Wait for the workspace to report ready. Import code at a verified Git commit and check artifact digests. A remote asynchronous operation is not complete until its own terminal status, exit code and output are inspected.

Before a destructive or costly job, confirm inputs, target, expected side effects, recovery path and capacity. Keep builds, temporary clones and caches inside the workspace; publish source to the Git authority and retained outputs to the approved artifact store. A snapshot of files does not save live memory or prove external board state.

## Finite tasks and accelerator leases

Standalone finite tasks use an administrator-approved worker. A worker's reported capabilities do not grant authorization. For Kubernetes use the explicitly isolated personal-task scope; for hardware, request only controller-approved GPU/NPU/FPGA device-plugin resources. USB access is not a generic finite-job capability.

An expired lease says only that communication or time expired. It does not establish that the process released the device. Keep uncertain reservations unavailable until remote teardown has been independently verified and the administrator explicitly authorizes recovery. Project workspaces and independent jobs must not claim the same device simultaneously.

## External clusters and laboratory hardware

Keep named external clusters, standalone servers, EDA guests, FPGA boards and remote Slurm partitions outside the free resource pool. A registry record or a `pending_external_adapter` reservation is an **intention**, not a real SSH session, Slurm allocation, device lease or verified job. Before claiming an external operation:

- Verify the adapter's protocol and read/write authority, exact host identity, credential scope and network policy.
- Obtain a provider-confirmed allocation or session ID; test execution on the actual target.
- Record job ID, resource allocation, return status, data transfer hashes and cleanup result.
- Confirm release from the external scheduler or host; do not infer release from a controller timeout.

Do not promise to execute work on a board, foreign cluster or cloud account when no authorized live adapter is available.

## Research, builds and artifact provenance

Record for reproducibility: project and task IDs, source commit, immutable image reference, toolchain and relevant driver versions, node/accelerator identity, input artifact IDs/SHAs, environment constraints, command and parameters, outputs/digests, observed timing and exit codes. Redact secrets, tokens, personal data and restricted licensed materials. Keep large logs/artifacts out of Git; link verified immutable storage references in a versioned knowledge record only when the knowledge policy permits it.

## Acceptance and recovery

A build can pass while the deployment fails. Distinguish these gates:

- **Source verification:** formatter, compile/typecheck, unit and race tests, static analysis, dependency policy and diff audit.
- **Integration verification:** authentication, role isolation, conflict handling, concurrent leases, storage integrity, rollback and migration against realistic adapters.
- **Runtime verification:** image built and pulled, correct live deployment, readiness and liveness checks, real resource acquisition, successful command/physical transfer and release.
- **Durability verification:** remote Git read-back, artifact hash read-back, off-host backup and an independent restore drill when required.
- **Publication verification:** correct PR/commit, protected-branch checks, approved merge policy and exact deployed image/source identity.

For failed CI or unavailable logs, identify the gap and run authorized alternative checks. Do not label the failure environmental without evidence. Do not claim production acceptance from a draft PR, mock-only test, successful push or merely queued Kubernetes operation.

## Handoff contract

Write a compact handoff only when the project has enabled Git knowledge or the user explicitly requests a portable handoff artifact. Include: objective, current authoritative source SHA, completed/verified work, incomplete tests, active resources and lease IDs, hashes of retained artifacts, known hazards, blockers, next concrete command or decision and exact references. Never include passwords, chats, discarded proposals or device assumptions.

## Sources

- [Kubernetes device plugins](https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/device-plugins/)
- [Kubernetes resource quotas](https://kubernetes.io/docs/concepts/policy/resource-quotas/)
- [Kubernetes jobs](https://kubernetes.io/docs/concepts/workloads/controllers/job/)
- [Kubernetes persistent volumes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/)
- [Kubernetes RBAC](https://kubernetes.io/docs/reference/access-authn-authz/rbac/)
