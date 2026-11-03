# Project Workspace MCP

A self-hosted project workspace and distributed compute broker for MCP-compatible AI clients. The controller retains project knowledge, artifact history, GitHub access, resource reservations, environment definitions, and execution state. Paired worker agents provide isolated containers with filesystem, terminal, application-toolchain, and approved hardware access.

**Architecture:** The currently deployed controller is Python (FastAPI, SQLite, and the MCP Python SDK). The preferred Linux worker is written in **Go** and builds as a standalone binary. Python is an optional language *inside* workspaces, not a dependency of the Go worker. The Go controller gateway owns project and versioned-note operations; it forwards all remaining worker, scheduler, GitHub, and MCP endpoints to the loopback Python controller. Full Go-only operation is not yet supported. A single host can serve both roles, and Kubernetes is not required.

This is an alpha implementation for trusted, owner-operated infrastructure. It is **not** a hosted multi-tenant execution service, full Codex replacement, or verified installation of Vivado. The complete acceptance requirements are tracked in this repository's issues.

## Controller installation

Requires Python 3.11 or later. On a machine that will retain project knowledge and artifacts:

```sh
python3 -m venv .venv
. .venv/bin/activate
pip install -e .
project-workspace init
project-workspace controller
```

The controller listens on `http://127.0.0.1:8000` by default. The MCP endpoint is `/mcp/`, the operator dashboard is `/dashboard`, and persistent state is under `~/.local/share/project-workspace/`. `init` creates an administrator token in `~/.config/project-workspace/admin.token` with owner-only file permissions. No model inference API key is required; the connected AI client supplies reasoning.

For remote access, terminate TLS, configure the controller's `PWM_ALLOWED_HOSTS` and `PWM_ALLOWED_ORIGINS`, and enforce authentication before public exposure. The controller currently uses one static administrator token, **not OAuth or multi-user project authorization**. Do not hand that token to untrusted clients. Hosted ChatGPT connections may require an OAuth-aware gateway.

## Go controller gateway

The Go controller can own project creation, note reads and writes, and note search while the Python service continues to own MCP, worker scheduling, and GitHub integration. Both use the same SQLite database schema and SQLite transactions. Start the existing Python controller on loopback first, then use the Go controller as the local gateway:

```sh
# Requires Go 1.23+, a C compiler, pkg-config, and SQLite development headers at build time.
CGO_ENABLED=1 go build -o project-workspace-controller ./cmd/project-workspace-controller
export PWM_ADMIN_TOKEN="$(cat ~/.config/project-workspace/admin.token)"
./project-workspace-controller --listen 127.0.0.1:8080 \
  --upstream http://127.0.0.1:8000
```

The gateway listens on loopback by default. Connect a local HTTP client to port 8080. Use a separately authenticated TLS terminator for remote clients; do not publish either server directly. The Go gateway handles `GET/POST /api/projects`, `GET/POST /api/projects/{project}/notes/{name}`, and `GET /api/projects/{project}/search?q=...`. Other routes, including `/mcp/`, remain forwarded to the Python backend. The Go gateway is a **migration boundary**, not a replacement for the compute scheduler or an OAuth implementation. The worker requires only the publicly configured gateway URL and its worker credential.

## Build and pair the Go worker

Requires Linux, Go 1.23+ at build time, and Docker on each execution node. The resulting worker binary does not require Go or Python to remain installed.

```sh
go build -o project-workspace-worker ./cmd/project-workspace-worker
./project-workspace-worker doctor
```

On the controller, create a one-time pairing code:

```sh
project-workspace pair
```

On a worker machine, replace the URL, node ID, and code:

```sh
./project-workspace-worker join --controller https://controller.example --node worker-one --code PAIRING_CODE
./project-workspace-worker serve --controller https://controller.example --node worker-one
```

Pairing writes a worker credential to the user's configuration directory with mode `0600`. Worker credentials are distinct from the administrator token. The agent makes outbound HTTPS requests to the controller, reports capacity, claims bounded operations, renews long operation leases, and reports verified results. A loopback HTTP controller is accepted for local integration testing only. Transient controller connection failures are retried with bounded exponential backoff; permanent authentication failures stop the agent. An operation whose completion acknowledgment was lost remains subject to its lease and requires reconciliation rather than blind reexecution. The Python CLI's `worker` command remains available for existing deployments, but the Go agent is the preferred path.

## USB passthrough (optional)

USB access has two independent **off-by-default** switches. The operator starts a Linux Go worker with `--enable-usb` (or `PWM_ENABLE_USB_PASSTHROUGH=1`); the user must also enable USB passthrough for a particular workspace and select the **exact advertised device and worker**. Ordinary jobs receive no USB device access. Disabling the operator switch prevents USB inventory advertisement and future USB container allocations, but does not retroactively revoke a device from a running container; stop its workspace and verify cleanup first.

When enabled, the Go worker discovers eligible whole USB mass-storage disks by stable `/dev/disk/by-id/usb-*` names. It checks their actual USB sysfs identity, rejects partitions, and refuses disks with mounted partitions or active mapped-device descendants. A connected USB disk may need to be unmounted by the operator before it is eligible. USB serial adapters and USB/JTAG devices require an explicit approved entry in the worker device manifest, such as `{"id":"usb-jtag-a","kind":"usb","paths":["/dev/serial/by-id/usb-Adapter"]}`. Do not use raw `/dev/sdX` or arbitrary `/dev` mappings. Workers re-evaluate availability on heartbeat and before container startup.

To enable raw device access on a trusted worker:

```sh
./project-workspace-worker serve --controller https://controller.example \
  --node worker-one --enable-usb
```

Inspect `GET /api/hardware` for the device `node` and `id`, and choose that device in the **Create workspace** dashboard form. Alternatively create a workspace through MCP `workspace_create` or REST:

```json
{
  "project_id": "research",
  "cpu": 1,
  "memory_mb": 2048,
  "usb_passthrough": true,
  "usb_node": "worker-one",
  "usb_device_id": "usb-REPLACE-WITH-ADVERTISED-ID",
  "hardware": {"usb": 1}
}
```

### Transfer an OS image to a workspace

An OS image can be larger than the normal 128 KiB workspace text-write limit. The controller provides an authenticated, streamed binary artifact upload and the Go worker can then import that artifact into its workspace. The image is not placed in a Docker layer. Configure `PWM_MAX_ARTIFACT_BYTES` on the controller **and worker** to a value sufficient for the image (the default is 2 GiB), and provision equivalent disk capacity on both systems.

```sh
IMAGE=path/to/operating-system.img
SHA256=$(sha256sum "$IMAGE" | awk '{print $1}')
curl --fail-with-body -X PUT "$BASE/api/projects/research/artifacts/upload" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Artifact-Name: operating-system.img" \
  -H "X-Content-Sha256: $SHA256" \
  -H 'Content-Type: application/octet-stream' \
  --data-binary @"$IMAGE"
```

Use the returned `id` to queue the import into a ready workspace:

```sh
curl --fail-with-body -X POST \
  "$BASE/api/projects/research/workspaces/WORKSPACE_ID/artifacts/import" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"artifact_id":"ARTIFACT_ID","path":"images/operating-system.img"}'
```

Check the operation result and verify the checksum inside the workspace before imaging any device. This API does not issue a flash command automatically; selecting a physical target and initiating a destructive write remain separate, explicit actions. File import refuses to overwrite an existing workspace file.

The controller reserves that single device exclusively until the worker confirms container cleanup. A missing or busy selected USB device causes an error; it cannot silently choose another drive. The container maps only the selected device, not all host USB ports. A workspace with a block-device lease can run an OS imaging tool against its mapped device if it has write access. **Flashing is destructive**: identify the intended target on the host and inside the container before writing an image, and never select a host system disk. Docker device pass-through and group permissions are not strong protection against malicious workloads, nor can they make unplug/replug during a flash safe. Physical-drive imaging has not yet passed hardware acceptance tests.

## Projects, environments, and workspace snapshots

An **environment** defines which applications and libraries exist in a container image. A **workspace** owns files and runtime state. The **hardware lease** reserves physical devices independently. Saving one does not automatically save the others.

An administrator may define a versioned environment using either an approved, already-installed image, or a Dockerfile. A Dockerfile is built on the selected worker, with an immutable versioned tag. Build recipes are explicit project data; they cannot alter a running container's read-only root filesystem.

```sh
TOKEN=$(cat ~/.config/project-workspace/admin.token)
BASE=http://127.0.0.1:8000
curl -fsS -X POST "$BASE/api/projects" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"id":"research","title":"Research"}'

# Create an ordinary workspace first; its worker can build a named environment.
curl -fsS -X POST "$BASE/api/workspaces" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"project_id":"research","cpu":2,"memory_mb":4096}'

# Save an environment recipe. A build is a distinct, asynchronous operation.
curl -fsS -X POST "$BASE/api/projects/research/environments" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"scientific-python","base_image":"python:3.12-slim", \
       "dockerfile":"FROM python:3.12-slim\nRUN python --version\n", \
       "expected_version":0}'

curl -fsS -X POST "$BASE/api/projects/research/environments/scientific-python/build/WORKSPACE_ID" \
  -H "Authorization: Bearer $TOKEN"
```

Poll `GET /api/projects/research/workspaces/WORKSPACE_ID/operations/OPERATION_ID` for success, then create a workspace with `"environment":"scientific-python"`. The scheduler selects the node that holds the built image; it rejects a stale, failed, or unbuilt environment. Define a revised environment with the current `expected_version` to create another image version. Existing workspaces continue using their original image.

The default Docker build has network access disabled. To install packages from a public index, the trusted worker administrator may set `PWM_BUILD_NETWORK=1`. For proprietary installers or other local files, configure an approved worker-local `PWM_BUILD_CONTEXT_ROOT` and put the installer files in a named child directory. Set `"context":"directory-name"` when defining the environment; worker code resolves and verifies that directory. The Dockerfile may use `COPY` for files in that approved context. Neither the path nor installation files are transferred from the AI client automatically. Do not place licenses, passwords, or tokens in images, Dockerfiles, or the build context.

### Save, export, and restore

- `workspace_snapshot` creates a compressed, SHA-256 checked archive of workspace files and uploads it to the controller using a fenced, streamed worker transfer. List snapshots and restore a chosen revision into a different workspace in the same project. Snapshots preserve files, not a running process or its memory.
- `environment_export` saves the built Docker image as a separate durable archive. `environment_import` loads an authorized image export into another approved worker, after checking project and environment versions. Images may be many gigabytes; set `PWM_MAX_ARTIFACT_BYTES` and provision sufficient durable storage and bandwidth.
- `workspace_call` can run commands, read and write files, list directories, search text, and export files. Large binary inputs use the authenticated streaming artifact-upload endpoint and `workspace_import_artifact`. Only a worker can upload an operation's result, and its short-lived lease token is checked again before the controller commits the artifact.
- Stop a workspace with `workspace_destroy` or the REST `/stop` route. The worker removes the container but retains the named Docker volume for explicit recovery. The controller releases its GPU/NPU/FPGA reservations **only after the worker confirms cleanup**. Failed cleanup can be retried. Export vital files before deleting volumes.

Docker image archives and filesystem snapshots are separate because mounted volumes are excluded from `docker commit` and `docker image save`. The controller database and artifact directory still need an independently tested off-host backup; the feature above is not a controller backup system.

## Reserve GPUs, NPUs, and FPGAs

A worker's hardware is an explicit, operator-approved inventory. The controller does not infer that every discovered device is safe to pass through to arbitrary code. Create a local manifest such as:

```json
{
  "devices": [
    {
      "id": "nvidia-gpu-a",
      "kind": "gpu",
      "model": "RTX GPU",
      "cdi": "nvidia.com/gpu=GPU-REPLACE-WITH-LOCAL-UUID"
    },
    {
      "id": "fpga-board-a",
      "kind": "fpga",
      "model": "PCIe FPGA",
      "paths": ["/dev/REPLACE-WITH-APPROVED-FPGA-DEVICE"]
    },
    {
      "id": "npu-a",
      "kind": "npu",
      "paths": ["/dev/accel/accel0"]
    }
  ]
}
```

The examples are **configuration patterns, not detected devices**. Replace the identifiers with actual host-local paths or CDI names after inspecting the hardware. Unavailable paths cause allocation startup to fail; the system never silently falls back to full host privileges. Start the worker with `--devices /etc/project-workspace/devices.json`, or set `PWM_DEVICE_MANIFEST`. The manifest is read by the worker and never supplied by an AI client.

To request an exclusive reservation:

```sh
curl -fsS -X POST "$BASE/api/workspaces" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"project_id":"research","cpu":4,"memory_mb":8192, \
       "hardware":{"gpu":1,"fpga":1},"duration_minutes":120}'
```

The controller chooses an online worker that has adequate free CPU/RAM and the requested number of unleased, approved devices. USB reservations additionally require the exact worker, device ID, and enabled workspace toggle. Each whole device ID is reserved transactionally; a failed or disconnected worker does not free its hardware until cleanup is confirmed. Check `/api/hardware` or the MCP `list_hardware` tool to inspect leases. On GPU nodes, install and configure NVIDIA Container Toolkit/CDI first. FPGA and NPU mappings depend on their device drivers, firmware, and vendor runtime. USB/JTAG passthrough may change device paths after reconnection; a host-local hardware-server adapter is preferable for fragile boards.

**Vivado:** AMD Vivado and Vitis are proprietary and may require licensed installation media, tool-specific environment variables, supported x86-64 operating systems, and device drivers. This project does not redistribute or silently install them. Build or import an administrator-approved image from your licensed software, then define an environment with `"architecture":"amd64"`. For an already installed image, leave `dockerfile` empty and provide the approved base image name. Use the explicit FPGA device manifest only when a physical board is required; synthesis itself normally needs no board checkout.

## GitHub integration

Set a fine-grained `PWM_GITHUB_TOKEN` on the controller, never on compute workers. Link each authorized repository to its project. The MCP tools support repository information, file reads, issues and PRs, immutable checkout archives, and revision-guarded draft PR publication from stored artifacts. `github_repository_api` exposes additional repository REST operations; generic writes are disabled unless `PWM_GITHUB_GENERIC_WRITES=1` is intentionally configured. Interactive GitHub App installation, separate user authorization, and unrestricted organization administration are not implemented.

## Runtime security and operational limits

Workspaces use Docker namespaces, disabled networking, a read-only container root filesystem, non-root execution, CPU/memory/PID limits, dropped Linux capabilities, and writable named volumes. Built images are approved by worker configuration and the controller's versioned environment record. Docker build recipes execute code and must be restricted to trusted administrators. GPU/CDI and FPGA passthrough expand the security boundary; **Docker is not sufficient isolation for mutually untrusted users**. Deploy a VM-backed worker for adversarial workloads.

The controller is currently a single owner and a single SQLite database, not a cluster. The Go gateway only migrates project and note APIs and still requires the Python MCP/scheduler backend. Device leases survive controller restarts but a dead worker may retain containers and device access. The next production milestone includes active reconciliation, user OAuth, multi-machine TLS acceptance, off-host backups, and tested controller restoration.

## Verification

```sh
pip install -e '.[dev]'
python -m pytest -q
go test ./...
go vet ./...
go build -o project-workspace-worker ./cmd/project-workspace-worker
CGO_ENABLED=1 go build -o project-workspace-controller ./cmd/project-workspace-controller
```

GitHub Actions runs Python 3.11 and 3.13 checks, Go unit tests and cross-builds, a Linux Docker lifecycle test, and an authenticated Go-worker-to-controller integration test. The latter exercises actual hardware lease metadata, files and commands, snapshots, image builds, image export/import, and cleanup on a test runner. Real FPGA/NPU/USB passthrough, licensed Vivado behavior, cross-physical-host deployment, and production security require independent acceptance on authorized hardware. The Go gateway has a cross-language SQLite compatibility test, but the remaining controller endpoints still use Python.
