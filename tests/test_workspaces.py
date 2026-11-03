"""Project isolation, pairing, reservation, lease fencing, and artifact retention."""
import base64
import hashlib

import pytest

from project_workspace.core import Store


@pytest.fixture
def store(tmp_path):
    database=Store(tmp_path/'state.db')
    database.project_create('alpha','Alpha')
    database.project_create('beta','Beta')
    token=database.node_register('worker-one',{'cpu':2,'memory_mb':4096,'max_jobs':1,'docker':True})
    return database


def ready_workspace(store):
    ws=store.workspace_create('alpha',cpu=1,memory_mb=512)
    start=store.operation_claim('worker-one')
    assert start['workspace']==ws['id'] and start['action']=='start'
    assert store.operation_finish('worker-one',start['id'],start['lease_token'],True,{'container':'pwm-x'})['status']=='succeeded'
    assert store.workspace_get('alpha',ws['id'])['status']=='ready'
    return ws


def test_pairing_code_single_use_and_no_existing_identity(store):
    code=store.pairing_create()['pairing_code']
    got=store.pairing_redeem(code,'worker-two',{'cpu':4,'memory_mb':2048,'max_jobs':1})
    assert store.node_auth('worker-two',got['worker_token'])
    with pytest.raises(ValueError,match='expired'):
        store.pairing_redeem(code,'worker-three',{'cpu':4,'memory_mb':2048,'max_jobs':1})
    code2=store.pairing_create()['pairing_code']
    with pytest.raises(ValueError,match='already registered'):
        store.pairing_redeem(code2,'worker-two',{'cpu':4,'memory_mb':2048,'max_jobs':1})


def test_worker_resource_reservation_and_project_isolation(store):
    ws=ready_workspace(store)
    with pytest.raises(ValueError,match='not found'):
        store.workspace_get('beta',ws['id'])
    with pytest.raises(ValueError,match='No online worker'):
        store.workspace_create('alpha',cpu=2,memory_mb=512)
    with pytest.raises(ValueError,match='not ready'):
        store.workspace_call('beta',ws['id'],'command',{'command':'echo nope'})
    with pytest.raises(ValueError,match='Unsupported'):
        store.workspace_call('alpha',ws['id'],'execute_host',{'command':'cat /etc/shadow'})


def test_workspace_operation_and_lease_fencing(store):
    ws=ready_workspace(store)
    call=store.workspace_call('alpha',ws['id'],'command',{'command':'echo ok'})
    op=store.operation_claim('worker-one')
    assert op['id']==call['operation_id']
    with pytest.raises(ValueError,match='Invalid or expired'):
        store.operation_finish('worker-one',op['id'],'wrong',True,{'exit_code':0})
    result=store.operation_finish('worker-one',op['id'],op['lease_token'],True,{'exit_code':0,'stdout':'ok'})
    assert result['status']=='succeeded'
    assert store.workspace_operation('alpha',ws['id'],op['id'])['result']['stdout']=='ok'
    with pytest.raises(ValueError,match='not found'):
        store.workspace_operation('beta',ws['id'],op['id'])
    with pytest.raises(ValueError,match='Invalid or expired'):
        store.operation_finish('worker-one',op['id'],op['lease_token'],True,{'stdout':'changed'})


def test_binary_artifact_export_and_stop(store):
    ws=ready_workspace(store)
    data=b'\x00\x80example\n'
    submitted=store.workspace_call('alpha',ws['id'],'export_file',{'path':'output.dat'})
    op=store.operation_claim('worker-one')
    output={'name':'output.dat','base64':base64.b64encode(data).decode()}
    finished=store.operation_finish('worker-one',op['id'],op['lease_token'],True,output)
    artifact=finished['result']['artifact']
    assert artifact['sha256']==hashlib.sha256(data).hexdigest()
    assert store.artifact_get('alpha',artifact['id'])[1]==data
    with pytest.raises(ValueError,match='not found'):
        store.artifact_get('beta',artifact['id'])
    stopped=store.workspace_stop('alpha',ws['id'])
    assert stopped['status']=='stopping'
    stop=store.operation_claim('worker-one')
    assert stop['action']=='stop'
    store.operation_finish('worker-one',stop['id'],stop['lease_token'],True,{})
    assert store.workspace_get('alpha',ws['id'])['status']=='stopped'
    assert store.artifact_get('alpha',artifact['id'])[1]==data


def test_repository_import_is_project_scoped(store):
    ws=ready_workspace(store)
    store.repo_link('alpha','example/source')
    artifact=store.artifact_save('alpha','source.tar.gz',b'archive')
    with pytest.raises(ValueError,match='not linked'):
        store.workspace_import('alpha',ws['id'],'another/source','a'*40,artifact['id'])
    receipt=store.workspace_import('alpha',ws['id'],'example/source','a'*40,artifact['id'])
    assert store.worker_artifact('worker-one',ws['id'],artifact['id'])[1]==b'archive'
    with pytest.raises(ValueError,match='not authorized'):
        store.worker_artifact('worker-two',ws['id'],artifact['id'])
    op=store.operation_claim('worker-one')
    assert op['id']==receipt['operation_id']
    store.operation_finish('worker-one',op['id'],op['lease_token'],True,{'files_bytes':10})
    assert store.workspace_repositories('alpha',ws['id'])==[{'repository':'example/source','revision':'a'*40}]


def test_stale_operation_claim_is_not_silently_reused(store,monkeypatch):
    ws=ready_workspace(store)
    submitted=store.workspace_call('alpha',ws['id'],'command',{'command':'sleep 1'})
    leased=store.operation_claim('worker-one')
    import project_workspace.core as core
    original=core.time.time()
    monkeypatch.setattr(core.time,'time',lambda:original+421)
    assert store.operation_claim('worker-one') is None
    assert store.workspace_operation('alpha',ws['id'],submitted['operation_id'])['status']=='expired'
    with pytest.raises(ValueError,match='expired'):
        store.operation_finish('worker-one',leased['id'],leased['lease_token'],True,{})
