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

## Operating limits

This is a single-owner system intended for trusted machines. It does not provide production OAuth, fine-grained multi-user project grants, VM-grade hostile-tenant isolation, automatic off-host database backup/restoration, or verified real GPU/NPU/FPGA/USB hardware passthrough. Those features require separate acceptance before untrusted users or destructive hardware work can be enabled. Proprietary applications such as Vivado are not redistributed by this project. An MCP tool call is not an autonomous reasoning agent.
