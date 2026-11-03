# Project Workspace MCP

A self-hosted, project-aware distributed computing system for MCP clients. Its Go controller retains projects, repository associations, knowledge, durable artifacts, environment definitions, hardware leases, jobs, and execution history. Linux Go workers run isolated Docker workspaces on allocated compute nodes. The controller and worker require **no Python interpreter or Python application server**. Python, Vivado, C++, and other applications can be installed within approved workspace images.

The controller uses SQLite and the system SQLite library through Go's CGO interface. Existing Project Workspace SQLite databases retain their schema and records. Do not run two authoritative controllers against the same database simultaneously. Back up `state.db` together with the sibling `artifacts/` directory before migrating a deployed instance.

## Requirements

- Go 1.23 or later at build time, and a C compiler plus SQLite development library for the controller (`pkg-config sqlite3`). Prebuilt binaries do not require a Go compiler or Python installation.
- Linux and Docker on each execution worker. The controller can run on supported hosts that provide SQLite and a C compiler at build time.
- A trusted MCP client. The HTTP MCP endpoint implements stateless Streamable HTTP with the 2025-06-18 protocol, including `initialize`, `tools/list`, and `tools/call`.
- An HTTPS ingress for remote connections; the controller listens on loopback by default. Authentication currently uses a single owner credential, not OAuth or project-specific multi-user authorization.

## Install and start

```sh
./scripts/install.sh
~/.local/bin/project-workspace-controller init
~/.local/bin/project-workspace-controller controller
```

To build without the script:

```sh
CGO_ENABLED=1 go build -o project-workspace-controller ./cmd/project-workspace-controller
go build -o project-workspace-worker ./cmd/project-workspace-worker
./project-workspace-controller init
./project-workspace-controller controller
```

`init` generates an owner-only credential at `~/.config/project-workspace/admin.token`. The server listens at `http://127.0.0.1:8000` and stores its data in `~/.local/share/project-workspace/state.db` unless `PWM_DB` overrides the path. MCP is at `/mcp/`; the local operator dashboard is `/dashboard`. Use `PWM_ADMIN_TOKEN` to supply a credential from a secrets manager without a token file. Never share this credential with an untrusted client. For a reverse proxy, set `PWM_ALLOWED_HOSTS` and `PWM_ALLOWED_ORIGINS` to the intended public host and browser origins. `PWM_ALLOW_REMOTE_LISTEN=1` permits binding away from loopback only when the operator provides a protected TLS ingress.

The server's configuration variables include `PWM_GITHUB_TOKEN`, `PWM_GITHUB_GENERIC_WRITES=1` (opt-in repository mutations), and `PWM_MAX_ARTIFACT_BYTES` (default 2 GiB).

## Pair a compute node

On the controller:

```sh
project-workspace-controller pair
```

On a trusted Linux worker:

```sh
project-workspace-worker doctor
project-workspace-worker join --controller https://controller.example --node worker-one --code PAIRING_CODE
project-workspace-worker serve --controller https://controller.example --node worker-one
```

A pairing code is single-use and expires after ten minutes. Each worker has a distinct, revocable credential. The worker contacts the controller over outbound HTTPS and claims work with expiring fenced tokens. Intermittent network failures use bounded retries; failed cleanup never releases a leased device prematurely.

## Projects, workspaces, and environments

A project owns knowledge, linked GitHub repositories, artifacts, and execution history. An environment describes a versioned OCI image containing applications and dependencies. A workspace contains mutable project files. Hardware allocations are separate exclusive leases. These objects are not interchangeable.

The default workspace image is `debian:bookworm-slim`, with the worker's `PWM_ALLOWED_IMAGES` controlling approved images. Use a Python image or an administrator-built Vivado image explicitly when needed. Define a named environment with a Dockerfile or a previously approved base image, run `environment_build`, and wait for the operation to succeed. The `environment_export` and `environment_import` tools transfer saved images; `workspace_snapshot` and `workspace_restore` save and restore files independently. Neither operation snapshots running process memory.

To create a project and workspace over REST:

```sh
TOKEN=$(cat ~/.config/project-workspace/admin.token)
BASE=http://127.0.0.1:8000
curl -fsS -X POST "$BASE/api/projects" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"id":"research","title":"Research"}'
curl -fsS -X POST "$BASE/api/workspaces" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"project_id":"research","cpu":2,"memory_mb":4096}'
```

Workspace creation queues a `start` operation; it is not synchronous. Poll `GET /api/projects/research/workspaces` until the workspace is `ready`. Use `workspace_call` to run commands and edit files; poll `workspace_operation` for worker-confirmed results. `exit_code != 0` means a command failed even when the operation itself completed. Export important artifacts before stopping the workspace. Named volumes are retained for deliberate recovery and cleanup.

## GPU, NPU, FPGA, and USB access

A node's operator supplies an allowlisted device manifest. The controller never accepts a host device path from an AI tool. Explicit leases reserve whole device identifiers for one workspace at a time. GPU access uses an administrator-configured CDI identifier; vendor FPGA/NPU hardware can use approved character devices and vendor runtime images.

USB access is **off by default**. The node operator enables it using `project-workspace-worker serve --enable-usb`, and the workspace must separately request `usb_passthrough=true`, `usb_node=<exact worker>`, and `usb_device_id=<exact device>`. The worker validates stable USB disk aliases, detects mounted or mapped storage, and rejects unsafe device paths. A hardware lease remains active until confirmed cleanup. The AI must not choose an arbitrary disk or infer authorization to erase one. Flashing an OS image is destructive: the intended USB device must be independently verified before issuing any write command.

Large OS images can be streamed to `PUT /api/projects/PROJECT/artifacts/upload` with `X-Artifact-Name`, `X-Content-SHA256`, and the owner bearer credential. Use `POST /api/projects/PROJECT/workspaces/WORKSPACE/artifacts/import` to stage the verified artifact in a workspace. The artifact path is relative to the workspace, and imports refuse unintended overwrites.

## Approved images and software packages

The controller has an administrator-managed image and software pool. A pool
entry does **not** automatically install an image, authorize a worker, or
grant access to hardware. Workers report the exact OCI references approved by
their local `PWM_ALLOWED_IMAGES` setting; `pool_list_images` displays
the currently eligible online workers. Images in the pool can be used in
`workspace_create(image_pool_id="...")`, which refuses to schedule a worker
that has not explicitly approved the selected image. Ordinary direct image
and versioned environment options remain supported.

```sh
curl -fsS -X POST "$BASE/api/pool/images" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"id":"debian-bookworm","image":"debian:bookworm-slim","description":"Debian base"}'
curl -fsS "$BASE/api/pool/images" -H "Authorization: Bearer $TOKEN"
```

The software catalog contains immutable, curated apt package sets associated
with one Debian or Ubuntu base image. It never accepts shell commands as
package names. Non-apt toolchains and licensed applications should instead
be provided as approved, versioned OCI images. Register packages and compose
an environment before requesting a worker build:

```sh
curl -fsS -X POST "$BASE/api/pool/software" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"id":"c-toolchain","image_id":"debian-bookworm","packages":["gcc","make"]}'
curl -fsS -X POST "$BASE/api/projects/research/environments/from-pool" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"c-build","image_id":"debian-bookworm","software_ids":["c-toolchain"],"expected_version":0}'
```

Then start a base-image workspace, call `environment_build`, await
worker-confirmed success, and start a workspace selecting the ready environment.
The build requires network access (`PWM_BUILD_NETWORK=1` on the authorized
worker) or an accessible local apt cache. Builds do not bypass image or
hardware policy, provide a private registry, redistribute proprietary software,
or guarantee availability of packages in upstream repositories.

### File transfer over MCP

The `artifact_send_base64` tool accepts `project_id`, `name`,
`sha256`, and `data_base64` for files up to **512 KiB**. The controller
verifies the digest before persisting them. `artifact_info` reads metadata
and `artifact_read_base64` retrieves a verified small file. Use
`workspace_import_artifact` to stage it in a ready worker, or export a worker
file as a durable artifact. These operations are project-scoped and require
the existing owner credential.

For larger PDFs, images, notebooks, datasets, and OS images, use the
authenticated streamed REST upload `PUT /api/projects/PROJECT/artifacts/upload`
and download `GET /api/projects/PROJECT/artifacts/ARTIFACT_ID`.
The content hash is mandatory. The current MCP attachment tool takes base64
text; an integration that converts ChatGPT's native file attachments to this
transport requires a separate client-side file-capable connector. It is not
automatically established by installing this repository.

## GitHub and persistent knowledge

Associate each GitHub repository with its project using `link_github_repository`. GitHub credentials stay exclusively on the controller; workers receive immutable archives rather than repository tokens. MCP tools support repository information, issue and PR lists, GitHub file reads, checkout by immutable revision, and revision-guarded draft PR publication. Extended repository REST actions are disabled for writes until the administrator explicitly enables `PWM_GITHUB_GENERIC_WRITES=1`.

Project notes use `expected_version` to reject conflicting writes. Search, artifacts, and snapshots are project-scoped. The controller retains the established SQLite table layout so existing records can be opened without a separate conversion script.

## Verification

```sh
go test ./...
go test -race ./internal/controller ./internal/worker
go vet ./...
```

GitHub Actions runs Go unit and protocol tests, cross-architecture builds, and an optional Docker controller/worker integration. The latter requires Docker and a `python:3.12-slim` test image **inside the container only**; no Python interpreter runs on the controller or worker host.

## Offline recovery snapshots

Stop the controller and all processes that write to its data directory before
backing up or restoring it. The snapshot includes a transactionally consistent
SQLite copy, referenced project artifacts, and referenced content-addressed
library objects. It verifies SHA-256 hashes, detects missing files, and refuses
backups while resumable library uploads remain pending. Unreferenced files and
unfinished uploads are not part of the snapshot.

```sh
project-workspace-controller backup /mnt/off-host/project-workspace-snapshot
```

The destination must not exist. Store the completed snapshot on independent
storage, with restricted access and encryption appropriate to the sensitivity
of project data. A backup left on the same disk is not an off-host backup.
Run recovery on a separate machine or clean data directory:

```sh
PWM_DB=/srv/project-workspace/state.db \
  project-workspace-controller restore /mnt/off-host/project-workspace-snapshot
```

Restore verifies every listed file before writing and refuses to overwrite an
existing database or artifact directory. It does not restore active processes,
worker leases, external GitHub repositories, or external hardware. Reconfigure
controller credentials separately and confirm that workers can be paired again.
Run a full restore exercise on another host before treating backups as reliable.
The commands are offline maintenance operations, not an automatic backup
scheduler or an online multi-controller failover mechanism.

## Operating limits

This is a single-owner system intended for trusted machines. It does not provide production OAuth, fine-grained multi-user project grants, VM-grade hostile-tenant isolation, automatic off-host database backup/restoration, or verified real GPU/NPU/FPGA/USB hardware passthrough. Those features require separate acceptance before untrusted users or destructive hardware work can be enabled. Proprietary applications such as Vivado are not redistributed by this project. An MCP tool call is not an autonomous reasoning agent.

## GitHub Projects planning and shared research libraries

Project Workspace can use a native GitHub Projects V2 board as the authoritative
planning record. Local projects remain usable without GitHub. GitHub planning
items, custom fields, and the Project README are read live from GitHub; the
controller stores only the verified Project association. The linked board may
contain issues and pull requests from repositories with different owners.

Configure `PWM_GITHUB_TOKEN` **on the controller only**. Its GitHub identity
needs `read:project` for reading Projects or `project` for updating them, as
well as the required repository permissions. To link an existing board by its
ordinary GitHub URL:

```sh
curl -fsS -X POST "$BASE/api/projects/research/planning" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"url":"https://github.com/users/OWNER/projects/1"}'
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/projects/research/planning/items"
```

The `github_project_items` MCP tool returns a page of at most 50 live issues,
pull requests, and drafts, including status and other custom fields. Use the
returned cursor to request further pages. `github_project_fields` retrieves the
Project's field IDs and single-select option IDs, including `Status`, and
supports pagination. `github_project_add_item` adds a
linked repository's issue or PR. `github_project_add_draft` creates planning
ideas without inventing a repository issue. `github_project_set_status` sets
an explicitly selected single-select field option. GitHub is the source of
truth; no separate planning-item database is maintained. The dashboard also
provides a GitHub Project URL connection form.

A project may link multiple repositories, including repositories owned by
other accessible organizations. Each association has a role (`source`,
`research`, `lean`, `hardware`, or `documentation`) and an optional preferred
ref. Existing `link_github_repository` calls retain the `source` default:

```sh
curl -fsS -X POST "$BASE/api/projects/research/repositories" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"repository":"OWNER/lean-foundations","role":"lean","default_ref":"main"}'
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/projects/research/repository-memberships"
```

### Shared Lean, research, and data libraries

Libraries are independent of any one project. Each library may reference
several repositories at **immutable commit hashes**, while any number of
projects can reference the same library. Referencing a library does **not**
automatically grant unrestricted GitHub repository write permissions. The
library catalog supports Lean formalizations, literature, software, and mixed
research collections.

```sh
curl -fsS -X POST "$BASE/api/libraries" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"id":"lean-foundations","title":"Lean Foundations","kind":"lean","description":"Shared mathematical formalizations"}'
curl -fsS -X POST "$BASE/api/libraries/lean-foundations/sources" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"repository":"OWNER/lean-theory","ref":"main","path":"Math","kind":"formalization"}'
curl -fsS -X POST "$BASE/api/projects/research/libraries" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"library":"lean-foundations"}'
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/projects/research/libraries/lean-foundations/resolve"
```

The source registration resolves `main` to a specific commit at registration
time. To use another revision, explicitly register it again. Source checkouts
stream the GitHub archive to the controller instead of buffering it in memory,
and queue an import into an authorized workspace via `library_checkout_source`.
The same mechanism supports ordinary linked-repository checkouts. Repositories
remain subject to GitHub's own size limits; splitting a large Lean source tree
into independent Lake packages is preferable to keeping an enormous Git
history in one repository.

Compiled `.olean` files, caches, datasets, PDFs, and other large binary objects
belong in the controller's **content-addressed library object store**, not in
Git history. Each object is stored under its SHA-256 digest and can be
referenced from multiple libraries without duplicating its bytes. Names are
immutable; changing an object means creating a different name or library
revision. To register a large artifact (the hash shown must be calculated from
its actual bytes):

```sh
export PWM_MAX_ARTIFACT_BYTES=107374182400  # maximum single upload: 100 GiB
sha256sum lean-cache.tar.zst
curl -fS -X PUT "$BASE/api/libraries/lean-foundations/objects/cache-2026.tar.zst" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Content-SHA256: REPLACE_WITH_SHA256" \
  -H 'X-Object-Kind: build-cache' \
  -H 'Content-Type: application/octet-stream' \
  --data-binary @lean-cache.tar.zst
```

For connections with restrictive upload limits or unstable networks, use
resumable uploads in chunks of at most **8 MiB**. The controller stores the
upload cursor in SQLite, so an interrupted client can query its accepted byte
offset and continue without starting over:

```sh
# The SHA-256 digest must describe the entire source file.
SHA=$(sha256sum lean-cache.tar.zst | cut -d ' ' -f1)
SIZE=$(wc -c < lean-cache.tar.zst | tr -d ' ')
curl -fsS -X POST "$BASE/api/libraries/lean-foundations/uploads" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"name\":\"cache-2026.tar.zst\",\"kind\":\"build-cache\",\"sha256\":\"$SHA\",\"size\":$SIZE}"
```

The response supplies `upload_id`, `received_bytes`, and `chunk_size`.
Upload consecutive binary chunks with `PUT /api/libraries/lean-foundations/uploads/UPLOAD_ID`
and the `X-Upload-Offset` header set to the exact byte position. Read
`GET /api/libraries/lean-foundations/uploads/UPLOAD_ID` after a disconnect;
complete with `POST .../uploads/UPLOAD_ID/complete` once all bytes are accepted.
The controller verifies both total size and SHA-256 before the object becomes
visible. Incomplete sessions expire after 24 hours and can be cancelled with
`DELETE .../uploads/UPLOAD_ID`. A compatible client must not skip offsets or
send more than 8 MiB in one request.

`library_import_object` queues a shared object into a project's ready
workspace. The controller creates an authorized hard link within its artifact
store; it does not copy the entire binary for every linked project. The worker
retrieves the bytes through its existing authenticated, digest-checked artifact
transfer and refuses accidental overwrites. The object store is limited by
available persistent disk and the configured per-upload maximum, not by
GitHub's repository guidelines. It still needs an independently tested,
off-host backup and storage-capacity monitoring. **It is not an off-host
backup solution.**

You can keep private source repositories and public planning boards separate,
but do not publish confidential research in Project items. Owner-token access
is still required for all library operations; a production multi-user
permission system is not yet implemented.

## GitHub issues, branches, and pull-request lifecycle

Each project can link multiple GitHub repositories. All GitHub operations act on
an explicitly linked repository; repository credentials remain on the Go
controller. GitHub Projects V2 remains the planning authority. Issues, commits,
reviews, checks, merges, and branch refs remain authoritative on GitHub.

The MCP tools provide these operations without enabling unrestricted GitHub REST
writes:

- `github_get_issue`, `github_create_issue`, `github_update_issue`, and
  `github_comment_issue` manage issues and their discussions. Updates require
  `expected_state` to detect stale issue status.
- `github_list_branches` and `github_create_branch` work with branches. The
  controller creates only `pwm/`-prefixed branches from a verified Git revision.
- `github_get_pull_request`, `github_create_pull_request`, and
  `github_update_pull_request` inspect and edit PRs with explicit head SHA checks.
  Draft PRs can be marked ready with `github_mark_pull_request_ready`.
- `github_verify_pull_request` reports the current PR head revision,
  mergeability, check runs, and combined commit status. A PR with missing,
  pending, or failing check runs is not considered verified for merging.
- `github_merge_pull_request` requires `confirm=true` and `expected_head` set to
  the exact 40-character commit SHA. It verifies checks, requests a GitHub
  merge with that SHA, and confirms the resulting merged PR. GitHub enforces
  branch protection, review requirements, and merge permissions. This allows
  an agent to merge a PR it authored **when GitHub permits it**, but it cannot
  approve its own PR as an independent reviewer.
- A successful merge deletes the agent-managed `pwm/` head branch by default
  if it remains unchanged, belongs to the same repository, is unprotected,
  and is not referenced by any open PR. If cleanup fails, the merge result
  still reports success together with the cleanup failure; do not retry the
  merge. `github_cleanup_merged_branch` accepts an explicit merged PR number,
  exact head SHA, and confirmation for older branches. It does not delete
  unrelated or active branches without these checks.
- `github_set_branch_auto_delete` configures GitHub's own
  `delete_branch_on_merge` repository setting. It requires repository admin
  permission and `confirm=true`. This is the preferred option for automatic
  cleanup of **all** merged branches, including branches merged outside this
  application; repository rules continue to apply.

For example, with a project that has already linked `OWNER/REPO`:

```sh
# Inspect checks and the SHA the next merge request must preserve.
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$BASE/api/projects/research/github/pulls/42/verify?repository=OWNER/REPO"

# Merge only after the verification result reports ready_to_merge=true.
curl -fsS -X POST "$BASE/api/projects/research/github/pulls/42/merge?repository=OWNER/REPO" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"confirm":true,"expected_head":"REPLACE_WITH_40_CHARACTER_HEAD_SHA","merge_method":"squash","cleanup_branch":true}'
```

A single owner credential controls these operations; do not expose it to an
untrusted MCP client. PR and branch updates have preflight revision checks,
but GitHub does not offer atomic conditional deletion of a Git ref, so a small
race remains between the final reference read and delete. Repository protection
and rulesets are the final authority. For protected workflows, enable GitHub's
native automatic branch deletion and require reviews or merge queues as needed.
