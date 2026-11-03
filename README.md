# Project Workspace MCP

A self-hosted project workspace and distributed compute broker for MCP-compatible AI clients. The controller owns project knowledge, durable artifacts, GitHub access, resource reservations, and work history. Paired Linux workers provide isolated execution environments with Desktop Commander-style file and terminal tools.

A single host can operate as both controller and worker. No Kubernetes installation or inference API key is required. The reasoning model runs in the connected MCP client; this package is not itself an autonomous AI model or Codex replacement.

## Requirements

- Python 3.11 or later on the controller and each worker.
- Linux with Docker on each **execution worker**. The controller can run on other operating systems.
- An MCP client that can connect to an authenticated Streamable HTTP endpoint. The current controller supports a static single-owner bearer credential. Some hosted MCP clients require OAuth; compatibility must be tested for the selected client.
- Optional: `PWM_GITHUB_TOKEN` with permissions appropriate for the linked repositories.

## Start a controller

```sh
python3 -m venv .venv
. .venv/bin/activate
pip install -e .
project-workspace init
project-workspace controller
```

`init` saves a random administrator token under `~/.config/project-workspace/admin.token` with owner-only permissions. The controller listens at `http://127.0.0.1:8000` and stores SQLite state under `~/.local/share/project-workspace/`. The MCP path is `/mcp/`. Open `http://127.0.0.1:8000/dashboard` for a minimal browser dashboard.

For remote clients, deploy an authenticated TLS terminator and configure `PWM_ALLOWED_HOSTS` for the external hostname and `PWM_ALLOWED_ORIGINS` for approved browser origins. These values are enforced independently by FastAPI and the MCP SDK. **Do not publish the controller or worker API over unauthenticated public HTTP.** Remote MCP host clients may require an OAuth-enabled gateway, which this release does not provide.

## Pair a Linux worker

Install the package on the worker, then inspect local prerequisites:

```sh
project-workspace doctor
```

On the controller, issue a single-use code:

```sh
project-workspace pair
```

On the worker, join the controller and start its agent (substitute the issued code):

```sh
project-workspace join --controller https://your-controller.example --node worker-one --code YOUR-CODE
project-workspace worker --controller https://your-controller.example --node worker-one
```

Enrollment creates a worker credential in `~/.config/project-workspace/workers/worker-one.token`. Pairing codes expire in 10 minutes and cannot be reused. The worker calls the controller over HTTPS. For single-machine development, use `http://127.0.0.1:8000` and pair a worker on the same host.

Set `PWM_ALLOWED_IMAGES` on workers to a comma-separated list of approved container images. The default is `python:3.12-slim`. The agent requires access to the local Docker daemon, which is a privileged operation; deploy only on devices you administer.

## Project workflow

The single administrator can operate through MCP tools, or use the controller's REST API. Here is a REST example:

```sh
TOKEN=$(cat ~/.config/project-workspace/admin.token)
BASE=http://127.0.0.1:8000
curl -fsS -X POST "$BASE/api/projects" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"id":"demo","title":"Demo project"}'

curl -fsS -X POST "$BASE/api/workspaces" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"project_id":"demo","cpu":1,"memory_mb":512,"duration_minutes":120}'
```

The create response contains a `workspace_id` and `starting` state. Wait for the worker to report `ready` in `GET /api/projects/demo/workspaces`. Queue a command:

```sh
curl -fsS -X POST "$BASE/api/projects/demo/workspaces/WORKSPACE_ID/operations" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"action":"command","payload":{"command":"python --version"}}'
```

Poll `GET /api/projects/demo/workspaces/WORKSPACE_ID/operations/OPERATION_ID` for the actual exit code and output. Supported workspace actions are `command`, `read_file`, `write_file`, `list_directory`, `search`, and `export_file`. File paths are relative to `/workspace`. `write_file` supports `expected_sha256` to avoid blind overwrites. `export_file` preserves a SHA-256 checked artifact on the controller, accessible through `GET /api/projects/demo/artifacts/ARTIFACT_ID`.

Stop with `POST /api/projects/demo/workspaces/WORKSPACE_ID/stop`. The worker removes the running container but retains its named Docker volume for manual recovery. Export essential outputs before stopping. Deletion of retained volumes is an explicit operator responsibility.

## GitHub integration

Set `PWM_GITHUB_TOKEN` on the controller using a fine-grained personal access token with the repository permissions actually needed. Never set it on workers or store it in project notes. A GitHub App installation flow is a separate planned integration.

Link repositories to a project with `link_github_repository`. The MCP tools then support:

- `github_repository`, `github_read_file`, `github_list_issues`, `github_list_pull_requests`, `github_create_issue`.
- `github_checkout_repository`: resolves a ref to an immutable commit, downloads an archive to the controller, and stages sanitized files into `/workspace/repos/OWNER__REPO` through a worker operation. GitHub tokens do not enter the workspace. Archive download is limited to 12 MiB and expanded content to 32 MiB; submodules and symlinks are not imported.
- `github_publish_artifacts`: creates a new GitHub branch and a draft pull request from selected controller artifacts. It requires an exact expected base commit SHA and will reject a stale base instead of rewriting it. A draft pull request is publication for review, not a merge.

Repository access remains bounded by both the GitHub token and the project's linked repository list. The broker is not a generic unrestricted GitHub account proxy; advanced actions such as reviews, releases, and organization administration are not implemented.

## Security and storage

The controller has a single administrator identity. Keep its token private and protect access to the SQLite database and artifact directory. Worker credentials are stored as SHA-256 digests in the controller; enrollment tokens are displayed only once. Workspace operations use short-lived, fenced operation tokens. Separate project IDs restrict lookups and artifact access, but **they are not multi-tenant user authorization**: every holder of the administrator token can access every project.

Worker containers have networking disabled, a read-only root filesystem, CPU/memory/pid limits, a non-root user, and dropped Linux capabilities. Only named workspace volumes are writable. These controls reduce risk but do not make arbitrary code execution on a Docker host a strong hostile-tenant security boundary. Use a virtual machine isolation backend before allowing untrusted users to run code.

The controller's SQLite database and `artifacts/` directory are the durable authority for knowledge and exported outputs. Back up both together and verify restoration. Docker volumes and unexported workspace files are not a substitute for controller backups. The SQLite state and local artifact directory are not replicated across controller machines.

## Verification

```sh
pip install -e '.[dev]'
pytest -q
```

The unit suite checks project isolation, concurrency versions, worker pairing, lease fencing, capacity reservations, artifact integrity, path validation, and GitHub revision guards. It uses mocks for GitHub and Docker. Live MCP client authentication, Docker execution, GPU jobs, multi-host networking, GitHub publication, TLS/OAuth, and restoration remain deployment acceptance requirements.

## Development boundaries

Implemented features provide a single-owner distributed workspace foundation. The following are outside the current release: multi-user OAuth and project grants; enterprise policy and auditing; automatic machine provisioning and upgrades; macOS/Windows workers; GPU reservation and acceleration; full GitHub account administration; unlimited artifact transfers; resilient external schedulers such as Slurm; autonomous model-hosted agent execution; production-grade host isolation; and live acceptance of the end-to-end workflow. Their absence must not be mistaken for a completed Codex replacement.
