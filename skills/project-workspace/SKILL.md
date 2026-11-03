---
name: project-workspace
description: Use an authorized, self-hosted Project Workspace MCP controller to work on durable projects through linked GitHub repositories, distributed Go/Linux workers, versioned software environments, exclusive GPU/NPU/FPGA and opt-in USB leases, interactive Docker workspaces, snapshots, and durable artifacts. Apply to multi-step coding, research, scientific computing, and hardware development when the user's Project Workspace MCP tools are connected.
---

# Project Workspace

Treat the project, not the machine or chat, as the unit of work. The installed Skill does not establish a connection, grant credentials, enroll devices, or execute code. Discover live MCP tools and their authorization before starting.

1. **Resolve the project.** List projects and their repository memberships, including roles and preferred refs. When `github_project_board` has a linked board, read planning items from GitHub Projects V2 with `github_project_items` and follow cursors; discover selectable fields with `github_project_fields`; never substitute old local task notes for a live GitHub planning status. Use `github_project_add_item` only for an explicitly linked repository, and update a Project field by its verified item, field, and option IDs. Projects without GitHub remain valid. Retrieve versioned notes and search project knowledge before repeating work.
2. **Resolve shared libraries.** List the catalog with `library_list`, then use `library_resolve` only for libraries explicitly linked to the project. Library sources are pinned to immutable Git revisions and can span several GitHub repositories. Read source files from their pinned revisions, not arbitrary moving branches. Large binary datasets and compiled Lean caches live in the content-addressed library object store; import through `library_import_object` or retrieve a pinned source using `library_checkout_source`. A library link does not grant generic GitHub repository write access.
3. **Choose the environment.** List environment definitions and their status. Select an approved, versioned environment matching the application/toolchain and CPU architecture. An environment is a *container image*; a workspace contains project files. Defining a Dockerfile does not install anything until `environment_build` reports completion. Proprietary software such as Vivado must be supplied by the authorized operator under its license. Never put license secrets or credentials in image layers.
4. **Reserve hardware deliberately.** Inspect `list_compute_nodes` and `list_hardware`. Request explicit GPU, NPU, or FPGA counts with `workspace_create(hardware={...})`. USB passthrough requires explicit operator permission, a user-selected worker and device ID, and `workspace_create(usb_passthrough=true, usb_node=..., usb_device_id=..., hardware={"usb":1})`. It is disabled by default. Never choose a USB target on the user's behalf or infer authorization to erase it; confirm device identity before any destructive flashing command. Treat inventory as advisory and only a confirmed workspace reservation as allocated. A missing device mapping is not permission to access host `/dev` paths. A compilation-only FPGA job need not reserve the physical board.
5. **Wait for a ready workspace.** `workspace_create` queues the start. Poll `workspace_list` until the selected workspace is ready; do not execute against a pending or failed lease. Use only its workspace-scoped operations. Node administration is a separate, explicitly authorized action.
6. **Checkout exact repository revisions.** Invoke `github_checkout_repository` only for linked repositories and wait for the import operation result. Record the exact resolved commit SHA. Repositories are placed under `/workspace/repos/OWNER__REPO`; they do not contain controller GitHub credentials.
7. **Execute and inspect.** Use `workspace_call` for commands, reads, writes, directory listings, and search. For large binary inputs (such as OS images), use the controller's authenticated streamed artifact-upload endpoint and `workspace_import_artifact`, then verify the checksum in the workspace before acting on hardware. Poll `workspace_operation` until its terminal result. A successful operation with `exit_code != 0` is a failed command. Use `expected_sha256` to prevent blind file overwrites, and record test outcomes rather than inferring success from queued tasks.
8. **Preserve distinct artifacts.** Save key output files with `export_file`; save the workspace file tree with `workspace_snapshot`, then verify its recorded artifact. Save the application/toolchain image independently with `environment_export`. Restore workspace files by choosing an authorized snapshot; load a saved image on another node with `environment_import`. Neither operation snapshots running process memory. Inspect artifact hashes and environment versions before relying on them.
9. **Publish and record.** Create a revision-guarded draft PR with `github_publish_artifacts` using the exact expected base SHA. Confirm provider evidence; never claim publication or merge based only on an enqueued request. Write project notes with `expected_version`, and reconcile conflicts. Separate proposals, measurements, and verified results.
10. **Release safely.** Request `workspace_destroy` and verify confirmed termination. Hardware reservations remain held when cleanup fails or the worker disappears. Do not represent an expired lease as a safely released GPU or FPGA. Do not delete worker volumes before the required snapshot has been retained.

The controller and worker are implemented in Go. Python is an optional application inside an approved workspace image, not a service dependency. The controller currently has one privileged administrator identity. Never put its token, GitHub credentials, or host SSH keys inside worker command payloads, project notes, snapshots, or image recipes. Docker is not a hostile-tenant security boundary. Report physical hardware integrations as unverified unless exercised on authorized devices.

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
