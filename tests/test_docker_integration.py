"""Actual Linux Docker workspace lifecycle. Opt-in on a trusted CI runner."""
import os
import shutil
import subprocess

import pytest

from project_workspace.core import Store
from project_workspace.worker import WorkspaceRunner

pytestmark = pytest.mark.skipif(
    os.getenv('RUN_DOCKER_INTEGRATION') != '1' or not shutil.which('docker'),
    reason='requires an explicitly enabled Linux Docker test runner',
)


def test_checkout_edit_execute_export_release(tmp_path):
    store=Store(tmp_path/'state.db')
    store.project_create('demo','Demo')
    store.node_register('worker-one',{'cpu':2,'memory_mb':3072,'max_jobs':1,'docker':True})
    runner=WorkspaceRunner()
    ws=store.workspace_create('demo',cpu=1,memory_mb=512,duration_minutes=10)
    wid=ws['id']
    volume='pwm-'+wid

    def perform_next():
        operation=store.operation_claim('worker-one')
        assert operation is not None
        try:
            result=runner.handle(operation)
            status=True
        except Exception as exc:
            result={'error':str(exc)}
            status=False
        finished=store.operation_finish('worker-one',operation['id'],operation['lease_token'],status,result)
        assert status, result
        return finished['result']

    try:
        perform_next()
        assert store.workspace_get('demo',wid)['status']=='ready'
        store.workspace_call('demo',wid,'write_file',{'path':'src/main.py','content':'print(3 * 7)\n'})
        written=perform_next()
        assert 'sha256' in written

        store.workspace_call('demo',wid,'read_file',{'path':'src/main.py'})
        assert perform_next()['content']=='print(3 * 7)\n'

        store.workspace_call('demo',wid,'command',{'command':'python src/main.py'})
        completed=perform_next()
        assert completed['exit_code']==0 and completed['stdout'].strip()=='21'

        store.workspace_call('demo',wid,'export_file',{'path':'src/main.py'})
        artifact=perform_next()['artifact']
        assert store.artifact_get('demo',artifact['id'])[1]==b'print(3 * 7)\n'

        store.workspace_stop('demo',wid)
        perform_next()
        assert store.workspace_get('demo',wid)['status']=='stopped'
    finally:
        subprocess.run(['docker','rm','-f',volume],capture_output=True,check=False)
        subprocess.run(['docker','volume','rm',volume],capture_output=True,check=False)


def test_controller_worker_http_lease_and_artifact_flow(tmp_path, monkeypatch):
    """Exercise an independently authenticated worker through controller HTTP routes."""
    import importlib
    import sys
    from fastapi.testclient import TestClient

    token='test-admin-'+'a'*48
    monkeypatch.setenv('PWM_ADMIN_TOKEN',token)
    monkeypatch.setenv('PWM_DB',str(tmp_path/'controller.db'))
    sys.modules.pop('project_workspace.server',None)
    server=importlib.import_module('project_workspace.server')
    admin={'Authorization':'Bearer '+token}
    runner=WorkspaceRunner()
    wid=None

    with TestClient(server.app,base_url='http://localhost') as client:
        created=client.post('/api/projects',headers=admin,
                            json={'id':'integration','title':'Integration project'})
        assert created.status_code==200
        pairing=client.post('/api/pairing',headers=admin).json()['pairing_code']
        joined=client.post('/api/pair',json={'pairing_code':pairing,'name':'worker-one',
                             'capabilities':{'cpu':2,'memory_mb':3072,'max_jobs':1,'docker':True}})
        assert joined.status_code==200
        worker={'Authorization':'Bearer '+joined.json()['worker_token']}
        assert client.get('/api/status',headers=worker).status_code==401
        created=client.post('/api/workspaces',headers=admin,
                            json={'project_id':'integration','cpu':1,'memory_mb':512})
        assert created.status_code==200,created.text
        wid=created.json()['id']

        def execute_http_operation():
            leased=client.post('/api/worker/worker-one/operation/claim',headers=worker)
            assert leased.status_code==200,leased.text
            operation=leased.json()['operation']
            assert operation and operation['workspace']==wid
            output=runner.handle(operation)
            done=client.post('/api/worker/worker-one/operation/finish',headers=worker,json={
                'operation_id':operation['id'],'lease_token':operation['lease_token'],
                'success':True,'result':output})
            assert done.status_code==200,done.text
            assert done.json()['status']=='succeeded'
            return done.json()['result']

        try:
            execute_http_operation()
            submitted=client.post(f'/api/projects/integration/workspaces/{wid}/operations',headers=admin,
                                  json={'action':'write_file','payload':{
                                      'path':'answer.py','content':'print(2 ** 6)\n'}})
            assert submitted.status_code==200,submitted.text
            execute_http_operation()
            submitted=client.post(f'/api/projects/integration/workspaces/{wid}/operations',headers=admin,
                                  json={'action':'command','payload':{'command':'python answer.py'}})
            assert submitted.status_code==200
            completed=execute_http_operation()
            assert completed['exit_code']==0 and completed['stdout'].strip()=='64'
            submitted=client.post(f'/api/projects/integration/workspaces/{wid}/operations',headers=admin,
                                  json={'action':'export_file','payload':{'path':'answer.py'}})
            assert submitted.status_code==200
            artifact=execute_http_operation()['artifact']
            fetched=client.get(f"/api/projects/integration/artifacts/{artifact['id']}",headers=admin)
            assert fetched.status_code==200 and fetched.content==b'print(2 ** 6)\n'
            stopped=client.post(f'/api/projects/integration/workspaces/{wid}/stop',headers=admin)
            assert stopped.status_code==200
            execute_http_operation()
            state=client.get('/api/projects/integration/workspaces',headers=admin)
            assert state.status_code==200
            assert state.json()[0]['status']=='stopped'
        finally:
            if wid:
                name='pwm-'+wid
                subprocess.run(['docker','rm','-f',name],capture_output=True,check=False)
                subprocess.run(['docker','volume','rm',name],capture_output=True,check=False)
