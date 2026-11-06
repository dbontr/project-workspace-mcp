# Project Workspace MCP Operations

Use this reference for live Project Workspace MCP operations. The controller owns project metadata, task coordination, worker leases and artifact routing; Git remains authoritative for code and opt-in project knowledge. Tool names identify capabilities, not permission grants. Inspect the connected server's `tools/list` and schemas before invoking a tool; versions and available adapters can differ.

## Establish identity and scope

1. Confirm the authenticated caller and Project Workspace server, relevant project IDs, project grants, linked repositories and active resource inventory. Controller owner access does not automatically establish provider permissions or hardware readiness.
2. Choose **project workspace**, **project compute job**, **independent personal task**, or **explicitly reserved external resource**. Never invent a project just to obtain compute. A personal task does not inherit a project's repositories or knowledge policy.
3. For source work, discover `git_provider_capabilities` and the actual repository membership. Use the provider-neutral `git_*` operations for supported GitHub or operator-approved `local:OWNER/REPOSITORY` identities; do not call `github_*` on local Git.
4. Keep the controller credential, GitHub PAT/App signing key, OIDC tokens, worker pairing tokens and private certificates outside repository contents, worker commands, generated artifacts, logs and messages. Project viewer/editor authorization is narrower than owner authority.

## Project and Git operations

- Use `list_projects`, repository membership tools and `git_link_repository` only after checking project authority. Code source must be a real Git repository with an explicit authoritative branch.
- Use `git_repository_head` for the exact commit, `git_read_file` for authorized content, `git_checkout_repository` for a pinned workspace import and `git_publish_file` with the expected head and explicit confirmation for supported text publication. Treat a mismatch as a conflict, not as permission to overwrite.
- GitHub repository account discovery and administration are separate, owner-authorized operations. Before `github_account_rename` or `github_account_transfer`, verify immutable repository ID, destination, administrator permission and exact confirmation. Reconciliation can be incomplete when an external provider transfer awaits acceptance; use `github_account_reconcile` only after the provider independently confirms identity.
- A local Git repository has commits and branches, not native GitHub Issues, pull requests or Projects. A separate, explicitly linked GitHub `planning` repository may own those objects. A planning-repository PR does not review code that exists only in local Git. See [github-workflow.md](github-workflow.md).

## Project workspaces

1. Inspect `resource_plan`, `list_compute_nodes`, `list_hardware`, `pool_list_images`, `environment_list` and available quotas. A favorable plan is advisory; it is **not** a hardware lease.
2. Select an operator-approved OCI image and architecture; record its digest and toolchain versions when available. An environment definition is not a running workspace. `environment_define_from_pool` and `environment_build` require the configured backend; proprietary toolchains require valid operator licensing.
3. Use `workspace_create` with the smallest adequate CPU, memory, duration and explicit hardware requirements. Request GPU/NPU/FPGA counts only as needed. USB passthrough requires a deliberate node and device choice plus authorization; do not infer permission from a visible host path.
4. Poll `workspace_list` until the exact workspace is ready. Then import the pinned Git revision or verified artifact. `workspace_call` starts a scoped operation; use `workspace_operation` to obtain the terminal result and exit code. An accepted or queued command has not necessarily executed successfully.
5. Publish code to the designated Git authority and collect output through `list_project_artifacts`, `artifact_info`, `workspace_snapshot`, or `environment_export` as appropriate. A saved file tree does not preserve live RAM, connected peripherals or running processes.
6. Before release, verify the artifact hash and external publication. Use `workspace_checkpoint_stop` and `workspace_checkpoint_stop_status` when mutable files require preservation. Use `workspace_destroy` only when destructive consequences are understood, then verify actual teardown; uncertain hardware leases remain occupied.

For smaller files, `artifact_send_base64`, `artifact_read_base64` and `workspace_import_artifact` use authenticated transfer and SHA-256 validation. Large binary files require the separately authorized streaming endpoint. A file mentioned in a chat is **not** automatically available to the MCP; a file-capable client must explicitly deliver the actual bytes.

## Cooperative project claims

- Before expensive duplicate-prone project work, choose a deterministic `work_key` from project ID, exact source revision, objective and method.
- Inspect `task_claim_list` or `task_claim_status`, then use `task_claim`. Execute only on a genuine `claimed` result. A `busy` response means another agent owns the lease; for `completed`, inspect the referenced immutable output.
- Renew active claims with `task_claim_renew` before expiration, using the correct token and generation. When verifiably complete, pass real project artifact IDs to `task_claim_finish`; release a failed or abandoned claim. Never persist claim tokens.
- A cooperative claim prevents duplicate work among compliant clients. It is not a physical device lease, repository write lock, production test or proof of execution.

## Independent personal tasks

- Use `task_create`, `task_list`, `task_details` and revision-guarded `task_update` for owner-scoped tasks, not project knowledge.
- Use `personal_task_claim_list`, `personal_task_claim`, `personal_task_claim_renew` and `personal_task_claim_finish` for independent cooperative claims. Confirm the generation and verify saved task file revisions before completing a claim.
- `task_save_text`, `task_send_base64`, `task_read_base64` and `task_file_info` retain explicit personal task files with revision/digest checks. For larger files, use authenticated REST streaming. `task_export_project` is an explicit promotion requiring project editor authorization; do not silently synthesize project records.
- `task_compute_submit` queues a **finite one-shot job**. An administrator must first authorize its worker through `task_worker_authorize`; inspect `task_worker_access`. On Kubernetes, the worker must belong to the isolated `personal-tasks` scope. Poll `task_compute_jobs` for real terminal results.
- Finite jobs may request bounded GPU/NPU/FPGA quantities and minimum GPU memory if supported by the live worker. They use exclusive controller leases and approved Docker device mappings or Kubernetes extended resources. Standalone USB passthrough and **interactive project-free workspaces are not available** in this interface. Do not substitute an unrequested project.
- A failed or expired hardware job does not prove a device is free. `task_hardware_recover` requires independently verified cleanup and administrator confirmation. Never claim successful release solely because a lease timed out.

## External machines, EDA and independent clusters

- `external_resource_list`, `external_resource_register`, `external_resource_reserve`, `external_resource_leases` and `external_resource_release` record separately controlled resources outside the ordinary cluster pool.
- A local `pending_external_adapter` entry is **not** an authenticated SSH session, Slurm allocation, EDA guest, remote execution or released FPGA. Require the real adapter's verified identity, allocation ID, capability, credential boundary, job status and teardown evidence before reporting execution or release.
- Do not join external hardware to the general Kubernetes pool merely because it is discoverable; use it only after the user explicitly identifies and authorizes the external resource.

## Research and shared libraries

- Use `library_create`, `library_link`, `library_resolve`, `library_add_source` and `library_read_file` for intentionally shared sources. Only read libraries actually linked to the authorized project; retain pinned Git revisions and original object digests.
- `research_register_source`, `research_ingest_passages`, `research_search`, `research_topics` and `research_read_passage` operate on a source-linked index. Index metadata, derived passages and FTS search results are not proof that an original PDF or claim was verified. Check the source document and stable locator before quotation.
- `library_attachment_send` and `study_pack_prepare` accept actual verified attachment bytes or prepared source references only through supported client transport. Do not invent page numbers, transcript text or document contents.
- The optional Git knowledge policy is separate: `knowledge_policy` defaults to `none`; only an explicitly configured `git` policy permits `knowledge_git_publish`. Use `knowledge_git_read` and revision-guarded publication under the chosen prefix. Historical local note and contribution APIs are read-only migration sources, not a new persistence destination. Follow [knowledge-standard.md](knowledge-standard.md) for record structure and publication.

## Provider authentication, catalog and recovery

- OAuth/OIDC proves identity; project roles and GitHub provider permissions still determine authorization. Worker pairing tokens never replace project grants. Use the project's permission endpoint only with owner authority and expected-role safeguards.
- `pool_register_image`, `pool_register_software`, `pool_propose_program`, `pool_review_program` and `pool_update_entry` are administrator catalog workflows. Proposal is not installation; an approved image or software collection is not available until its worker distribution/build is verified. Respect software licenses and native OS constraints.
- Encrypted Restic backups preserve controller state, referenced artifacts, research objects and selected task files when configured. A controller-local snapshot is not an independent off-host restore. Confirm repository integrity and restore on an independent recovery host before declaring resilience.
- `workspace_prune_volume` and destructive artifact deletion require direct owner authorization and proof that snapshots, source publication and external dependencies are preserved.

## Verification record

For any material operation, report only observed evidence: project/task ID; exact source repository and commit; authorization scope; image/toolchain; worker/device and lease; input/output artifact hashes; command exit code or provider terminal state; versioned knowledge destination if enabled; GitHub issue/PR/board state if applicable; and precise open risks.

Use [cluster-and-delivery.md](cluster-and-delivery.md) for deployment, safety and acceptance gates. Treat controller tool availability, CI success, real cluster execution and hardware acceptance as distinct conditions.
