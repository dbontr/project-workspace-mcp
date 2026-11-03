"""Durable project state and bounded job scheduling."""
import base64
import hashlib
import os
import hmac
import json
import re
import secrets
import sqlite3
import threading
import time
import uuid
from pathlib import Path

from .hardware import validate_devices
from .resources import ResourceStoreMixin
from .uploads import UploadStoreMixin

ID = re.compile(r"^[a-z][a-z0-9-]{1,62}$")


class Store(ResourceStoreMixin, UploadStoreMixin):
    def __init__(self, path):
        self.path = str(path)
        Path(path).parent.mkdir(parents=True, exist_ok=True)
        self.lock = threading.RLock()
        with self.connect() as db:
            db.executescript("""
            PRAGMA journal_mode=WAL;
            CREATE TABLE IF NOT EXISTS projects(id TEXT PRIMARY KEY,title TEXT NOT NULL,created REAL NOT NULL);
            CREATE TABLE IF NOT EXISTS notes(project TEXT NOT NULL,name TEXT NOT NULL,body TEXT NOT NULL,version INTEGER NOT NULL DEFAULT 1,updated REAL NOT NULL,PRIMARY KEY(project,name),FOREIGN KEY(project) REFERENCES projects(id));
            CREATE TABLE IF NOT EXISTS repositories(project TEXT NOT NULL,full_name TEXT NOT NULL,PRIMARY KEY(project,full_name));
            CREATE TABLE IF NOT EXISTS artifacts(id TEXT PRIMARY KEY,project TEXT NOT NULL,name TEXT NOT NULL,sha256 TEXT NOT NULL,size INTEGER NOT NULL,created REAL NOT NULL);
            CREATE TABLE IF NOT EXISTS nodes(id TEXT PRIMARY KEY,token_hash TEXT NOT NULL,capabilities TEXT NOT NULL,last_seen REAL NOT NULL);
            CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,project TEXT NOT NULL,node TEXT,command TEXT NOT NULL,image TEXT NOT NULL,cpu REAL NOT NULL,memory_mb INTEGER NOT NULL,status TEXT NOT NULL,lease_until REAL,token_hash TEXT,created REAL NOT NULL,updated REAL NOT NULL,result TEXT);
            CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status,created);
            CREATE TABLE IF NOT EXISTS pairings(code_hash TEXT PRIMARY KEY,expires REAL NOT NULL);
            CREATE TABLE IF NOT EXISTS workspaces(
                id TEXT PRIMARY KEY,project TEXT NOT NULL,node TEXT NOT NULL,image TEXT NOT NULL,
                cpu REAL NOT NULL,memory_mb INTEGER NOT NULL,status TEXT NOT NULL,
                expires REAL NOT NULL,created REAL NOT NULL,updated REAL NOT NULL,
                FOREIGN KEY(project) REFERENCES projects(id));
            CREATE TABLE IF NOT EXISTS operations(
                id TEXT PRIMARY KEY,workspace TEXT NOT NULL,node TEXT NOT NULL,
                action TEXT NOT NULL,payload TEXT NOT NULL,status TEXT NOT NULL,
                token_hash TEXT,lease_until REAL,result TEXT,created REAL NOT NULL,updated REAL NOT NULL,
                FOREIGN KEY(workspace) REFERENCES workspaces(id));
            CREATE INDEX IF NOT EXISTS idx_operations_node ON operations(node,status,created);
            CREATE TABLE IF NOT EXISTS hardware_leases(
                workspace TEXT NOT NULL,node TEXT NOT NULL,device_id TEXT NOT NULL,kind TEXT NOT NULL,
                acquired REAL NOT NULL,released REAL,
                PRIMARY KEY(workspace,node,device_id), FOREIGN KEY(workspace) REFERENCES workspaces(id));
            CREATE UNIQUE INDEX IF NOT EXISTS idx_device_leases_active
                ON hardware_leases(node,device_id) WHERE released IS NULL;
            CREATE TABLE IF NOT EXISTS environments(
                project TEXT NOT NULL,name TEXT NOT NULL,version INTEGER NOT NULL,
                base_image TEXT NOT NULL,dockerfile TEXT NOT NULL,image TEXT NOT NULL,
                context_name TEXT NOT NULL DEFAULT '',architecture TEXT NOT NULL DEFAULT '',
                node TEXT,status TEXT NOT NULL,image_id TEXT,created REAL NOT NULL,updated REAL NOT NULL,
                PRIMARY KEY(project,name),FOREIGN KEY(project) REFERENCES projects(id));
            CREATE TABLE IF NOT EXISTS environment_exports(
                artifact TEXT PRIMARY KEY,project TEXT NOT NULL,name TEXT NOT NULL,
                version INTEGER NOT NULL,created REAL NOT NULL);
            CREATE TABLE IF NOT EXISTS workspace_snapshots(
                id TEXT PRIMARY KEY,workspace TEXT NOT NULL,artifact TEXT NOT NULL,
                created REAL NOT NULL,FOREIGN KEY(workspace) REFERENCES workspaces(id));
            CREATE TABLE IF NOT EXISTS operation_artifacts(
                operation TEXT NOT NULL, artifact TEXT NOT NULL,
                PRIMARY KEY(operation,artifact), FOREIGN KEY(operation) REFERENCES operations(id));
            CREATE TABLE IF NOT EXISTS artifact_grants(workspace TEXT NOT NULL,artifact TEXT NOT NULL,
                PRIMARY KEY(workspace,artifact));
            CREATE TABLE IF NOT EXISTS workspace_repositories(workspace TEXT NOT NULL,repository TEXT NOT NULL,
                revision TEXT NOT NULL,PRIMARY KEY(workspace,repository));
            """)

    def connect(self):
        db = sqlite3.connect(self.path, timeout=30)
        db.row_factory = sqlite3.Row
        db.execute("PRAGMA foreign_keys=ON")
        db.execute("PRAGMA busy_timeout=30000")
        return db

    def project_create(self, project, title):
        if not ID.fullmatch(project) or not title.strip():
            raise ValueError("Invalid project ID or title")
        with self.lock, self.connect() as db:
            db.execute("INSERT INTO projects VALUES(?,?,?)", (project,title,time.time()))
        return {"id":project,"title":title}

    def projects(self):
        with self.connect() as db:
            return [dict(x) for x in db.execute("SELECT * FROM projects ORDER BY id")]

    def note_put(self, project, name, body, expected_version=None):
        if not ID.fullmatch(name) or len(body.encode()) > 512_000:
            raise ValueError("Invalid note name or size")
        with self.lock, self.connect() as db:
            db.execute("BEGIN IMMEDIATE")
            row = db.execute("SELECT version FROM notes WHERE project=? AND name=?",(project,name)).fetchone()
            actual = row['version'] if row else 0
            if expected_version is None or expected_version != actual:
                raise ValueError(f"Version conflict: expected {expected_version}, actual {actual}")
            if not db.execute("SELECT 1 FROM projects WHERE id=?",(project,)).fetchone():
                raise ValueError("Unknown project")
            db.execute("INSERT INTO notes VALUES(?,?,?,?,?) ON CONFLICT(project,name) DO UPDATE SET body=excluded.body,version=excluded.version,updated=excluded.updated",(project,name,body,actual+1,time.time()))
            return {"project":project,"name":name,"version":actual+1}

    def note_get(self, project, name):
        with self.connect() as db:
            row=db.execute("SELECT * FROM notes WHERE project=? AND name=?",(project,name)).fetchone()
            if not row: raise ValueError("Note not found")
            return dict(row)

    def search(self, project, query):
        with self.connect() as db:
            return [dict(x) for x in db.execute("SELECT project,name,version,substr(body,1,4000) AS excerpt FROM notes WHERE project=? AND (name LIKE ? OR body LIKE ?) LIMIT 30",(project,'%'+query+'%','%'+query+'%'))]

    def repo_link(self, project, full_name):
        if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+",full_name):
            raise ValueError("Invalid repository name")
        with self.connect() as db:
            if not db.execute("SELECT 1 FROM projects WHERE id=?",(project,)).fetchone(): raise ValueError("Unknown project")
            db.execute("INSERT INTO repositories VALUES(?,?) ON CONFLICT DO NOTHING",(project,full_name))
        return {"project":project,"repository":full_name}

    def repos(self, project):
        with self.connect() as db:
            return [r['full_name'] for r in db.execute("SELECT full_name FROM repositories WHERE project=?",(project,))]

    def node_register(self, name, capabilities):
        if not ID.fullmatch(name): raise ValueError("Invalid node ID")
        self._validate_capacity(capabilities)
        token=secrets.token_urlsafe(36)
        digest=hashlib.sha256(token.encode()).hexdigest()
        with self.lock, self.connect() as db:
            db.execute("INSERT INTO nodes VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET token_hash=excluded.token_hash,capabilities=excluded.capabilities,last_seen=excluded.last_seen",(name,digest,json.dumps(capabilities),time.time()))
        return token

    def node_auth(self, name, token):
        with self.connect() as db:
            r=db.execute("SELECT token_hash FROM nodes WHERE id=?",(name,)).fetchone()
            return bool(r and hmac.compare_digest(r['token_hash'],hashlib.sha256(token.encode()).hexdigest()))

    def heartbeat(self,name,capabilities):
        self._validate_capacity(capabilities)
        with self.connect() as db:
            db.execute("UPDATE nodes SET capabilities=?,last_seen=? WHERE id=?",(json.dumps(capabilities),time.time(),name))

    def nodes(self):
        with self.connect() as db:
            return [{"id":r['id'],"capabilities":json.loads(r['capabilities']),"last_seen":r['last_seen'],"online":time.time()-r['last_seen']<90} for r in db.execute("SELECT id,capabilities,last_seen FROM nodes")]

    def job_submit(self, project, command, image='python:3.12-slim',cpu=1,memory_mb=512):
        if not command.strip() or len(command)>16000 or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9./:_@-]{1,200}",image):
            raise ValueError("Invalid job command or image")
        if not (0.1<=cpu<=32 and 64<=memory_mb<=131072): raise ValueError("Resource limits exceeded")
        jid=str(uuid.uuid4())
        with self.connect() as db:
            if not db.execute("SELECT 1 FROM projects WHERE id=?",(project,)).fetchone(): raise ValueError("Unknown project")
            db.execute("INSERT INTO jobs(id,project,command,image,cpu,memory_mb,status,created,updated) VALUES(?,?,?,?,?,?,'queued',?,?)",(jid,project,command,image,cpu,memory_mb,time.time(),time.time()))
        return {"id":jid,"status":"queued"}

    def jobs(self,project):
        with self.connect() as db:
            return [dict(x) for x in db.execute("SELECT id,project,node,status,created,updated,result FROM jobs WHERE project=? ORDER BY created DESC LIMIT 100",(project,))]

    def claim(self,node,capacity):
        with self.lock,self.connect() as db:
            db.execute("BEGIN IMMEDIATE")
            db.execute("UPDATE jobs SET status='expired',updated=? WHERE status='leased' AND lease_until<?",(time.time(),time.time()))
            if not isinstance(capacity,dict) or not capacity.get('docker'):
                return None
            occupied=db.execute("SELECT count(*) n,coalesce(sum(cpu),0) c,coalesce(sum(memory_mb),0) m FROM jobs WHERE node=? AND status='leased'",(node,)).fetchone()
            if occupied['n']>=max(1,int(capacity.get('max_jobs',1))): return None
            reserved=db.execute("SELECT coalesce(sum(cpu),0) c,coalesce(sum(memory_mb),0) m FROM workspaces WHERE node=? AND status IN ('starting','ready','stopping','failed')",(node,)).fetchone()
            free_cpu=max(0,capacity.get('cpu',0)-occupied['c']-reserved['c'])
            free_memory=max(0,capacity.get('memory_mb',0)-occupied['m']-reserved['m'])
            job=db.execute("SELECT * FROM jobs WHERE status='queued' AND cpu<=? AND memory_mb<=? ORDER BY created,id LIMIT 1",(free_cpu,free_memory)).fetchone()
            if not job:return None
            token=secrets.token_urlsafe(32)
            db.execute("UPDATE jobs SET status='leased',node=?,token_hash=?,lease_until=?,updated=? WHERE id=?",(node,hashlib.sha256(token.encode()).hexdigest(),time.time()+420,time.time(),job['id']))
            out=dict(job);out['lease_token']=token
            return out

    def finish(self,node,job_id,token,status,result):
        if status not in ('succeeded','failed'): raise ValueError("Invalid job status")
        with self.lock,self.connect() as db:
            db.execute("BEGIN IMMEDIATE")
            row=db.execute("SELECT * FROM jobs WHERE id=? AND node=? AND status='leased' AND lease_until>?",(job_id,node,time.time())).fetchone()
            if not row or not hmac.compare_digest(row['token_hash'],hashlib.sha256(token.encode()).hexdigest()): raise ValueError("Invalid or expired lease")
            db.execute("UPDATE jobs SET status=?,result=?,updated=?,lease_until=NULL,token_hash=NULL WHERE id=?",(status,result[:32768],time.time(),job_id))
            return {"id":job_id,"status":status}

    @staticmethod
    def _validate_capacity(capacity):
        if not isinstance(capacity, dict):
            raise ValueError('Invalid node capabilities')
        validate_devices(capacity)
        for key in ('cpu', 'memory_mb', 'max_jobs'):
            value = capacity.get(key)
            if not isinstance(value, (int, float)) or isinstance(value, bool) or not 0 < value <= 1_000_000:
                raise ValueError(f'Invalid node capacity: {key}')

    def pairing_create(self, ttl=600):
        code = secrets.token_urlsafe(24)
        with self.connect() as db:
            db.execute('INSERT INTO pairings VALUES (?,?)', (hashlib.sha256(code.encode()).hexdigest(),time.time()+ttl))
            db.execute('DELETE FROM pairings WHERE expires<?', (time.time(),))
        return {'pairing_code': code, 'expires_in_seconds': ttl}

    def pairing_redeem(self, code, node, capabilities):
        self._validate_capacity(capabilities)
        if not ID.fullmatch(node): raise ValueError('Invalid node ID')
        digest=hashlib.sha256(code.encode()).hexdigest()
        with self.lock, self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            matched=db.execute('DELETE FROM pairings WHERE code_hash=? AND expires>? RETURNING code_hash',(digest,time.time())).fetchone()
            if not matched: raise ValueError('Pairing code invalid or expired')
            if db.execute('SELECT 1 FROM nodes WHERE id=?',(node,)).fetchone(): raise ValueError('Node already registered')
            token=secrets.token_urlsafe(36)
            db.execute('INSERT INTO nodes VALUES(?,?,?,?)',(node,hashlib.sha256(token.encode()).hexdigest(),json.dumps(capabilities),time.time()))
        return {'node_id':node,'worker_token':token}

    def workspaces(self, project):
        with self.connect() as db:
            return [dict(r) for r in db.execute('SELECT * FROM workspaces WHERE project=? ORDER BY created DESC',(project,))]

    def workspace_get(self, project, wid):
        with self.connect() as db:
            row=db.execute('SELECT * FROM workspaces WHERE id=? AND project=?',(wid,project)).fetchone()
            if not row: raise ValueError('Workspace not found')
            return dict(row)

    def _enqueue(self, db, wid, node, action, payload):
        op_id=uuid.uuid4().hex
        db.execute('INSERT INTO operations(id,workspace,node,action,payload,status,created,updated) VALUES(?,?,?,?,?,\'queued\',?,?)',
                   (op_id,wid,node,action,json.dumps(payload),time.time(),time.time()))
        return op_id

    def workspace_call(self, project, wid, action, payload):
        allowed={'command','read_file','write_file','list_directory','search','export_file','snapshot'}
        if action not in allowed or not isinstance(payload,dict): raise ValueError('Unsupported workspace operation')
        if len(json.dumps(payload).encode())>262144: raise ValueError('Operation payload too large')
        with self.lock,self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            row=db.execute('SELECT * FROM workspaces WHERE id=? AND project=?',(wid,project)).fetchone()
            if not row or row['status']!='ready' or row['expires']<=time.time(): raise ValueError('Workspace is not ready')
            queued=db.execute("SELECT count(*) n FROM operations WHERE workspace=? AND status IN ('queued','leased')",(wid,)).fetchone()['n']
            if queued>=32:raise ValueError('Workspace operation queue is full')
            return {'operation_id':self._enqueue(db,wid,row['node'],action,payload),'status':'queued'}

    def workspace_stop(self, project, wid):
        with self.lock,self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            row=db.execute('SELECT * FROM workspaces WHERE id=? AND project=?',(wid,project)).fetchone()
            if not row: raise ValueError('Workspace not found')
            if row['status'] in ('stopping','stopped','expired'): return {'status':row['status']}
            if row['status'] not in ('ready','failed'): raise ValueError('Workspace cannot be stopped')
            db.execute("UPDATE workspaces SET status='stopping',updated=? WHERE id=?",(time.time(),wid))
            db.execute("UPDATE operations SET status='cancelled',updated=? WHERE workspace=? AND status='queued'",(time.time(),wid))
            return {'status':'stopping','operation_id':self._enqueue(db,wid,row['node'],'stop',{})}

    def workspace_operation(self, project, wid, op_id):
        with self.connect() as db:
            row=db.execute('''SELECT o.id,o.workspace,o.action,o.status,o.result,o.created,o.updated
                              FROM operations o JOIN workspaces w ON o.workspace=w.id
                              WHERE o.id=? AND w.id=? AND w.project=?''',(op_id,wid,project)).fetchone()
            if not row:raise ValueError('Operation not found')
            result=dict(row)
            if result['result'] is not None:result['result']=json.loads(result['result'])
            return result

    def _expire(self, db, now):
        db.execute("UPDATE operations SET status='expired',token_hash=NULL,updated=? WHERE status='leased' AND lease_until<?",(now,now))
        db.execute("UPDATE workspaces SET status='failed',updated=? WHERE status='starting' AND EXISTS (SELECT 1 FROM operations WHERE workspace=workspaces.id AND action='start' AND status='expired')",(now,))
        expired=db.execute("SELECT id,node FROM workspaces WHERE expires<? AND status IN ('starting','ready')",(now,)).fetchall()
        for r in expired:
            db.execute("UPDATE operations SET status='cancelled',updated=? WHERE workspace=? AND status='queued'",(now,r['id']))
            db.execute("UPDATE workspaces SET status='stopping',updated=? WHERE id=?",(now,r['id']))
            self._enqueue(db,r['id'],r['node'],'stop',{})

    def operation_claim(self, node):
        with self.lock,self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            now=time.time()
            self._expire(db,now)
            row=db.execute("""SELECT * FROM operations o WHERE node=? AND status='queued'
                              AND NOT EXISTS (SELECT 1 FROM operations x WHERE x.workspace=o.workspace AND x.status='leased')
                              ORDER BY created,id LIMIT 1""",(node,)).fetchone()
            if not row:return None
            token=secrets.token_urlsafe(32)
            db.execute("UPDATE operations SET status='leased',token_hash=?,lease_until=?,updated=? WHERE id=?",(hashlib.sha256(token.encode()).hexdigest(),now+420,now,row['id']))
            return {'id':row['id'],'workspace':row['workspace'],'action':row['action'],
                    'payload':json.loads(row['payload']),'lease_token':token}

    def operation_finish(self, node, op_id, token, success, output):
        if not isinstance(success,bool) or not isinstance(output,dict):raise ValueError('Invalid operation result')
        if len(json.dumps(output).encode())>3_000_000:raise ValueError('Operation output too large')
        with self.lock,self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            op=db.execute("SELECT * FROM operations WHERE id=? AND node=? AND status='leased' AND lease_until>?",(op_id,node,time.time())).fetchone()
            if not op or not hmac.compare_digest(op['token_hash'],hashlib.sha256(token.encode()).hexdigest()):
                raise ValueError('Invalid or expired operation lease')
            ws=db.execute('SELECT * FROM workspaces WHERE id=?',(op['workspace'],)).fetchone()
            if op['action']=='import_archive' and success:
                payload=json.loads(op['payload'])
                db.execute('INSERT INTO workspace_repositories VALUES(?,?,?) ON CONFLICT(workspace,repository) DO UPDATE SET revision=excluded.revision',
                           (ws['id'],payload['repository'],payload['revision']))
            if op['action'] in ('export_file','snapshot') and success and 'base64' in output:
                encoded=output.get('base64')
                if not isinstance(encoded,str):raise ValueError('Missing artifact payload')
                try:data=base64.b64decode(encoded,validate=True)
                except Exception as exc:raise ValueError('Invalid artifact encoding') from exc
                if len(data)>1_048_576:raise ValueError('Artifact exceeds 1 MiB limit')
                artifact=self._artifact_write(db,ws['project'],output.get('name','output'),data,op_id)
                output={'artifact':artifact}
            if op['action'] in ('export_file','snapshot','export_image') and success and 'artifact_id' in output:
                linked=db.execute('SELECT 1 FROM operation_artifacts WHERE operation=? AND artifact=?',
                                  (op_id,output['artifact_id'])).fetchone()
                if not linked:raise ValueError('Unverified operation artifact')
                artifact=db.execute('SELECT id,name,sha256,size FROM artifacts WHERE id=? AND project=?',
                                    (output['artifact_id'],ws['project'])).fetchone()
                if not artifact:raise ValueError('Missing uploaded artifact')
                output={'artifact':dict(artifact)}
            if op['action']=='export_image' and success:
                payload=json.loads(op['payload'])
                profile=db.execute('SELECT * FROM environments WHERE project=? AND name=?',
                                   (ws['project'],payload['name'])).fetchone()
                if not profile or profile['version']!=payload['version']:
                    raise ValueError('Environment revision changed during export')
                if 'artifact' not in output:raise ValueError('Image export needs a durable artifact')
                db.execute('INSERT INTO environment_exports VALUES(?,?,?,?,?)',
                           (output['artifact']['id'],ws['project'],payload['name'],
                            payload['version'],time.time()))
            if op['action']=='load_image' and success:
                payload=json.loads(op['payload'])
                profile=db.execute('SELECT * FROM environments WHERE project=? AND name=?',
                                   (ws['project'],payload['name'])).fetchone()
                if not profile or profile['version']!=payload['version']:
                    raise ValueError('Environment revision changed during import')
                if output.get('image')!=profile['image'] or not output.get('image_id'):
                    raise ValueError('Loaded image identity mismatch')
                db.execute("UPDATE environments SET status='ready',node=?,image_id=?,updated=? WHERE project=? AND name=?",
                           (ws['node'],output['image_id'],time.time(),ws['project'],payload['name']))
            if op['action']=='snapshot' and success:
                if 'artifact' not in output:raise ValueError('Snapshot needs a durable artifact')
                db.execute('INSERT INTO workspace_snapshots VALUES(?,?,?,?)',
                           (uuid.uuid4().hex,ws['id'],output['artifact']['id'],time.time()))
            if op['action']=='build_image':
                payload=json.loads(op['payload'])
                profile=db.execute('SELECT * FROM environments WHERE project=? AND name=?',
                                   (ws['project'],payload['name'])).fetchone()
                if profile and profile['version']==payload['version'] and profile['node']==ws['node']:
                    if success and output.get('image')==profile['image'] and output.get('image_id'):
                        db.execute("UPDATE environments SET status='ready',image_id=?,updated=? WHERE project=? AND name=?",
                                   (output['image_id'],time.time(),ws['project'],payload['name']))
                    else:
                        success=False
                        db.execute("UPDATE environments SET status='failed',updated=? WHERE project=? AND name=?",
                                   (time.time(),ws['project'],payload['name']))
            status='succeeded' if success else 'failed' 
            db.execute('UPDATE operations SET status=?,result=?,updated=?,token_hash=NULL WHERE id=?',
                       (status,json.dumps(output),time.time(),op_id))
            if op['action']=='start':
                db.execute('UPDATE workspaces SET status=?,updated=? WHERE id=?',('ready' if success else 'failed',time.time(),ws['id']))
            elif op['action']=='stop':
                db.execute('UPDATE workspaces SET status=?,updated=? WHERE id=?',('stopped' if success else 'failed',time.time(),ws['id']))
                if success:
                    db.execute('UPDATE hardware_leases SET released=? WHERE workspace=? AND released IS NULL',
                               (time.time(),ws['id']))
            return {'id':op_id,'status':status,'result':output}

    def _artifact_write(self,db,project,name,data,source):
        if not isinstance(name,str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,127}',name):
            raise ValueError('Invalid artifact name')
        digest=hashlib.sha256(data).hexdigest()
        artifact_id=uuid.uuid4().hex
        directory=Path(self.path).parent/'artifacts'
        directory.mkdir(parents=True,exist_ok=True)
        target=directory/artifact_id
        fd=os.open(target,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
        try:
            with os.fdopen(fd,'wb') as f:
                f.write(data)
                f.flush()
                os.fsync(f.fileno())
        except BaseException:
            target.unlink(missing_ok=True)
            raise
        db.execute('INSERT INTO artifacts VALUES(?,?,?,?,?,?)',(artifact_id,project,name,digest,len(data),time.time()))
        return {'id':artifact_id,'name':name,'sha256':digest,'size':len(data),'source':source}

    def artifacts(self,project):
        with self.connect() as db:
            return [dict(r) for r in db.execute('SELECT * FROM artifacts WHERE project=? ORDER BY created DESC',(project,))]

    def artifact_info(self,project,artifact_id):
        with self.connect() as db:
            row=db.execute('SELECT * FROM artifacts WHERE id=? AND project=?',
                           (artifact_id,project)).fetchone()
            if not row:raise ValueError('Artifact not found')
            return dict(row),Path(self.path).parent/'artifacts'/artifact_id

    def artifact_get(self,project,artifact_id):
        with self.connect() as db:
            row=db.execute('SELECT * FROM artifacts WHERE id=? AND project=?',(artifact_id,project)).fetchone()
            if not row:raise ValueError('Artifact not found')
            meta=dict(row)
        data=(Path(self.path).parent/'artifacts'/artifact_id).read_bytes()
        if hashlib.sha256(data).hexdigest()!=meta['sha256']:raise ValueError('Artifact checksum mismatch')
        return meta,data

    def status(self):
        with self.connect() as db:
            return {name:db.execute('SELECT count(*) n FROM '+name).fetchone()['n'] for name in ('projects','nodes','jobs','workspaces','artifacts')}

    def artifact_save(self, project, name, data):
        if not isinstance(data, bytes) or len(data)>12*1024*1024:
            raise ValueError('Artifact exceeds 12 MiB import limit')
        with self.lock,self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            if not db.execute('SELECT 1 FROM projects WHERE id=?',(project,)).fetchone():
                raise ValueError('Unknown project')
            return self._artifact_write(db,project,name,data,'github')

    def workspace_import_file(self, project, wid, artifact_id, destination):
        """Authorize a specific controller artifact for one active workspace."""
        if (not isinstance(destination, str) or len(destination) > 511 or not destination
                or destination.startswith('/') or any(part in ('', '.', '..')
                for part in destination.split('/')) or any(c in destination for c in ('\x00', '\r', '\n'))):
            raise ValueError('Invalid workspace destination')
        with self.lock, self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            ws = db.execute("SELECT * FROM workspaces WHERE id=? AND project=? AND status='ready' AND expires>?",
                            (wid, project, time.time())).fetchone()
            if not ws:
                raise ValueError('Workspace is not ready')
            if not db.execute('SELECT 1 FROM artifacts WHERE id=? AND project=?',
                              (artifact_id, project)).fetchone():
                raise ValueError('Artifact not found')
            db.execute('INSERT OR IGNORE INTO artifact_grants VALUES(?,?)', (wid, artifact_id))
            return {'operation_id': self._enqueue(db, wid, ws['node'], 'import_file',
                    {'artifact_id': artifact_id, 'path': destination}), 'status': 'queued'}

    def workspace_import(self, project, wid, repository, revision, artifact_id):
        with self.lock,self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            ws=db.execute("SELECT * FROM workspaces WHERE id=? AND project=? AND status='ready' AND expires>?",
                          (wid,project,time.time())).fetchone()
            if not ws:raise ValueError('Workspace is not ready')
            if not db.execute('SELECT 1 FROM repositories WHERE project=? AND full_name=?',(project,repository)).fetchone():
                raise ValueError('Repository not linked to project')
            if not db.execute('SELECT 1 FROM artifacts WHERE project=? AND id=?',(project,artifact_id)).fetchone():
                raise ValueError('Artifact not found')
            db.execute('INSERT INTO artifact_grants VALUES(?,?) ON CONFLICT DO NOTHING',(wid,artifact_id))
            return {'operation_id':self._enqueue(db,wid,ws['node'],'import_archive',
                    {'artifact_id':artifact_id,'repository':repository,'revision':revision}), 'status':'queued'}

    def worker_artifact_info(self,node,wid,artifact_id):
        with self.connect() as db:
            row=db.execute("SELECT w.project FROM workspaces w JOIN artifact_grants a "
                           "ON a.workspace=w.id WHERE w.id=? AND w.node=? AND a.artifact=?",
                           (wid,node,artifact_id)).fetchone()
            if not row:raise ValueError('Worker is not authorized to fetch this artifact')
            return self.artifact_info(row['project'],artifact_id)

    def worker_artifact(self,node,wid,artifact_id):
        with self.connect() as db:
            row=db.execute('''SELECT w.project FROM workspaces w JOIN artifact_grants a ON a.workspace=w.id
                              WHERE w.id=? AND w.node=? AND a.artifact=?''',(wid,node,artifact_id)).fetchone()
            if not row:raise ValueError('Worker is not authorized to fetch this artifact')
        return self.artifact_get(row['project'],artifact_id)

    def workspace_repositories(self,project,wid):
        self.workspace_get(project,wid)
        with self.connect() as db:
            return [dict(r) for r in db.execute('SELECT repository,revision FROM workspace_repositories WHERE workspace=?',(wid,))]
