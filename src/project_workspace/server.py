"""Single-owner controller, REST operations, and Streamable HTTP MCP tools."""
import os
import secrets
from contextlib import asynccontextmanager
from importlib.resources import files
from pathlib import Path

from fastapi import Depends, FastAPI, Header, HTTPException
from fastapi.responses import Response, HTMLResponse
from mcp.server.fastmcp import FastMCP
from pydantic import BaseModel, Field
from starlette.middleware.trustedhost import TrustedHostMiddleware
from starlette.responses import PlainTextResponse
from starlette.types import Receive, Scope, Send

from .core import Store
from .github import GitHubBroker

store = Store(os.getenv('PWM_DB', str(Path.home() / '.local/share/project-workspace/state.db')))
ADMIN = os.getenv('PWM_ADMIN_TOKEN', '')
if len(ADMIN) < 32:
    raise RuntimeError('PWM_ADMIN_TOKEN must contain at least 32 characters')

# A root path inside the /mcp mount yields the public /mcp/ endpoint.
mcp = FastMCP('Project Workspace', stateless_http=True, json_response=True, streamable_http_path='/')


def github(project_id: str, repository: str) -> GitHubBroker:
    if repository not in store.repos(project_id):
        raise ValueError('Repository not linked to this project')
    return GitHubBroker(os.getenv('PWM_GITHUB_TOKEN', ''))


def admin(authorization: str = Header(default='')):
    if not secrets.compare_digest(authorization, 'Bearer ' + ADMIN):
        raise HTTPException(401, 'Invalid controller credential')


def worker_auth(node_id: str, authorization: str):
    token = authorization.removeprefix('Bearer ')
    if not store.node_auth(node_id, token):
        raise HTTPException(401, 'Invalid worker credential')


class Project(BaseModel):
    id: str
    title: str


class Note(BaseModel):
    body: str
    expected_version: int


class Repo(BaseModel):
    full_name: str


class Job(BaseModel):
    command: str
    image: str = 'python:3.12-slim'
    cpu: float = 1
    memory_mb: int = 512


class Enrollment(BaseModel):
    name: str
    capabilities: dict


class Pairing(BaseModel):
    pairing_code: str
    name: str
    capabilities: dict


class Heartbeat(BaseModel):
    capabilities: dict


class Finish(BaseModel):
    job_id: str
    lease_token: str
    status: str
    result: str


class WorkspaceRequest(BaseModel):
    project_id: str
    image: str = 'python:3.12-slim'
    cpu: float = 1
    memory_mb: int = 512
    duration_minutes: int = Field(default=120, ge=1, le=1440)


class OperationRequest(BaseModel):
    action: str
    payload: dict


class OperationFinish(BaseModel):
    operation_id: str
    lease_token: str
    success: bool
    result: dict


@mcp.tool()
def list_projects() -> list[dict]:
    """List projects registered with this controller."""
    return store.projects()


@mcp.tool()
def create_project(project_id: str, title: str) -> dict:
    """Register a project. Its knowledge and compute allocations are isolated by project ID."""
    return store.project_create(project_id, title)


@mcp.tool()
def read_project_note(project_id: str, name: str) -> dict:
    """Read an authoritative project note with its revision number."""
    return store.note_get(project_id, name)


@mcp.tool()
def write_project_note(project_id: str, name: str, body: str, expected_version: int) -> dict:
    """Write a project note only when its version matches (zero for creation)."""
    return store.note_put(project_id, name, body, expected_version)


@mcp.tool()
def search_project_knowledge(project_id: str, query: str) -> list[dict]:
    """Find notes within the specified project."""
    return store.search(project_id, query)


@mcp.tool()
def list_compute_nodes() -> list[dict]:
    """Return registered workers, advertised resources, and recent heartbeat state."""
    return store.nodes()


@mcp.tool()
def submit_compute_job(project_id: str, command: str, image: str = 'python:3.12-slim',
                       cpu: float = 1, memory_mb: int = 512) -> dict:
    """Queue a bounded one-shot container task; submission is not execution."""
    return store.job_submit(project_id, command, image, cpu, memory_mb)


@mcp.tool()
def list_project_jobs(project_id: str) -> list[dict]:
    """Inspect one-shot compute jobs and their confirmed outcomes."""
    return store.jobs(project_id)


@mcp.tool()
def link_github_repository(project_id: str, repository: str) -> dict:
    """Associate a GitHub repository identifier with a project; does not grant access."""
    return store.repo_link(project_id, repository)


@mcp.tool()
def list_github_repositories(project_id: str) -> list[str]:
    """List GitHub repository associations for a project."""
    return store.repos(project_id)


@mcp.tool()
def workspace_create(project_id: str, image: str = 'python:3.12-slim',
                     cpu: float = 1, memory_mb: int = 512, duration_minutes: int = 120) -> dict:
    """Reserve an online Linux worker and request an isolated interactive container."""
    return store.workspace_create(project_id, image, cpu, memory_mb, duration_minutes)


@mcp.tool()
def workspace_list(project_id: str) -> list[dict]:
    """List workspace states, leases, and allocated workers within a project."""
    return store.workspaces(project_id)


@mcp.tool()
def workspace_call(project_id: str, workspace_id: str, action: str, payload: dict) -> dict:
    """Queue a workspace operation: command, read_file, write_file, list_directory, search, export_file."""
    return store.workspace_call(project_id, workspace_id, action, payload)


@mcp.tool()
def workspace_operation(project_id: str, workspace_id: str, operation_id: str) -> dict:
    """Get the confirmed result of a queued workspace operation."""
    return store.workspace_operation(project_id, workspace_id, operation_id)


@mcp.tool()
def workspace_destroy(project_id: str, workspace_id: str) -> dict:
    """Stop an interactive workspace, retaining its named volume for recovery."""
    return store.workspace_stop(project_id, workspace_id)


@mcp.tool()
def github_repository(project_id: str, repository: str) -> dict:
    """Read GitHub repository information for a project-linked repository."""
    return github(project_id, repository).repo(repository)


@mcp.tool()
def github_read_file(project_id: str, repository: str, path: str, ref: str) -> dict:
    """Read a GitHub file from a selected ref, within a linked repository."""
    broker = github(project_id, repository)
    sha = broker.commit(repository, ref)
    return broker.file(repository, path, sha)


@mcp.tool()
def github_list_issues(project_id: str, repository: str, state: str = 'open') -> list[dict]:
    """List issues in a project-linked GitHub repository."""
    return github(project_id, repository).issues(repository, state)


@mcp.tool()
def github_list_pull_requests(project_id: str, repository: str, state: str = 'open') -> list[dict]:
    """List pull requests in a project-linked GitHub repository."""
    return github(project_id, repository).pull_requests(repository, state)


@mcp.tool()
def github_create_issue(project_id: str, repository: str, title: str, body: str = '') -> dict:
    """Create an issue in a project-linked GitHub repository."""
    return github(project_id, repository).issue_create(repository, title, body)


@mcp.tool()
def github_checkout_repository(project_id: str, workspace_id: str, repository: str, ref: str) -> dict:
    """Fetch an immutable GitHub archive and stage it in an owned workspace without passing credentials to workers."""
    store.workspace_get(project_id, workspace_id)
    broker = github(project_id, repository)
    revision, content = broker.snapshot(repository, ref)
    artifact = store.artifact_save(project_id, 'repo-' + revision[:12] + '.tar.gz', content)
    operation = store.workspace_import(project_id, workspace_id, repository, revision, artifact['id'])
    return {'revision': revision, 'artifact_id': artifact['id'], **operation}


@mcp.tool()
def github_publish_artifacts(project_id: str, repository: str, base_branch: str,
                             expected_head: str, changes: dict[str, str], title: str,
                             body: str = '') -> dict:
    """Publish selected durable project artifacts to a revision-checked draft pull request."""
    broker = github(project_id, repository)
    files = {path: store.artifact_get(project_id, artifact_id)[1] for path, artifact_id in changes.items()}
    return broker.publish(repository, base_branch, expected_head, files, title, body)


@mcp.tool()
def list_project_artifacts(project_id: str) -> list[dict]:
    """List durable SHA-256-verified artifacts exported from a project."""
    return store.artifacts(project_id)


@asynccontextmanager
async def lifespan(app):
    async with mcp.session_manager.run():
        yield


app = FastAPI(title='Project Workspace MCP', lifespan=lifespan)
app.add_middleware(TrustedHostMiddleware,
                   allowed_hosts=os.getenv('PWM_ALLOWED_HOSTS', '127.0.0.1,localhost').split(','))


@app.get('/health')
def health():
    return {'status': 'ok'}


@app.get('/dashboard', response_class=HTMLResponse)
def dashboard():
    return HTMLResponse(files('project_workspace').joinpath('dashboard.html').read_text(encoding='utf-8'),
                        headers={'Cache-Control':'no-store', 'Content-Security-Policy':
                                 "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; base-uri 'none'"})


@app.get('/api/status', dependencies=[Depends(admin)])
def status():
    return store.status()


@app.get('/api/projects', dependencies=[Depends(admin)])
def projects():
    return store.projects()


@app.post('/api/projects', dependencies=[Depends(admin)])
def create_project_http(value: Project):
    return store.project_create(value.id, value.title)


@app.post('/api/projects/{project_id}/notes/{name}', dependencies=[Depends(admin)])
def note_http(project_id: str, name: str, value: Note):
    return store.note_put(project_id, name, value.body, value.expected_version)


@app.get('/api/projects/{project_id}/notes/{name}', dependencies=[Depends(admin)])
def note_read_http(project_id: str, name: str):
    return store.note_get(project_id, name)


@app.get('/api/projects/{project_id}/jobs', dependencies=[Depends(admin)])
def jobs_http(project_id: str):
    return store.jobs(project_id)


@app.get('/api/projects/{project_id}/repositories', dependencies=[Depends(admin)])
def repositories_http(project_id: str):
    return store.repos(project_id)


@app.get('/api/projects/{project_id}/artifacts', dependencies=[Depends(admin)])
def artifact_list_http(project_id: str):
    return store.artifacts(project_id)


@app.get('/api/projects/{project_id}/artifacts/{artifact_id}', dependencies=[Depends(admin)])
def artifact_http(project_id: str, artifact_id: str):
    meta, content = store.artifact_get(project_id, artifact_id)
    return Response(content, media_type='application/octet-stream',
                    headers={'Content-Disposition': 'attachment; filename="' + meta['name'] + '"',
                             'X-Content-SHA256': meta['sha256']})


@app.get('/api/nodes', dependencies=[Depends(admin)])
def nodes_http():
    return store.nodes()


@app.post('/api/nodes', dependencies=[Depends(admin)])
def register_node(value: Enrollment):
    return {'node_id': value.name, 'worker_token': store.node_register(value.name, value.capabilities)}


@app.post('/api/pairing', dependencies=[Depends(admin)])
def create_pairing():
    return store.pairing_create()


@app.post('/api/pair')
def pair_node(value: Pairing):
    return store.pairing_redeem(value.pairing_code, value.name, value.capabilities)


@app.post('/api/workspaces', dependencies=[Depends(admin)])
def create_workspace_http(value: WorkspaceRequest):
    return store.workspace_create(value.project_id, value.image, value.cpu, value.memory_mb,
                                  value.duration_minutes)


@app.get('/api/projects/{project_id}/workspaces', dependencies=[Depends(admin)])
def workspaces_http(project_id: str):
    return store.workspaces(project_id)


@app.post('/api/projects/{project_id}/workspaces/{workspace_id}/operations', dependencies=[Depends(admin)])
def operation_http(project_id: str, workspace_id: str, value: OperationRequest):
    return store.workspace_call(project_id, workspace_id, value.action, value.payload)


@app.get('/api/projects/{project_id}/workspaces/{workspace_id}/operations/{operation_id}',
         dependencies=[Depends(admin)])
def operation_status_http(project_id: str, workspace_id: str, operation_id: str):
    return store.workspace_operation(project_id, workspace_id, operation_id)


@app.post('/api/projects/{project_id}/workspaces/{workspace_id}/stop', dependencies=[Depends(admin)])
def stop_http(project_id: str, workspace_id: str):
    return store.workspace_stop(project_id, workspace_id)


@app.post('/api/worker/{node_id}/heartbeat')
def heartbeat(node_id: str, value: Heartbeat, authorization: str = Header(default='')):
    worker_auth(node_id, authorization)
    store.heartbeat(node_id, value.capabilities)
    return {'ok': True}


@app.post('/api/worker/{node_id}/claim')
def claim(node_id: str, authorization: str = Header(default='')):
    worker_auth(node_id, authorization)
    caps = next((n['capabilities'] for n in store.nodes() if n['id'] == node_id), {})
    return {'job': store.claim(node_id, caps)}


@app.post('/api/worker/{node_id}/finish')
def finish(node_id: str, value: Finish, authorization: str = Header(default='')):
    worker_auth(node_id, authorization)
    return store.finish(node_id, value.job_id, value.lease_token, value.status, value.result)


@app.get('/api/worker/{node_id}/workspaces/{workspace_id}/artifacts/{artifact_id}')
def worker_artifact_http(node_id: str, workspace_id: str, artifact_id: str,
                         authorization: str = Header(default='')):
    worker_auth(node_id, authorization)
    _, content = store.worker_artifact(node_id, workspace_id, artifact_id)
    return Response(content, media_type='application/octet-stream')


@app.post('/api/worker/{node_id}/operation/claim')
def operation_claim_http(node_id: str, authorization: str = Header(default='')):
    worker_auth(node_id, authorization)
    return {'operation': store.operation_claim(node_id)}


@app.post('/api/worker/{node_id}/operation/finish')
def operation_finish_http(node_id: str, value: OperationFinish,
                          authorization: str = Header(default='')):
    worker_auth(node_id, authorization)
    return store.operation_finish(node_id, value.operation_id, value.lease_token,
                                  value.success, value.result)


@app.exception_handler(ValueError)
async def invalid_request(request, error: ValueError):
    return PlainTextResponse(str(error), status_code=409 if 'conflict' in str(error).lower() else 400)


class ProtectedMCP:
    def __init__(self, downstream):
        self.downstream = downstream

    async def __call__(self, scope: Scope, receive: Receive, send: Send):
        headers = dict(scope.get('headers', []))
        authorization = headers.get(b'authorization', b'').decode('utf-8', 'replace')
        if not secrets.compare_digest(authorization, 'Bearer ' + ADMIN):
            await PlainTextResponse('Unauthorized', status_code=401)(scope, receive, send)
            return
        await self.downstream(scope, receive, send)


app.mount('/mcp', ProtectedMCP(mcp.streamable_http_app()))
