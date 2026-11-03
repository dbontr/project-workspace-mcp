# Project Workspace MCP

Self-hosted project knowledge and distributed compute for MCP-compatible AI clients. A controller manages durable project records and bounded jobs. Independently paired worker agents advertise capacity and execute jobs in restricted Linux Docker containers.

## Quick start

Requires Python 3.11+. Workers additionally require Linux and Docker.

```bash
python -m venv .venv
. .venv/bin/activate
pip install -e .
project-workspace init  # copy the generated token to a secure environment variable
export PWM_ADMIN_TOKEN='replace-with-generated-token'
project-workspace controller
```

The controller listens on `127.0.0.1:8000` by default. The MCP endpoint is `http://127.0.0.1:8000/mcp/`. Supply `Authorization: Bearer <PWM_ADMIN_TOKEN>` to all MCP and administration requests. Use an authenticated HTTPS reverse proxy and an MCP-compatible client authentication flow for remote clients; a bearer token alone is not a substitute for OAuth integration.

Create a project and enroll a worker using the administrative API:

```bash
curl -X POST http://127.0.0.1:8000/api/projects -H "Authorization: Bearer $PWM_ADMIN_TOKEN" -H 'Content-Type: application/json' -d '{"id":"demo","title":"Demo Project"}'
curl -X POST http://127.0.0.1:8000/api/nodes -H "Authorization: Bearer $PWM_ADMIN_TOKEN" -H 'Content-Type: application/json' -d '{"name":"worker-one","capabilities":{"cpu":4,"memory_mb":4096,"max_jobs":1}}'
```

The enrollment response returns a secret worker token (shown only once). On a Linux worker:

```bash
export PWM_WORKER_TOKEN='token-from-enrollment-response'
project-workspace worker --node worker-one --controller https://your-authenticated-controller.example
```

Local testing can use `http://127.0.0.1:8000`. Remote enrollment must use HTTPS with valid certificates and appropriate access controls. Worker registration currently uses an administrative API; single-command pairing is a planned feature.

## Current capabilities

- SQLite-backed projects and optimistic-concurrency project notes.
- Project-scoped text search and linked repository metadata.
- Worker identity based on rotating, hashed bearer credentials and heartbeats.
- Durable job queue and capacity-based worker selection, one job per worker by default.
- Container execution with no network, a read-only root filesystem, a non-root user, CPU/RAM/pid limits, and dropped capabilities.
- MCP tools for projects, notes, worker inventory, jobs, and repository links.

## Security and limitations

The controller currently has one administrator identity, not per-user or per-project grants. Do not expose it on the public internet without an identity-aware gateway. Docker daemon access is privileged; register workers only on trusted systems and treat command execution as sensitive. CPU and memory declarations are worker-provided and not attested. Jobs have a seven-minute lease and a five-minute process timeout; long jobs may finish after lease expiry, so they are reported as expired and not accepted. Expired jobs are not retried automatically.

The first release does **not** include GitHub API access, token brokerage, scoped checkouts, artifact transfer, cross-platform execution, a graphical dashboard, automatic node pairing, or a full autonomous Codex-like agent. Repository links store metadata only. These features require separate implementation and acceptance tests; they are not implied by the MCP tools.

## Development

```bash
pip install -e '.[dev]'
pytest -q
```