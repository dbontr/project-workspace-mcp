"""Durable, lease-bound streaming artifact transfers and operation renewal."""

import hashlib
import hmac
import os
import re
import time
import uuid
from pathlib import Path


class UploadStoreMixin:
    def operation_renew(self, node, op_id, token):
        with self.lock, self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            row = db.execute('SELECT o.token_hash,o.lease_until,w.expires FROM operations o '
                             'JOIN workspaces w ON w.id=o.workspace '
                             "WHERE o.id=? AND o.node=? AND o.status='leased'", (op_id,node)).fetchone()
            if not row or row['lease_until'] <= time.time() or not hmac.compare_digest(
                    row['token_hash'], hashlib.sha256(token.encode()).hexdigest()):
                raise ValueError('Invalid or expired operation lease')
            until = min(time.time() + 420, row['expires'] + 120)
            if until <= time.time():
                raise ValueError('Workspace lease expired')
            db.execute('UPDATE operations SET lease_until=?,updated=? WHERE id=?',
                       (until,time.time(),op_id))
            return {'lease_until': until}

    @staticmethod
    def _upload_row(db, node, wid, op_id, token):
        row = db.execute('SELECT w.project,o.action,o.lease_until,o.token_hash FROM operations o '
                         'JOIN workspaces w ON w.id=o.workspace '
                         "WHERE o.id=? AND o.workspace=? AND o.node=? AND o.status='leased'",
                         (op_id,wid,node)).fetchone()
        if not row or row['action'] not in ('export_file','snapshot','export_image') or row['lease_until'] <= time.time() \
                or not hmac.compare_digest(row['token_hash'],hashlib.sha256(token.encode()).hexdigest()):
            raise ValueError('Invalid upload lease')
        return row

    def upload_authorize(self, node, wid, op_id, token):
        with self.connect() as db:
            return self._upload_row(db,node,wid,op_id,token)['project']

    def upload_commit(self, node, wid, op_id, token, name, temp_path, size, digest):
        if not isinstance(name,str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,127}',name):
            raise ValueError('Invalid artifact name')
        artifact_id = uuid.uuid4().hex
        target = Path(self.path).parent/'artifacts'/artifact_id
        target.parent.mkdir(parents=True, exist_ok=True)
        with self.lock, self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            row = self._upload_row(db,node,wid,op_id,token)
            if db.execute('SELECT 1 FROM operation_artifacts WHERE operation=?',(op_id,)).fetchone():
                raise ValueError('Operation already has an uploaded artifact')
            os.replace(temp_path,target)
            db.execute('INSERT INTO artifacts VALUES(?,?,?,?,?,?)',
                       (artifact_id,row['project'],name,digest,size,time.time()))
            db.execute('INSERT INTO operation_artifacts VALUES(?,?)',(op_id,artifact_id))
            return {'id':artifact_id,'name':name,'sha256':digest,'size':size}

    def artifact_import_stream(self, project, name, temp_path, size, digest):
        """Register a fully streamed and checksum-verified administrator upload."""
        if not isinstance(name, str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]{0,127}', name):
            raise ValueError('Invalid artifact name')
        if size < 0 or not re.fullmatch(r'[0-9a-f]{64}', digest):
            raise ValueError('Invalid artifact metadata')
        artifact_id = uuid.uuid4().hex
        target = Path(self.path).parent / 'artifacts' / artifact_id
        target.parent.mkdir(parents=True, exist_ok=True)
        with self.lock, self.connect() as db:
            db.execute('BEGIN IMMEDIATE')
            if not db.execute('SELECT 1 FROM projects WHERE id=?', (project,)).fetchone():
                raise ValueError('Unknown project')
            os.replace(temp_path, target)
            try:
                db.execute('INSERT INTO artifacts VALUES(?,?,?,?,?,?)',
                           (artifact_id, project, name, digest, size, time.time()))
            except BaseException:
                target.unlink(missing_ok=True)
                raise
        return {'id': artifact_id, 'name': name, 'sha256': digest, 'size': size}
