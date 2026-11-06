---
name: workspace
description: Use an authorized, self-hosted Workspace MCP controller for distributed Go/Linux workers, versioned software environments, exclusive GPU/NPU/FPGA and opt-in USB leases, isolated Kubernetes Pod workspaces with PVCs and optional Docker compatibility, snapshots, and durable artifacts. Apply to multi-step coding, engineering, scientific computing, and hardware development when connected to Workspace MCP. A Git remote is required for authoritative source code; GitHub, GitLab, and self-hosted Git are target providers. Project Knowledge is an independent opt-in skill.
---

# Workspace

Treat the project, not the machine or chat, as the unit of work. The installed Skill does not establish a connection, grant credentials, enroll devices, or execute code. Discover live MCP tools and their authorization before starting.

1. **Resolve the project and Git authority.** List authorized projects and confirm the Git remote(s) that own their source. Code projects require at least one configured Git repository. The remote may be GitHub, GitLab, or an operator-approved self-hosted Git server; never assume those providers share APIs or authentication. For this deployment, use GitHub. An optional project board or knowledge repository is not a prerequisite. Never create an additional knowledge repository automatically.
2. **Resolve task inputs.** For code, obtain the exact authorized Git commit, ref and remote; transient workspace clones are not canonical storage. Non-code inputs can come from uploaded artifacts or explicitly approved libraries. For a linked library, use `library_resolve` and verify exact source revisions or object SHA-256. A workspace may run non-code jobs, but never represent uncommitted local source as durable project code.
3. **Choose the environment.** List environment definitions and their status. Select an approved, versioned environment matching the application/toolchain and CPU architecture. An environment is a *container image*; a workspace contains project files. On the Kubernetes backend, applications come from approved registry images; local `environment_build` and image export/import require a separately approved registry builder. Defining an image does not install applications until the matching Pod becomes ready. Proprietary software such as Vivado must be supplied by the authorized operator under its license. Never put license secrets or credentials in image layers.
4. **Reserve hardware deliberately.** Inspect `list_compute_nodes` and `list_hardware`. Request explicit GPU, NPU, or FPGA counts with `workspace_create(hardware={...})`. USB passthrough requires explicit operator permission, a user-selected worker and device ID, and `workspace_create(usb_passthrough=true, usb_node=..., usb_device_id=..., hardware={"usb":1})`. It is disabled by default. Never choose a USB target on the user's behalf or infer authorization to erase it; confirm device identity before any destructive flashing command. Treat inventory as advisory and only a confirmed workspace reservation as allocated. A missing device mapping is not permission to access host `/dev` paths. A compilation-only FPGA job need not reserve the physical board.
5. **Wait for a ready workspace.** `workspace_create` queues the start. Poll `workspace_list` until the selected workspace is ready; do not execute against a pending or failed lease. Use only its workspace-scoped operations. Node administration is a separate, explicitly authorized action.
6. **Import the exact Git revision.** Resolve a linked repository with an approved provider adapter. Use `github_checkout_repository` only for GitHub repositories when that is the available method; for another Git provider, use a verified generic Git operation if exposed, otherwise report that provider integration is unavailable. Record the resolved commit SHA. Workspace clones are ephemeral execution copies, not the authoritative repository, and must not contain controller credentials.
7. **Execute and inspect.** Use `workspace_call` for commands, reads, writes, directory listings, and search. For large binary inputs (such as OS images), use the controller's authenticated streamed artifact-upload endpoint and `workspace_import_artifact`, then verify the checksum in the workspace before acting on hardware. Poll `workspace_operation` until its terminal result. A successful operation with `exit_code != 0` is a failed command. Use `expected_sha256` to prevent blind file overwrites, and record test outcomes rather than inferring success from queued tasks.
8. **Preserve distinct artifacts.** Save key output files with `export_file`; save the workspace file tree with `workspace_snapshot`, then verify its recorded artifact. Save the application/toolchain image independently with `environment_export`. Restore workspace files by choosing an authorized snapshot; load a saved image on another node with `environment_import`. Neither operation snapshots running process memory. Inspect artifact hashes and environment versions before relying on them.
9. **Publish source to Git.** For code changes, verify a commit or reviewed branch has reached the configured authoritative Git remote, with the expected base revision checked before write; never count a local clone or an unpushed commit as durable publication. GitHub can use `github_publish_artifacts` and a reviewed PR; other hosts require their own verified Git adapter and permissions. Save and verify separate binary artifacts as requested. Do not write project notes or knowledge commits unless the separate, optional `project-knowledge` skill is explicitly enabled. Distinguish proposals, tests and verified results.
10. **Release safely.** Request `workspace_destroy` and verify confirmed termination. Hardware reservations remain held when cleanup fails or the worker disappears. Do not represent an expired lease as a safely released GPU or FPGA. Do not delete worker volumes before the required snapshot has been retained.

The controller and worker are implemented in Go. Python is an optional application inside an approved workspace image, not a service dependency. The controller currently has one privileged administrator identity. Never put its token, GitHub credentials, or host SSH keys inside worker command payloads, project notes, snapshots, or image recipes. Kubernetes namespaces, RBAC, workload admission, and Pod Security Standards require operator verification; Pod sandboxing is not a substitute for a hostile-tenant security audit. Report physical hardware integrations as unverified unless exercised on authorized devices.

## Optional knowledge workflows

Install `skills/project-knowledge` separately only when a project opts into knowledge persistence. Its modes are `none` (no intentional knowledge writes) and `git` (knowledge committed to an explicitly configured Git repository, either the source repository or another approved remote). The source-code Git remote remains mandatory independently of this choice. The controller's local note and contribution APIs are not evidence of Git persistence, and must not be invoked as an implicit fallback. Invoking compute does not automatically save knowledge.

## Cooperative claims on personal tasks

Before expensive standalone work, inspect
`personal_task_claim_list(task_id=...)`. Choose a stable lowercase
`work_key` using the exact input revision or SHA-256, objective and
method. Call `personal_task_claim`; only `claimed` authorizes
execution. A `busy` response means another agent owns the active
lease. For `completed`, verify the returned immutable task file IDs and
reuse results. Renew before expiry with the secret token and generation,
then `personal_task_claim_finish(status="completed")` with verified
file artifact IDs. If the job fails or is canceled, release the lease.
Never store its token in a repository, task file, worker, or log. Task
claims do not reserve computing resources.

## Independent personal tasks and file revisions

Use `task_list` and `task_details` to resume owner-scoped work that is not
part of a research project. Create with `task_create` and use
`task_update(expected_version=...)` to change metadata or archive tasks.
`task_save_text` and `task_send_base64` preserve immutable file revisions.
A new revision requires the previous revision number. For files larger than
512 KiB, use authenticated REST streaming with independently computed SHA-256,
and inspect `task_file_info` or download the actual verified bytes. An
authenticated personal task can be promoted to a project artifact only with
`task_export_project` and project editor authority. Do not conflate
standalone task file storage with project-scoped task claims or native ChatGPT
attachment delivery; the latter requires a separate file-capable connector.

## Cooperative project work

Before beginning a long or duplicate-prone operation, derive a stable
`work_key` from the project, exact source revision and operation. Call
`task_claim_list` and `task_claim`, then distinguish `claimed`,
`busy`, and `completed`. A busy task is not permission to repeat the
work. Renew a claim with its secret token and generation before expiry.
A completed claim returns artifact references to inspect and reuse.
After collecting and verifying outputs, call `task_claim_finish` with
`status="completed"` and project-owned artifact IDs. Use
`status="released"` if work is abandoned. Never persist the lease token
in GitHub, notes, logs, artifacts or a worker command. A task claim does
not itself allocate compute or serialize unrelated repository writes.

## GitHub App credential scope

The controller may use a repository-scoped, short-lived GitHub App installation
token instead of a static personal access token. The operator must configure
the App, installation ID, and protected RSA key on the controller. An App
installation does not make a repository eligible for use without its project
link, and its permission set remains authoritative. The controller requests
per-repository installation tokens for REST operations, while native Projects
V2 GraphQL uses the installation's selected repository visibility. Do not
place App signing keys, issued installation tokens or static PATs in worker
environments, PR text, logs, or research notes.

## GitHub development workflow

Use GitHub's live issues, branches, pull requests, reviews, and check results
as the authoritative record for development activity. Choose a linked
repository; never assume that a project or shared library grants write access.
Read the issue before updating it, and pass its expected state. Create work
branches with `github_create_branch` under `pwm/` and record their source
revision. Publish changes through a revision-guarded PR and attach the issue or
PR to the linked GitHub Project when that project tracks the work.

Use `github_mark_pull_request_ready` to advance a draft when appropriate, and
`github_verify_pull_request` to read live check results. Never represent
self-review as an independent approval: GitHub authors cannot approve their
own PRs. To merge with `github_merge_pull_request`, confirm the exact head SHA,
check that `ready_to_merge` is true, and provide `confirm=true`. The repository's
review requirements and rulesets remain authoritative. If a merge succeeds,
inspect its `cleanup` result; branch cleanup is not guaranteed by a merge.

Only merged, unchanged, unprotected branches no longer referenced by any open
PR may be deleted. Tool-created `pwm/` branches are eligible for automatic
post-merge cleanup. To delete an older manually named branch, use
`github_cleanup_merged_branch` with its merged PR, exact head SHA, and
explicit confirmation. Do not delete branches from forks or shared work.
When repository administrators want automatic cleanup for all merged branches,
configure `github_set_branch_auto_delete` intentionally and report the
provider-confirmed setting.


## Program and catalog lifecycle

Discover `pool_list_programs` before assuming a named application is
available. `pool_propose_program` records the publisher, supported OS,
distribution route and license information; it does not install software.
Administrative review uses `pool_review_program` with the current revision.
Only reviewed Linux container entries linked to an active apt collection or
OCI image can become catalog-available. Windows and macOS software requires a
separate verified native execution adapter; do not substitute Wine or an
untested virtual machine silently.

Use `pool_update_entry` with an expected revision and reason to disable or
retire a package collection or image. A disabled entry is barred from future
catalog allocation. A retired entry is a permanent tombstone. Inspect
`pool_events` for the administrative history; no catalog operation uninstalls
software from existing workspaces or deletes retained artifacts. Never
download, redistribute, or run a proprietary installer merely because a
program request cites a publisher URL.

## Encrypted recovery snapshots

The controller supports optional Restic-backed encrypted snapshots of
canonical SQLite state, referenced artifacts, research objects, and personal
task file revisions. Only controller administrators may configure
`PWM_RESTIC_REPOSITORY` and a protected
`PWM_RESTIC_PASSWORD_FILE`. The in-process interval scheduler can safely
snapshot the active Database; the standalone `backup-offsite` command must
run while the authoritative controller process is stopped. Preserve the exact
snapshot ID, verify encrypted repository integrity and perform
`restore-offsite SNAPSHOT_ID` plus `verify-offsite SNAPSHOT_ID` on an
independent recovery host. Never treat a local repository or CI test as
proof of off-site resilience. Never send repository keys to compute workers.
Recovery does not restore running processes or uncollected workspace files.

## Checkpoint-preserving workspace stop

For mutable workspaces that contain results not yet exported, use
`workspace_checkpoint_stop` rather than ordinary
`workspace_destroy`. Poll `workspace_checkpoint_stop_status` and
the named snapshot/stop operations. Only a `stopped` status with a
verified snapshot ID establishes successful checkpoint-before-stop;
an enqueued snapshot is not durable evidence. A failed checkpoint
does not authorize teardown and can be retried while the workspace
lease remains valid. An expired workspace may still require cleanup,
so checkpoint well before expiry. Restore the recorded snapshot to a
separate ready workspace in the same project, check the terminal
restore result, and verify the files. Do not confuse a controller-local
checkpoint with an independently restorable off-host backup.

## Advisory resource planning

Call `resource_plan` before a resource-intensive workspace. Supply CPU,
memory, image or ready environment, architecture, GPU/NPU/FPGA counts,
an exact USB device if requested, and `min_gpu_memory_mb` where relevant.
Read all candidate exclusions; prefer a non-accelerated node for CPU-only
jobs. A feasible plan is advisory, not a scheduler reservation. Pass an
eligible `node_id` to `workspace_create` only when a specific node is
required. The controller rechecks resources and device leases before
allocation. Treat operator-advertised GPU VRAM as a total hardware property,
not a live free-memory reading. Unknown application licenses, missing
native desktop adapters, and external-cluster quotas cannot be satisfied
by an ordinary Docker candidate.

## Shared image and software catalogs

Before reserving a worker, call `pool_list_images` to discover images approved
by operators and their currently eligible online worker nodes. Select the
`image_pool_id` in `workspace_create` when using a shared image; a catalog
entry alone does not install an image. `pool_list_software` retrieves curated
apt package sets. Compose a definition with `environment_define_from_pool`
and explicitly build it on an authorized worker. Only the owner should
register image and software entries. Proprietary tools belong in
operator-approved images, subject to installation and license restrictions.

For files up to 512 KiB, use `artifact_send_base64` with an independently
computed SHA-256, and `artifact_read_base64` for verified retrieval. Larger
files require authenticated streamed REST transfer, followed by
`workspace_import_artifact`. These tools do not themselves attach a file
uploaded in the chat UI; that requires file-capable connector mediation.

## Project-scoped OAuth access

Discover whether an OIDC issuer is configured before assuming that any
non-owner bearer token will be accepted. The OAuth authorization server issues
and revokes credentials; this Go controller verifies those tokens and
maintains per-project grants. Obtain the current caller's stable subject
through `GET /api/whoami`. The controller owner can grant and revoke
`viewer`, `editor`, and `owner` roles using the project's `/permissions`
endpoint, with the previous role as an expected-state guard. Never send the
static owner credential or external authorization tokens into workspaces.
Worker credentials are separate. A successful token validation is not evidence
of authorization to work in a particular project. Privileged GitHub writes
remain owner-only, and OAuth does not bypass repository rules or hardware
allocation safeguards.
