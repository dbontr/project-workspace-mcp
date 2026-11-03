"""Project environment definitions and exclusive hardware allocation."""

import json
import re
import time
import uuid

from .hardware import choose_devices, validate_devices, validate_request

NAME = re.compile(r'^[a-z][a-z0-9-]{1,62}$')
IMAGE = re.compile(r'^[A-Za-z0-9][A-Za-z0-9./:_@-]{1,200}$')


class ResourceStoreMixin:
    def hardware_inventory(self):
        with self.connect() as db:
            nodes = db.execute('SELECT id,capabilities,last_seen FROM nodes ORDER BY id').fetchall()
            leases = {(r['node'], r['device_id']): r['workspace'] for r in db.execute(
                'SELECT node,device_id,workspace FROM hardware_leases WHERE released IS NULL')}
            return [{'node': r['id'], 'online': time.time() - r['last_seen'] < 90,
                     'id': d['id'], 'kind': d['kind'], 'model': d.get('model', ''),
                     'memory_mb': d.get('memory_mb', 0),
                     'leased_to': leases.get((r['id'], d['id']))}
                    for r in nodes for d in validate_devices(json.loads(r['capabilities']))]

    def workspace_create(self, project, image='python:3.12-slim', cpu=1, memory_mb=512,
                         duration_minutes=120, hardware=None, environment=None):
        if not isinstance(image, str) or not IMAGE.fullmatch(image):
            raise ValueError('Invalid image')
        if (not 0.1 <= cpu <= 32 or not 64 <= memory_mb <= 131072 or
                not 1 <= duration_minutes <= 1440):
            raise ValueError('Workspace limits exceeded')
        request = validate_request(hardware)
        if environment is not None and (not isinstance(environment, str) or not NAME.fullmatch(environment)):
            raise ValueError('Invalid environment name')
        now = time.time()
        with self.lock, self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            if not db.execute('SELECT 1 FROM projects WHERE id=?', (project,)).fetchone():
                raise ValueError('Unknown project')
            preferred_node = None
            if environment is not None:
                profile = db.execute('SELECT * FROM environments WHERE project=? AND name=?',
                                     (project, environment)).fetchone()
                if not profile or profile['status'] != 'ready':
                    raise ValueError('Environment is not ready')
                image = profile['image']
                preferred_node = profile['node']
            nodes = db.execute('SELECT id,capabilities FROM nodes WHERE last_seen>? ORDER BY id',
                               (now-90,)).fetchall()
            selected = None
            for node in nodes:
                if preferred_node is not None and node['id'] != preferred_node:
                    continue
                caps = json.loads(node['capabilities'])
                if environment is not None and profile['architecture']:
                    arch={'x86_64':'amd64','aarch64':'arm64'}.get(caps.get('arch'),caps.get('arch'))
                    if arch!=profile['architecture']:
                        continue
                if not caps.get('docker') or caps.get('cpu', 0) < cpu or caps.get('memory_mb', 0) < memory_mb:
                    continue
                running = db.execute("SELECT coalesce(sum(cpu),0) c,coalesce(sum(memory_mb),0) m FROM workspaces WHERE node=? AND status IN ('starting','ready','stopping','failed')", (node['id'],)).fetchone()
                jobs = db.execute("SELECT coalesce(sum(cpu),0) c,coalesce(sum(memory_mb),0) m FROM jobs WHERE node=? AND status='leased'", (node['id'],)).fetchone()
                if running['c'] + jobs['c'] + cpu > caps['cpu'] or running['m'] + jobs['m'] + memory_mb > caps['memory_mb']:
                    continue
                busy = {r['device_id'] for r in db.execute(
                    'SELECT device_id FROM hardware_leases WHERE node=? AND released IS NULL',
                    (node['id'],))}
                devices = choose_devices(validate_devices(caps), busy, request)
                if devices is not None:
                    selected = node['id'], devices
                    break
            if selected is None:
                raise ValueError('No online worker with available capacity and hardware')
            node_id, devices = selected
            wid = uuid.uuid4().hex
            db.execute('INSERT INTO workspaces VALUES(?,?,?,?,?,?,?,?,?,?)',
                       (wid, project, node_id, image, cpu, memory_mb, 'starting',
                        now + duration_minutes * 60, now, now))
            for device in devices:
                db.execute('INSERT INTO hardware_leases VALUES(?,?,?,?,?,NULL)',
                           (wid, node_id, device['id'], device['kind'], now))
            self._enqueue(db, wid, node_id, 'start', {
                'image': image, 'cpu': cpu, 'memory_mb': memory_mb,
                'devices': [{'id': d['id'], 'kind': d['kind']} for d in devices],
            })
            return {'id': wid, 'project': project, 'node': node_id, 'status': 'starting',
                    'expires': now + duration_minutes * 60,
                    'hardware': [{'id': d['id'], 'kind': d['kind']} for d in devices],
                    'environment': environment}

    def environment_define(self, project, name, base_image, dockerfile='', expected_version=0,
                           context_name='', architecture=''):
        if (not isinstance(name, str) or not NAME.fullmatch(name)
                or not isinstance(base_image, str) or not IMAGE.fullmatch(base_image)
                or not isinstance(dockerfile, str) or len(dockerfile.encode()) > 16384
                or (context_name and (not isinstance(context_name,str) or not NAME.fullmatch(context_name)))
                or architecture not in ('', 'amd64','arm64')):
            raise ValueError('Invalid environment definition')
        if dockerfile:
            directives = [line.strip() for line in dockerfile.splitlines()
                          if line.strip() and not line.lstrip().startswith('#')]
            from_lines = [line.split() for line in directives if line.split()[0].upper() == 'FROM']
            if (not directives or directives[0].split()[0].upper() != 'FROM'
                    or len(from_lines) != 1 or len(from_lines[0]) != 2
                    or from_lines[0][1] != base_image):
                raise ValueError('Dockerfile must start with a single FROM matching base_image')
        with self.lock, self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            if not db.execute('SELECT 1 FROM projects WHERE id=?', (project,)).fetchone():
                raise ValueError('Unknown project')
            current = db.execute('SELECT version FROM environments WHERE project=? AND name=?',
                                 (project, name)).fetchone()
            version = current['version'] if current else 0
            if expected_version != version:
                raise ValueError('Environment version conflict')
            target = f'pwm-env-{project}-{name}:v{version + 1}'
            now = time.time()
            db.execute('''INSERT INTO environments(
                project,name,version,base_image,dockerfile,image,context_name,architecture,node,status,image_id,created,updated)
                VALUES(?,?,?,?,?,?,?,?,NULL,'defined',NULL,?,?)
                ON CONFLICT(project,name) DO UPDATE SET version=excluded.version,
                base_image=excluded.base_image,dockerfile=excluded.dockerfile,
                image=excluded.image,context_name=excluded.context_name,
                architecture=excluded.architecture,node=NULL,status='defined',image_id=NULL,
                updated=excluded.updated''',
                       (project, name, version+1, base_image, dockerfile, target,context_name,architecture,now,now))
            return {'project': project, 'name': name, 'version': version+1,
                    'image': target, 'status': 'defined', 'context':context_name,
                    'architecture':architecture}

    def environments(self, project):
        with self.connect() as db:
            return [dict(r) for r in db.execute('''SELECT project,name,version,base_image,
                image,context_name,architecture,node,status,image_id,created,updated FROM environments
                WHERE project=? ORDER BY name''', (project,))]

    def environment_build(self, project, name, wid):
        with self.lock, self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            workspace = db.execute('SELECT * FROM workspaces WHERE id=? AND project=?',
                                   (wid, project)).fetchone()
            profile = db.execute('SELECT * FROM environments WHERE project=? AND name=?',
                                 (project, name)).fetchone()
            if not profile:
                raise ValueError('Environment not found')
            if not workspace or workspace['status'] != 'ready':
                raise ValueError('Workspace is not ready')
            if profile['architecture']:
                node=db.execute('SELECT capabilities FROM nodes WHERE id=?',(workspace['node'],)).fetchone()
                node_arch=json.loads(node['capabilities']).get('arch','') if node else ''
                node_arch={'x86_64':'amd64','aarch64':'arm64'}.get(node_arch,node_arch)
                if node_arch!=profile['architecture']:
                    raise ValueError('Environment architecture does not match worker')
            if profile['status'] == 'building':
                raise ValueError('Environment build in progress')
            if profile['status'] == 'ready':
                raise ValueError('Environment already built; update its version to rebuild')
            payload = {'name': name, 'version': profile['version'], 'image': profile['image'],
                       'base_image': profile['base_image'], 'dockerfile': profile['dockerfile'],
                       'context':profile['context_name'],'architecture':profile['architecture']}
            op_id = self._enqueue(db, wid, workspace['node'], 'build_image', payload)
            db.execute("UPDATE environments SET status='building',node=?,updated=? WHERE project=? AND name=?",
                       (workspace['node'], time.time(), project, name))
            return {'operation_id': op_id, 'image': profile['image'], 'status': 'queued'}

    def environment_export(self, project, name, wid):
        with self.lock, self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            ws=db.execute("SELECT * FROM workspaces WHERE id=? AND project=? AND status='ready'",
                          (wid,project)).fetchone()
            profile=db.execute('SELECT * FROM environments WHERE project=? AND name=?',
                               (project,name)).fetchone()
            if not ws or not profile or profile['status']!='ready' or ws['node']!=profile['node']:
                raise ValueError('Environment image is not ready on this node')
            op=self._enqueue(db,wid,ws['node'],'export_image',
                             {'name':name,'version':profile['version'],'image':profile['image']})
            return {'operation_id':op,'status':'queued'}

    def environment_import(self, project, name, wid, artifact_id):
        with self.lock, self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            ws=db.execute("SELECT * FROM workspaces WHERE id=? AND project=? AND status='ready'",
                          (wid,project)).fetchone()
            profile=db.execute('SELECT * FROM environments WHERE project=? AND name=?',
                               (project,name)).fetchone()
            saved=db.execute('SELECT * FROM environment_exports WHERE artifact=? AND project=? AND name=?',
                             (artifact_id,project,name)).fetchone()
            if not ws or not profile:raise ValueError('Workspace or environment is not ready')
            if not saved or saved['version']!=profile['version']:
                raise ValueError('Export is not associated with this environment version')
            db.execute('INSERT OR IGNORE INTO artifact_grants VALUES(?,?)',(wid,artifact_id))
            op=self._enqueue(db,wid,ws['node'],'load_image',
                             {'name':name,'version':profile['version'],'image':profile['image'],
                              'artifact_id':artifact_id})
            return {'operation_id':op,'status':'queued'}

    def workspace_snapshots(self, project, wid):
        self.workspace_get(project, wid)
        with self.connect() as db:
            return [dict(x) for x in db.execute('''SELECT s.id,s.workspace,s.artifact,s.created
                FROM workspace_snapshots s WHERE s.workspace=? ORDER BY s.created DESC''', (wid,))]

    def workspace_restore(self, project, wid, snapshot_id):
        with self.lock, self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            ws = db.execute('SELECT * FROM workspaces WHERE id=? AND project=?', (wid, project)).fetchone()
            snap = db.execute('''SELECT s.artifact FROM workspace_snapshots s
                JOIN workspaces w ON w.id=s.workspace WHERE s.id=? AND w.project=?''',
                              (snapshot_id, project)).fetchone()
            if not ws or ws['status'] != 'ready':
                raise ValueError('Workspace is not ready')
            if not snap:
                raise ValueError('Snapshot not found')
            queued = db.execute("SELECT count(*) n FROM operations WHERE workspace=? AND status IN ('queued','leased')", (wid,)).fetchone()['n']
            if queued >= 32:
                raise ValueError('Workspace operation queue is full')
            db.execute('INSERT OR IGNORE INTO artifact_grants VALUES(?,?)', (wid, snap['artifact']))
            op_id = self._enqueue(db, wid, ws['node'], 'restore_snapshot',
                                  {'artifact_id': snap['artifact']})
            return {'operation_id': op_id, 'status': 'queued'}
