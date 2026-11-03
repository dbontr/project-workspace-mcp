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
