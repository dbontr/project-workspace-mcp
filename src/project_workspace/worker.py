"""Linux worker with Docker-isolated, project-scoped workspace tools."""
import base64
import hashlib
import io
import os
import platform
import re
import shlex
import shutil
import subprocess
import tarfile
import time
from pathlib import PurePosixPath

import httpx


def capabilities():
    memory = 0
    if os.path.exists('/proc/meminfo'):
        with open('/proc/meminfo', encoding='ascii') as source:
            for line in source:
                if line.startswith('MemAvailable:'):
                    memory = int(line.split()[1]) // 1024
                    break
    out = {'cpu': os.cpu_count() or 1, 'memory_mb': memory, 'os': platform.system(),
           'arch': platform.machine(), 'max_jobs': 1,
           'docker': platform.system() == 'Linux' and bool(shutil.which('docker'))}
    if shutil.which('nvidia-smi'):
        try:
            result = subprocess.run(['nvidia-smi', '--query-gpu=name,memory.total', '--format=csv,noheader,nounits'],
                                    capture_output=True, text=True, check=True, timeout=5)
            out['gpus'] = [line.strip() for line in result.stdout.splitlines() if line.strip()][:16]
        except (OSError, subprocess.SubprocessError):
            out['gpus'] = []
    return out


def _run(args, *, data=None, timeout=120):
    return subprocess.run(args, input=data, capture_output=True, timeout=timeout, check=False)


def _path(value):
    if not isinstance(value, str) or not 0 < len(value) < 512 or '\x00' in value:
        raise ValueError('Invalid relative path')
    p = PurePosixPath(value)
    if p.is_absolute() or any(part in ('.', '..') for part in value.split('/')) or str(p) in ('', '.'):
        raise ValueError('Workspace paths must be relative and stay inside /workspace')
    return '/workspace/' + str(p)


class WorkspaceRunner:
    def __init__(self, fetch_artifact=None):
        self.fetch_artifact = fetch_artifact
        self.images = {x.strip() for x in os.getenv('PWM_ALLOWED_IMAGES', 'python:3.12-slim').split(',') if x.strip()}

    @staticmethod
    def _name(wid):
        if not re.fullmatch(r'[a-f0-9]{32}', wid):
            raise ValueError('Invalid workspace ID')
        return 'pwm-' + wid

    def _docker(self, *args, data=None, timeout=120):
        return _run(['docker', *args], data=data, timeout=timeout)

    def _checked(self, args, *, data=None, timeout=120):
        result = self._docker(*args, data=data, timeout=timeout)
        if result.returncode:
            raise RuntimeError(result.stderr.decode('utf-8', 'replace')[:2048] or f'Docker exit {result.returncode}')
        return result.stdout

    def start(self, wid, payload):
        if platform.system() != 'Linux' or not shutil.which('docker'):
            raise RuntimeError('Docker execution requires Linux and Docker')
        image = payload['image']
        if image not in self.images:
            raise ValueError('Container image not approved for this worker')
        cpu, mem = float(payload['cpu']), int(payload['memory_mb'])
        if not 0.1 <= cpu <= 32 or not 64 <= mem <= 131072:
            raise ValueError('Invalid workspace resource request')
        name = self._name(wid)
        self._checked(('volume', 'create', name), timeout=30)
        # Initialize a writable named volume without copying the image directory.
        # Each workspace owns a distinct volume; the unprivileged runtime cannot
        # modify the host or another workspace's files.
        self._checked(('run', '--rm', '--network', 'none', '--cap-drop', 'ALL',
                       '--cap-add', 'CHOWN', '--cap-add', 'FOWNER',
                       '--user', '0', '--mount', f'type=volume,source={name},target=/workspace,volume-nocopy',
                       image, 'sh', '-c', 'chown 65534:65534 /workspace && chmod 0700 /workspace'),
                      timeout=300)
        self._checked(('run', '-d', '--name', name, '--label', 'project-workspace=managed',
                       '--network', 'none', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
                       '--pids-limit', '128', '--cpus', str(cpu), '--memory', f'{mem}m',
                       '--memory-swap', f'{mem}m', '--tmpfs', '/tmp:rw,nosuid,noexec,size=64m',
                       '--user', '65534:65534', '--workdir', '/workspace',
                       '--mount', f'type=volume,source={name},target=/workspace,volume-nocopy',
                       image, 'sh', '-c', 'while :; do sleep 3600; done'), timeout=300)
        try:
            self._checked(('exec', name, 'sh', '-c',
                           'touch /workspace/.pwm-writecheck && rm /workspace/.pwm-writecheck'),
                          timeout=30)
        except Exception:
            self._docker('rm', '-f', name, timeout=30)
            raise
        return {'container': name, 'status': 'ready'}

    def exec(self, wid, command, *, data=None, timeout=120):
        name = self._name(wid)
        if not isinstance(command,str) or not command or len(command)>16000:
            raise ValueError('Invalid command')
        args = ('exec', '-i', name, 'sh', '-c', command) if data is not None else ('exec', name, 'sh', '-c', command)
        result = self._docker(*args, data=data, timeout=timeout)
        return {'exit_code': result.returncode,
                'stdout': result.stdout.decode('utf-8', 'replace')[-131072:],
                'stdout_truncated': len(result.stdout)>131072,
                'stderr': result.stderr.decode('utf-8', 'replace')[-32768:]}

    def handle(self, operation):
        wid = operation['workspace']
        action = operation['action']
        data = operation['payload']
        if action == 'start':
            return self.start(wid, data)
        if action == 'stop':
            name = self._name(wid)
            result = self._docker('rm', '-f', name, timeout=30)
            if result.returncode and b'No such container' not in result.stderr:
                raise RuntimeError(result.stderr.decode('utf-8', 'replace')[:2048])
            # Retain the named volume so unexported work can be recovered.
            return {'status': 'stopped', 'volume': name}
        if action == 'command':
            return self.exec(wid, data.get('command', ''), timeout=120)
        if action == 'read_file':
            path = _path(data.get('path'))
            result = self.exec(wid, 'cat -- ' + shlex.quote(path))
            if result['exit_code']:
                raise ValueError('Unable to read file: ' + result['stderr'][:256])
            if result['stdout_truncated']:raise ValueError('File exceeds 128 KiB read limit')
            return {'path': data['path'], 'content': result['stdout']}
        if action == 'write_file':
            path = _path(data.get('path'))
            body = data.get('content')
            if not isinstance(body,str) or len(body.encode())>131072:
                raise ValueError('File content exceeds 128 KiB')
            expected = data.get('expected_sha256')
            if expected is not None:
                existing = self._docker('exec', self._name(wid), 'sha256sum', '--', path)
                current = existing.stdout.decode().split()[0] if existing.returncode == 0 else None
                if current != expected:raise ValueError('File revision conflict')
            parent = str(PurePosixPath(path).parent)
            script = 'mkdir -p -- ' + shlex.quote(parent) + ' && cat > ' + shlex.quote(path)
            result = self.exec(wid, script, data=body.encode())
            if result['exit_code']:raise ValueError('Unable to write file: ' + result['stderr'][:256])
            return {'path': data['path'], 'sha256': hashlib.sha256(body.encode()).hexdigest()}
        if action == 'list_directory':
            path = _path(data.get('path', 'repos'))
            return self.exec(wid, 'ls -la -- ' + shlex.quote(path))
        if action == 'search':
            query = data.get('query')
            if not isinstance(query,str) or not 0 < len(query) <= 256:raise ValueError('Invalid query')
            result = self.exec(wid, 'grep -R -n -F -- ' + shlex.quote(query) + ' /workspace 2>/dev/null')
            return {'matches': result['stdout'][:32768], 'truncated': len(result['stdout'])>32768}
        if action == 'import_archive':
            if self.fetch_artifact is None:
                raise RuntimeError('Worker artifact transfer is not configured')
            repo = data['repository']
            if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+',repo):
                raise ValueError('Invalid repository name')
            archive = self.fetch_artifact(wid, data['artifact_id'])
            if len(archive)>12*1024*1024:raise ValueError('Repository archive exceeds 12 MiB')
            output = io.BytesIO()
            prefix = 'repos/' + repo.replace('/', '__') + '/'
            total = 0
            with tarfile.open(fileobj=io.BytesIO(archive),mode='r:gz') as source, \
                 tarfile.open(fileobj=output,mode='w') as target:
                for index, member in enumerate(source):
                    if index>=10000:raise ValueError('Archive contains too many files')
                    components = member.name.split('/')[1:]
                    if not components:continue
                    relative='/'.join(components)
                    if member.isdir():continue
                    if not member.isfile() or not relative or any(x in ('', '..', '.') for x in components):
                        raise ValueError('Archive contains an unsupported or unsafe entry')
                    total += member.size
                    if total>32*1024*1024:raise ValueError('Expanded archive exceeds 32 MiB')
                    item=tarfile.TarInfo(prefix+relative)
                    item.mode=0o755 if member.mode & 0o111 else 0o644
                    item.uid=item.gid=65534
                    item.size=member.size
                    stream=source.extractfile(member)
                    if stream is None:raise ValueError('Missing archive file content')
                    target.addfile(item,stream)
            self._checked(('exec',self._name(wid),'mkdir','-p','/workspace/repos'))
            self._checked(('cp','-a','-',self._name(wid)+':/workspace'),data=output.getvalue(),timeout=120)
            return {'repository':repo,'revision':data['revision'],'files_bytes':total}
        if action == 'export_file':
            path = _path(data.get('path'))
            result = self._docker('exec', self._name(wid), 'cat', '--', path, timeout=120)
            if result.returncode:raise ValueError('Unable to export file')
            if len(result.stdout)>1_048_576:raise ValueError('Artifact exceeds 1 MiB limit')
            return {'name': PurePosixPath(path).name, 'base64': base64.b64encode(result.stdout).decode()}
        raise ValueError('Unsupported workspace operation')


def execute(job):
    """Run a one-shot job with no filesystem or network access."""
    if not shutil.which('docker') or platform.system() != 'Linux':
        raise RuntimeError('Docker execution requires Linux and Docker')
    allowed={x.strip() for x in os.getenv('PWM_ALLOWED_IMAGES', 'python:3.12-slim').split(',')}
    if job['image'] not in allowed:raise ValueError('Container image not approved for this worker')
    args=['docker','run','--rm','--network','none','--read-only','--cap-drop','ALL',
          '--security-opt','no-new-privileges','--pids-limit','128','--cpus',str(job['cpu']),
          '--memory',str(job['memory_mb'])+'m','--memory-swap',str(job['memory_mb'])+'m',
          '--tmpfs','/tmp:rw,noexec,nosuid,size=64m','--user','65534:65534',
          job['image'],'sh','-c',job['command']]
    completed=subprocess.run(args,capture_output=True,text=True,timeout=300,check=False)
    return ('succeeded' if completed.returncode==0 else 'failed',(completed.stdout+'\n'+completed.stderr)[-32768:])


def serve(controller,node,token,interval=5):
    if not controller.startswith('https://') and not controller.startswith('http://127.0.0.1:'):
        raise ValueError('Worker connection requires HTTPS or loopback HTTP')
    headers={'Authorization':'Bearer '+token}
    with httpx.Client(base_url=controller,headers=headers,timeout=20) as client:
        def fetch_artifact(wid, artifact_id):
            response=client.get(f'/api/worker/{node}/workspaces/{wid}/artifacts/{artifact_id}',timeout=90)
            response.raise_for_status()
            return response.content

        runner=WorkspaceRunner(fetch_artifact)
        while True:
            caps=capabilities()
            client.post(f'/api/worker/{node}/heartbeat',json={'capabilities':caps}).raise_for_status()
            res=client.post(f'/api/worker/{node}/operation/claim')
            res.raise_for_status()
            operation=res.json()['operation']
            if operation:
                try:
                    output=runner.handle(operation)
                    success=True
                except Exception as exc:
                    output={'error':str(exc)[:2048]}
                    success=False
                reply=client.post(f'/api/worker/{node}/operation/finish',json={
                    'operation_id':operation['id'], 'lease_token':operation['lease_token'],
                    'success':success,'result':output})
                reply.raise_for_status()
                continue
            response=client.post(f'/api/worker/{node}/claim')
            response.raise_for_status()
            job=response.json()['job']
            if job:
                try: status,result=execute(job)
                except Exception as exc:status,result='failed',str(exc)
                client.post(f'/api/worker/{node}/finish',json={
                    'job_id':job['id'],'lease_token':job['lease_token'],
                    'status':status,'result':result}).raise_for_status()
                continue
            time.sleep(interval)
